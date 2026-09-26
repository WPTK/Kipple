// Package imgcache is the bounded on-disk image cache under the image proxy
// (design §7.4, backend additions §2).
//
// Layout under Dir:
//
//	v1/<k[0:2]>/<k>   one file per cached image; k = first 32 hex of sha256("img-v1|orig|<flags>|<url>")
//	tmp/              half-written downloads, renamed into v1/ when complete
//	index.db          its own SQLite file: one row per key, plus per-host hotlink hints
//
// The index is deliberately not kipple.db: per-hit LRU bookkeeping must never
// touch the main database's single writer. It is a cache, so a corrupt or
// old-schema index is renamed aside and rebuilt empty, and the startup repair
// then deletes the files that no longer have rows.
//
// The size cap is enforced by LRU eviction (down to 90% of the cap), entries
// idle for 60 days expire, and failures are remembered for a short while so a
// dead image is not refetched on every list render. Writes are refused when the
// volume would drop below a free-space floor.
package imgcache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// VariantThumb is the variant of the list-card thumbnail (800 px wide).
const VariantThumb = "t800"

// Defaults.
const (
	DefaultMaxObject   = 15 << 20
	DefaultMinFree     = 2 << 30 // bytes; the floor is max(this, 5% of the volume)
	DefaultIdleExpiry  = 60 * 24 * time.Hour
	DefaultFlushEvery  = 60 * time.Second
	DefaultSweepEvery  = 10 * time.Minute
	evictTargetPct     = 90
	lowDiskTargetPct   = 50
	maxNegEntries      = 20000 // failure rows (in-progress markers are counted apart)
	maxMarkerEntries   = 5000
	freshMin           = 24 * time.Hour
	freshMax           = 30 * 24 * time.Hour
	freshDefault       = 7 * 24 * time.Hour
	negTransientBase   = 10 * time.Minute
	negMax             = 24 * time.Hour
	negPermanent       = 24 * time.Hour
	hostHintExpiry     = 90 * 24 * time.Hour
	lowDiskWarnEvery   = time.Hour
	lowDiskEvictEvery  = time.Minute
	evictBatch         = 200
	statusOK           = "ok"
	statusNeg          = "neg"
	variantOrig        = "orig"
	keyHexLen          = 32
	tmpSubdir          = "tmp"
	filesSubdir        = "v1"
	maxHintHostLen     = 253
	maxStoredURLLength = 4096
	blockSize          = 4096  // the least a cached file costs on disk
	maxHostHints       = 10000 // hotlink hint rows; past that the least recently updated go
)

// Errors.
var (
	ErrDisabled = errors.New("imgcache: disabled")
	ErrDiskLow  = errors.New("imgcache: free disk space below the floor")
	ErrTooLarge = errors.New("imgcache: object over the size limit")
	ErrMissing  = errors.New("imgcache: file missing")
	ErrClosed   = errors.New("imgcache: closed")
)

// Options configures Open.
type Options struct {
	Dir      string // required; created 0700
	MaxBytes int64  // the cap; 0 disables the cache (nothing is written or served)
	Logger   *slog.Logger

	MaxObject   int64         // per-image limit; default DefaultMaxObject
	MinFree     int64         // free-space floor in bytes; default DefaultMinFree (also 5% of the volume, whichever is larger)
	IdleExpiry  time.Duration // default 60 days
	FlushEvery  time.Duration // access-time flush interval; default 60 s
	SweepEvery  time.Duration // background eviction interval; default 10 min
	Now         func() time.Time
	DiskSpace   func(dir string) (free, total uint64, err error) // tests inject; default is the OS call
	NoBackgound bool                                             // tests: no background goroutine (flush and sweep by hand)
	// ByteAccounting charges each entry its file size alone against the cap
	// (tests of the eviction arithmetic). By default an entry costs its size
	// rounded up to whole 4 KiB blocks plus its URL (the index row), and the
	// downloads in progress in tmp/ count too.
	ByteAccounting bool
}

// Meta describes a fetched image for Commit.
type Meta struct {
	ContentType  string
	ETag         string
	LastModified string
	FreshFor     time.Duration // 0 means the default (7 days)
	Variant      string        // "" is the original; VariantThumb for a thumbnail
}

// Entry is one index row.
type Entry struct {
	Key          string
	URL          string
	Flags        int
	OK           bool // false: a remembered failure
	ContentType  string
	Size         int64
	SHA256       string
	ETag         string
	LastModified string
	FetchedAt    time.Time
	FreshUntil   time.Time // for a failure this is when the retry-after ends
	LastAccess   time.Time
	Hits         int64
	NegStatus    int // upstream status, 415 for a type refusal, 0 for a transport error
	NegReason    string
	NegCount     int
}

// Fresh reports whether e can be served (or, for a failure, replayed) without
// contacting the source.
func (e Entry) Fresh(now time.Time) bool { return now.Before(e.FreshUntil) }

// Stats is a snapshot for the API.
type Stats struct {
	Enabled      bool
	MaxBytes     int64
	UsedBytes    int64 // the cached files' bytes
	ChargedBytes int64 // what counts against MaxBytes: whole 4 KiB blocks plus URLs, and the downloads in progress
	Files        int64
	NegEntries   int64
	Hits         int64
	Misses       int64
	Evictions    int64
	Failures     int64 // failures recorded (negative entries written)
	Thumbnails   int64 // cached thumbnails (a subset of Files)
	Since        time.Time
	OldestAccess time.Time // zero when empty
	DiskFree     uint64
	DiskTotal    uint64
	DiskFloor    uint64
	LowDisk      bool // the last write attempt was refused for lack of disk
}

// Cache is the image cache. Safe for concurrent use.
type Cache struct {
	o        Options
	log      *slog.Logger
	now      func() time.Time
	dir      string
	filesDir string
	tmpDir   string
	wr, rd   *sql.DB

	mu sync.Mutex // serializes every index mutation and the used/files counters

	used, files, negN  atomic.Int64 // negN counts failure rows and in-progress markers
	charged            atomic.Int64 // what the ok entries cost against the cap (cost)
	tmpBytes           atomic.Int64 // bytes written to downloads in progress (tmp/)
	markN              atomic.Int64 // of negN, the in-progress markers (failures are negN - markN)
	maxNeg, maxMarkers int64        // the caps of each (maxNegEntries, maxMarkerEntries; tests lower them)
	maxHosts           int          // the cap of the hosts table (maxHostHints; tests lower it)
	maxBytes           atomic.Int64
	hits, misses       atomic.Int64
	evictions, fails   atomic.Int64
	since              time.Time
	lowDisk            atomic.Bool
	lastWarn           atomic.Int64 // unix seconds
	lastLowEvict       atomic.Int64
	evictWarn          warnGate

	amu    sync.Mutex
	access map[string]accessRec

	fmu     sync.Mutex
	flights map[string]chan struct{}

	closed atomic.Bool
	cancel context.CancelFunc
	ctx    context.Context // cancelled by Close; bounds every eviction
	wg     sync.WaitGroup
}

type accessRec struct {
	at   int64
	hits int64
}

// Key returns the cache key of (variant, flags, url): the first 32 hex characters
// of sha256("img-v1|<variant>|<flags>|<url>"). The variant is "orig" for the
// image as served by the source.
func Key(variant string, flags int, url string) string {
	sum := sha256.Sum256([]byte("img-v1|" + variant + "|" + strconv.Itoa(flags) + "|" + url))
	return hex.EncodeToString(sum[:])[:keyHexLen]
}

// KeyOrig is Key for the original image.
func KeyOrig(flags int, url string) string { return Key(variantOrig, flags, url) }

// KeyThumb is Key for the card thumbnail of an image. flags are the fetch flags
// of the source (without the proxy's thumbnail bit), so the thumbnail and the
// original are separate entries that share one fetch.
func KeyThumb(flags int, url string) string { return Key(VariantThumb, flags, url) }

// Open creates the directory tree, opens the index, repairs it against the files
// on disk and starts the background flush/eviction goroutine.
func Open(o Options) (*Cache, error) {
	if o.Dir == "" {
		return nil, errors.New("imgcache: empty directory")
	}
	if o.MaxObject <= 0 {
		o.MaxObject = DefaultMaxObject
	}
	if o.MinFree <= 0 {
		o.MinFree = DefaultMinFree
	}
	if o.IdleExpiry <= 0 {
		o.IdleExpiry = DefaultIdleExpiry
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DiskSpace == nil {
		o.DiskSpace = diskSpace
	}
	if o.MaxBytes < 0 {
		o.MaxBytes = 0
	}
	c := &Cache{
		o: o, log: o.Logger, now: o.Now, dir: o.Dir,
		filesDir: filepath.Join(o.Dir, filesSubdir), tmpDir: filepath.Join(o.Dir, tmpSubdir),
		access: map[string]accessRec{}, flights: map[string]chan struct{}{}, since: o.Now(),
		maxNeg: maxNegEntries, maxMarkers: maxMarkerEntries, maxHosts: maxHostHints,
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.maxBytes.Store(o.MaxBytes)
	for _, d := range []string{c.dir, c.filesDir, c.tmpDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("imgcache: %w", err)
		}
	}
	var rebuilt bool
	var err error
	c.wr, c.rd, rebuilt, err = openIndex(c.dir, c.now)
	if err != nil {
		return nil, fmt.Errorf("imgcache: index: %w", err)
	}
	if rebuilt {
		c.log.Warn("imgcache: index was corrupt or from another version; moved aside and rebuilt empty")
	}
	if err := c.repair(); err != nil {
		_ = c.wr.Close()
		_ = c.rd.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.ctx = ctx
	if !o.NoBackgound {
		c.wg.Add(1)
		go c.loop(ctx)
	}
	return c, nil
}

// Close flushes access times, stops the background goroutine and closes the index.
func (c *Cache) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.cancel()
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.flushLocked()
	return errors.Join(c.wr.Close(), c.rd.Close())
}

func (c *Cache) loop(ctx context.Context) {
	defer c.wg.Done()
	flush := time.NewTicker(c.o.FlushEvery)
	sweep := time.NewTicker(c.o.SweepEvery)
	defer flush.Stop()
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-flush.C:
			c.mu.Lock()
			if err := c.flushLocked(); err != nil {
				c.log.Debug("imgcache: flush", "err", err)
			}
			c.mu.Unlock()
		case <-sweep.C:
			if _, err := c.Sweep(ctx, false); ctx.Err() == nil {
				c.noteEvict(err, "imgcache: sweep")
			}
		}
	}
}

// Dir is the cache directory.
func (c *Cache) Dir() string { return c.dir }

// Enabled reports whether the cache stores anything (cap above zero).
func (c *Cache) Enabled() bool { return c != nil && !c.closed.Load() && c.maxBytes.Load() > 0 }

// MaxBytes is the current cap.
func (c *Cache) MaxBytes() int64 { return c.maxBytes.Load() }

func (c *Cache) path(key string) string { return filepath.Join(c.filesDir, key[:2], key) }

// cost is what one cached file counts against the cap: its size in whole 4
// KiB blocks (a 100-byte file still takes a block on disk) plus its URL (the
// bulk of its index row). With ByteAccounting it is the size alone.
func (c *Cache) cost(size int64, urlLen int) int64 {
	if c.o.ByteAccounting {
		return size
	}
	return (size+blockSize-1)/blockSize*blockSize + int64(urlLen)
}

// costSQL is cost as an SQL expression over an entries row.
func (c *Cache) costSQL() string {
	if c.o.ByteAccounting {
		return "size"
	}
	return "((size + 4095) / 4096) * 4096 + length(CAST(url AS BLOB))"
}

// load is what counts against the cap: the cached files' cost plus the bytes
// of the downloads in progress.
func (c *Cache) load() int64 { return c.charged.Load() + c.tmpBytes.Load() }

func validKey(k string) bool {
	if len(k) != keyHexLen {
		return false
	}
	for _, r := range k {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

const entryCols = `key, url, flags, status, content_type, size, sha256, etag, last_modified,
	fetched_at, fresh_until, last_access_at, hits, neg_status, neg_reason, neg_count`

type scanner interface{ Scan(dest ...any) error }

func scanEntry(s scanner) (Entry, error) {
	var e Entry
	var status string
	var fetched, fresh, acc int64
	if err := s.Scan(&e.Key, &e.URL, &e.Flags, &status, &e.ContentType, &e.Size, &e.SHA256, &e.ETag, &e.LastModified,
		&fetched, &fresh, &acc, &e.Hits, &e.NegStatus, &e.NegReason, &e.NegCount); err != nil {
		return Entry{}, err
	}
	e.OK = status == statusOK
	e.FetchedAt, e.FreshUntil, e.LastAccess = time.Unix(fetched, 0), time.Unix(fresh, 0), time.Unix(acc, 0)
	return e, nil
}

// Lookup returns the entry for key. Serving an ok entry counts as a hit and
// refreshes its LRU position (buffered in memory, flushed every minute); a
// failure entry whose retry-after has passed, or no entry, counts as a miss.
// The file itself is checked by OpenFile.
func (c *Cache) Lookup(ctx context.Context, key string) (Entry, bool, error) {
	if c.closed.Load() {
		return Entry{}, false, ErrClosed
	}
	if !validKey(key) {
		return Entry{}, false, nil
	}
	e, err := scanEntry(c.rd.QueryRowContext(ctx, "SELECT "+entryCols+" FROM entries WHERE key = ?", key))
	if errors.Is(err, sql.ErrNoRows) {
		c.misses.Add(1)
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	now := c.now()
	if !e.OK && !e.Fresh(now) {
		c.misses.Add(1)
		return Entry{}, false, nil
	}
	c.hits.Add(1)
	c.touch(key, now) // a replayed failure is used too: pruning keeps the recently replayed ones
	return e, true, nil
}

// Peek is Lookup without the hit/miss counters and without refreshing the LRU
// position (a leader re-checking the index after it won the flight).
func (c *Cache) Peek(ctx context.Context, key string) (Entry, bool) {
	if c.closed.Load() || !validKey(key) {
		return Entry{}, false
	}
	e, err := scanEntry(c.rd.QueryRowContext(ctx, "SELECT "+entryCols+" FROM entries WHERE key = ?", key))
	if err != nil || (!e.OK && !e.Fresh(c.now())) {
		return Entry{}, false
	}
	return e, true
}

// Now is the cache's clock, so freshness decisions use the same time as the index.
func (c *Cache) Now() time.Time { return c.now() }

// Touch refreshes key's LRU position without a lookup (the original behind a
// thumbnail that was just served, so the original is not evicted long before
// the thumbnail that needs it to be remade). It does not count as a hit.
func (c *Cache) Touch(key string) {
	if c.closed.Load() || !validKey(key) {
		return
	}
	now := c.now()
	c.amu.Lock()
	r := c.access[key]
	r.at = now.Unix()
	c.access[key] = r
	c.amu.Unlock()
}

func (c *Cache) touch(key string, now time.Time) {
	c.amu.Lock()
	r := c.access[key]
	r.at, r.hits = now.Unix(), r.hits+1
	c.access[key] = r
	c.amu.Unlock()
}

// OpenFile opens the cached file of an ok entry. When the file is gone the
// row is deleted and ErrMissing returned, so the caller refetches.
func (c *Cache) OpenFile(key string) (*os.File, error) {
	if !validKey(key) {
		return nil, ErrMissing
	}
	f, err := os.Open(c.path(key))
	if err == nil {
		return f, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		c.mu.Lock()
		if derr := c.dropLocked(key, false); derr != nil {
			c.log.Debug("imgcache: dropping a row whose file vanished", "err", derr)
		}
		c.mu.Unlock()
		return nil, ErrMissing
	}
	return nil, err
}

// Revalidated records a 304: the entry is fresh again for freshFor (0 = the
// default), with the validators the source sent (empty keeps the stored ones).
// It also clears the revalidation back-off of DeferRevalidation.
func (c *Cache) Revalidated(key string, freshFor time.Duration, etag, lastModified string) error {
	if freshFor <= 0 {
		freshFor = freshDefault
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.wr.Exec(`UPDATE entries SET fetched_at = ?, fresh_until = ?, neg_count = 0,
		etag = CASE WHEN ? = '' THEN etag ELSE ? END,
		last_modified = CASE WHEN ? = '' THEN last_modified ELSE ? END
		WHERE key = ? AND status = 'ok'`,
		now.Unix(), now.Add(freshFor).Unix(), etag, etag, lastModified, lastModified, key)
	return err
}

// DeferRevalidation records a failed revalidation of a stale ok entry (the
// source answered an error, a 4xx or something that is not an image) without
// touching its file: the stale copy is served, and counts as fresh, for 10
// minutes, doubling with each consecutive failure up to 24 hours. The entry
// keeps its fetched_at and validators; neg_count counts the failures and a
// later Revalidated or Commit resets it.
func (c *Cache) DeferRevalidation(key string) error {
	if !c.Enabled() || !validKey(key) {
		return ErrDisabled
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int
	if err := c.wr.QueryRow("SELECT neg_count FROM entries WHERE key = ? AND status = 'ok'", key).Scan(&n); err != nil {
		return err
	}
	_, err := c.wr.Exec("UPDATE entries SET fresh_until = ?, neg_count = ? WHERE key = ? AND status = 'ok'",
		now.Add(backoff(n)).Unix(), n+1, key)
	return err
}

// backoff is the transient retry-after after n earlier consecutive failures:
// 10 minutes, doubling, at most 24 hours.
func backoff(n int) time.Duration {
	ttl := negTransientBase
	for i := 0; i < n && ttl < negMax; i++ {
		ttl *= 2
	}
	return min(ttl, negMax)
}

// Delete removes one entry and its file.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	if err := c.dropLocked(key, false); err != nil {
		c.log.Debug("imgcache: delete", "err", err)
	}
	c.mu.Unlock()
}

// dropLocked deletes a row and, when it was an ok entry, its file. mu is held.
// A missing row is not an error; a failing index is, so a loop over keys can stop.
func (c *Cache) dropLocked(key string, evicted bool) error {
	var status, reason string
	var size int64
	var urlLen int
	err := c.wr.QueryRow("SELECT status, size, neg_reason, length(CAST(url AS BLOB)) FROM entries WHERE key = ?", key).Scan(&status, &size, &reason, &urlLen)
	if errors.Is(err, sql.ErrNoRows) {
		c.forgetAccess(key)
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := c.wr.Exec("DELETE FROM entries WHERE key = ?", key); err != nil {
		return err
	}
	c.forgetAccess(key)
	if status == statusOK {
		_ = os.Remove(c.path(key)) // a failure (an open file on Windows) leaves an orphan the next start removes
		c.used.Add(-size)
		c.charged.Add(-c.cost(size, urlLen))
		c.files.Add(-1)
		if evicted {
			c.evictions.Add(1)
		}
	} else {
		c.negN.Add(-1)
		if reason == InProgress {
			c.markN.Add(-1)
		}
	}
	return nil
}

func (c *Cache) forgetAccess(key string) {
	c.amu.Lock()
	delete(c.access, key)
	c.amu.Unlock()
}

// Flight makes at most one caller per key the leader. A follower gets a channel
// that closes when the leader is done (its entry, or its failure record, is then
// in the index); it should wait on it and look the key up again. The leader must
// call release exactly once.
func (c *Cache) Flight(key string) (leader bool, done <-chan struct{}, release func()) {
	c.fmu.Lock()
	defer c.fmu.Unlock()
	if ch, ok := c.flights[key]; ok {
		return false, ch, nil
	}
	ch := make(chan struct{})
	c.flights[key] = ch
	var once sync.Once
	return true, ch, func() {
		once.Do(func() {
			c.fmu.Lock()
			delete(c.flights, key)
			c.fmu.Unlock()
			close(ch)
		})
	}
}

// flushLocked writes buffered access times and hit counts to the index. mu is held.
func (c *Cache) flushLocked() error {
	c.amu.Lock()
	if len(c.access) == 0 {
		c.amu.Unlock()
		return nil
	}
	batch := c.access
	c.access = map[string]accessRec{}
	c.amu.Unlock()
	tx, err := c.wr.Begin()
	if err != nil {
		c.requeue(batch)
		return err
	}
	st, err := tx.Prepare("UPDATE entries SET last_access_at = max(last_access_at, ?), hits = hits + ? WHERE key = ?")
	if err != nil {
		_ = tx.Rollback()
		c.requeue(batch)
		return err
	}
	defer st.Close()
	for k, r := range batch {
		if _, err := st.Exec(r.at, r.hits, k); err != nil {
			_ = tx.Rollback()
			c.requeue(batch)
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		c.requeue(batch)
		return err
	}
	return nil
}

func (c *Cache) requeue(batch map[string]accessRec) {
	c.amu.Lock()
	defer c.amu.Unlock()
	for k, r := range batch {
		cur := c.access[k]
		if r.at > cur.at {
			cur.at = r.at
		}
		cur.hits += r.hits
		c.access[k] = cur
	}
}

// Flush writes buffered access times now (tests, and before eviction).
func (c *Cache) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flushLocked()
}

// SetCap changes the cap. A lower cap evicts (in the background) down to 90% of
// it; 0 disables the cache and purges everything.
func (c *Cache) SetCap(bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	c.maxBytes.Store(bytes)
	if c.closed.Load() {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if bytes == 0 {
			if _, err := c.Clear(); err != nil {
				c.log.Warn("imgcache: purge on disable", "err", err)
			}
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.load() > bytes {
			c.noteEvict(c.evictLocked(c.ctx, bytes*evictTargetPct/100), "imgcache: evict after cap change")
		}
	}()
}

// errNoProgress stops an eviction or expiry loop whose batch removed nothing.
var errNoProgress = errors.New("imgcache: eviction made no progress")

// noteEvict logs an eviction failure at WARN, at most once per back-off period
// (1 minute, doubling to 1 hour while the failures go on); a success resets it.
func (c *Cache) noteEvict(err error, msg string) {
	if err == nil {
		c.evictWarn.reset()
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if c.evictWarn.allow(c.now()) {
		c.log.Warn(msg, "err", err)
	}
}

// warnGate rate-limits one repeating warning with a doubling back-off.
type warnGate struct {
	mu    sync.Mutex
	next  time.Time
	every time.Duration
}

func (g *warnGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Before(g.next) {
		return false
	}
	switch {
	case g.every == 0:
		g.every = time.Minute
	case g.every < time.Hour:
		g.every = min(2*g.every, time.Hour)
	}
	g.next = now.Add(g.every)
	return true
}

func (g *warnGate) reset() {
	g.mu.Lock()
	g.every, g.next = 0, time.Time{}
	g.mu.Unlock()
}

// evictLocked deletes least-recently-used files until the load (their cost
// plus the downloads in progress) is at most target. mu is held. It stops at
// the first index error (a full or failing disk) and on a batch that frees
// nothing, so it can never spin while holding mu.
func (c *Cache) evictLocked(ctx context.Context, target int64) error {
	if err := c.flushLocked(); err != nil {
		return err
	}
	target = max(target-c.tmpBytes.Load(), 0) // the downloads in progress are not evictable: make room for them
	for c.charged.Load() > target {
		if err := ctx.Err(); err != nil {
			return err
		}
		keys, err := c.keysLocked(ctx, "SELECT key FROM entries WHERE status = 'ok' ORDER BY last_access_at, fetched_at, key LIMIT ?", evictBatch)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			// Counters drifted (nothing left to evict): resync from the index.
			return c.recountLocked()
		}
		before := c.charged.Load()
		dropped := 0
		for _, k := range keys {
			if c.charged.Load() <= target {
				break
			}
			if err := c.dropLocked(k, true); err != nil {
				return err
			}
			dropped++
		}
		if dropped == 0 || (c.charged.Load() >= before && c.charged.Load() > target) {
			// Rows that do not go away or free nothing: resync and stop rather than re-select them forever.
			if err := c.recountLocked(); err != nil {
				return err
			}
			if c.charged.Load() > target {
				return errNoProgress
			}
		}
	}
	return nil
}

// keysLocked runs a one-column key query. mu is held.
func (c *Cache) keysLocked(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := c.wr.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (c *Cache) recountLocked() error {
	var used, charged, files, neg int64
	if err := c.wr.QueryRow("SELECT COALESCE(SUM(size),0), COALESCE(SUM("+c.costSQL()+"),0), count(*) FROM entries WHERE status = 'ok'").Scan(&used, &charged, &files); err != nil {
		return err
	}
	if err := c.wr.QueryRow("SELECT count(*) FROM entries WHERE status = 'neg'").Scan(&neg); err != nil {
		return err
	}
	c.used.Store(used)
	c.charged.Store(charged)
	c.files.Store(files)
	c.negN.Store(neg)
	return c.recountMarkersLocked()
}

// recountMarkersLocked reloads the in-progress marker count from the index (after a bulk delete
// that does not know which rows it removed). mu is held.
func (c *Cache) recountMarkersLocked() error {
	var n int64
	if err := c.wr.QueryRow("SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason = ?", InProgress).Scan(&n); err != nil {
		return err
	}
	c.markN.Store(n)
	return nil
}

// negOverCapLocked reports whether the failure rows or the in-progress markers are past their own
// cap (each is checked against its own, so a flood of one cannot hide behind the other's room).
func (c *Cache) negOverCap() bool {
	marks := c.markN.Load()
	return marks > c.maxMarkers || c.negN.Load()-marks > c.maxNeg
}

// Clear deletes every file, entry and host hint. It returns how many cached
// images it removed.
func (c *Cache) Clear() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.files.Load()
	if _, err := c.wr.Exec("DELETE FROM entries"); err != nil {
		return 0, err
	}
	if _, err := c.wr.Exec("DELETE FROM hosts"); err != nil {
		return 0, err
	}
	c.amu.Lock()
	c.access = map[string]accessRec{}
	c.amu.Unlock()
	ents, err := os.ReadDir(c.filesDir)
	if err != nil {
		return n, err
	}
	var errs []error
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(c.filesDir, e.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	c.used.Store(0)
	c.charged.Store(0)
	c.files.Store(0)
	c.negN.Store(0)
	c.markN.Store(0)
	return n, errors.Join(errs...)
}

// Stats returns a snapshot.
func (c *Cache) Stats() Stats {
	s := Stats{
		Enabled: c.Enabled(), MaxBytes: c.maxBytes.Load(), UsedBytes: c.used.Load(), ChargedBytes: c.load(), Files: c.files.Load(),
		NegEntries: c.negN.Load(), Hits: c.hits.Load(), Misses: c.misses.Load(), Evictions: c.evictions.Load(),
		Failures: c.fails.Load(), Since: c.since, LowDisk: c.lowDisk.Load(),
	}
	if c.closed.Load() {
		return s
	}
	var oldest sql.NullInt64
	if err := c.rd.QueryRow("SELECT min(last_access_at) FROM entries WHERE status = 'ok'").Scan(&oldest); err == nil && oldest.Valid {
		s.OldestAccess = time.Unix(oldest.Int64, 0)
	}
	_ = c.rd.QueryRow("SELECT count(*) FROM entries WHERE status = 'ok' AND variant <> 'orig'").Scan(&s.Thumbnails)
	if free, total, err := c.o.DiskSpace(c.dir); err == nil {
		s.DiskFree, s.DiskTotal, s.DiskFloor = free, total, c.floor(total)
	}
	return s
}

// DiskBytes is the cache's footprint on disk without walking it: what the
// cached files and the downloads in progress count against the cap
// (Stats().ChargedBytes: whole 4 KiB blocks plus URLs) plus the index file,
// its WAL and shared-memory file.
func (c *Cache) DiskBytes() int64 {
	n := c.load()
	base := filepath.Join(c.dir, "index.db")
	for _, p := range []string{base, base + "-wal", base + "-shm"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// floor is the free-space floor for a volume of the given size.
func (c *Cache) floor(total uint64) uint64 {
	f := uint64(c.o.MinFree)
	if pct := total / 20; pct > f {
		f = pct
	}
	return f
}

// NegKind classifies a failure for its retry-after.
type NegKind int

const (
	// NegTransient is a 5xx, timeout, DNS or connection failure: retry after 10
	// minutes, doubling with each repeat up to 24 hours.
	NegTransient NegKind = iota
	// NegPermanent is a 404, 410, 403, an unsupported type or an oversize
	// image: retry after 24 hours.
	NegPermanent
)

// PutNeg remembers a failure so the source is not contacted again until the
// retry-after passes. It replaces whatever the key held (an ok entry loses its file).
func (c *Cache) PutNeg(key, url string, flags int, kind NegKind, status int, reason string) error {
	return c.PutNegVariant(key, url, flags, variantOrig, kind, status, reason)
}

// PutNegVariant is PutNeg for an entry of the given variant (a thumbnail that
// could not be made is remembered so it is not attempted again until the retry-after).
func (c *Cache) PutNegVariant(key, url string, flags int, variant string, kind NegKind, status int, reason string) error {
	return c.putNeg(key, url, flags, variant, kind, status, reason, true)
}

// InProgress is the neg_reason of an in-progress marker (PutInProgress).
const InProgress = "in progress"

// PutInProgress writes an in-progress marker on key before risky work (a
// thumbnail decode) starts: a transient failure entry (10 minutes, doubling
// with each marker that was never replaced, up to 24 hours) with the reason
// InProgress. The worker replaces it with its result. If the process dies
// instead (an OOM kill records nothing), the marker is what the next start
// finds, so the work is not retried in a crash loop. It does not count as a
// failure in Stats, and Sweep keeps expired markers for two days so the
// doubling survives the expiry.
func (c *Cache) PutInProgress(key, url string, flags int, variant string) error {
	return c.putNeg(key, url, flags, variant, NegTransient, 0, InProgress, false)
}

func (c *Cache) putNeg(key, url string, flags int, variant string, kind NegKind, status int, reason string, countFail bool) error {
	if !c.Enabled() || !validKey(key) {
		return ErrDisabled
	}
	if len(url) > maxStoredURLLength {
		url = truncateURL(url)
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var oldStatus, oldReason string
	var oldSize int64
	var oldNeg, oldURLLen int
	err := c.wr.QueryRow("SELECT status, size, neg_count, neg_reason, length(CAST(url AS BLOB)) FROM entries WHERE key = ?", key).Scan(&oldStatus, &oldSize, &oldNeg, &oldReason, &oldURLLen)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	count := 0
	ttl := negPermanent
	if kind == NegTransient {
		switch {
		case exists && oldStatus == statusNeg && oldReason == InProgress && reason != InProgress:
			count = oldNeg // the work's own outcome replaces its marker: not another repeat
		case exists && oldStatus == statusNeg:
			count = oldNeg + 1
		}
		ttl = backoff(count)
	}
	if _, err := c.wr.Exec(`INSERT OR REPLACE INTO entries (key, url, flags, variant, status, fetched_at, fresh_until, last_access_at,
		neg_status, neg_reason, neg_count) VALUES (?, ?, ?, ?, 'neg', ?, ?, ?, ?, ?, ?)`,
		key, url, flags, variant, now.Unix(), now.Add(ttl).Unix(), now.Unix(), status, truncate(reason, 200), count); err != nil {
		return err
	}
	if countFail {
		c.fails.Add(1)
	}
	switch {
	case exists && oldStatus == statusOK:
		_ = os.Remove(c.path(key))
		c.used.Add(-oldSize)
		c.charged.Add(-c.cost(oldSize, oldURLLen))
		c.files.Add(-1)
		c.negN.Add(1)
	case !exists:
		c.negN.Add(1)
	}
	if wasMarker := exists && oldStatus == statusNeg && oldReason == InProgress; wasMarker != (reason == InProgress) {
		if wasMarker {
			c.markN.Add(-1)
		} else {
			c.markN.Add(1)
		}
	}
	c.forgetAccess(key)
	if c.negOverCap() {
		c.pruneNegLocked()
	}
	return nil
}

// pruneNegLocked bounds the negative rows. In-progress markers are the crash
// protection of the thumbnailer, so ordinary failures never push them out:
// markers have their own cap (the oldest go first). Failures past theirs go
// expired first, then the least recently replayed. Each set is cut to 90% of
// its cap so pruning is rare. mu is held.
func (c *Cache) pruneNegLocked() {
	if err := c.flushLocked(); err != nil {
		c.log.Debug("imgcache: flush before pruning failures", "err", err)
	}
	now := c.now().Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`WITH n(c) AS (SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason = ?1)
			DELETE FROM entries WHERE key IN (SELECT key FROM entries WHERE status = 'neg' AND neg_reason = ?1
			ORDER BY fetched_at, key LIMIT (SELECT CASE WHEN c > ?2 THEN c - ?2 * 9 / 10 ELSE 0 END FROM n))`,
			[]any{InProgress, c.maxMarkers}},
		{`WITH n(c) AS (SELECT count(*) FROM entries WHERE status = 'neg' AND neg_reason <> ?1)
			DELETE FROM entries WHERE key IN (SELECT key FROM entries WHERE status = 'neg' AND neg_reason <> ?1
			ORDER BY fresh_until >= ?3, last_access_at, fetched_at, key
			LIMIT (SELECT CASE WHEN c > ?2 THEN c - ?2 * 9 / 10 ELSE 0 END FROM n))`,
			[]any{InProgress, c.maxNeg, now}},
	} {
		res, err := c.wr.Exec(q.sql, q.args...)
		if err != nil {
			c.log.Debug("imgcache: pruning failures", "err", err)
			return
		}
		if n, err := res.RowsAffected(); err == nil {
			c.negN.Add(-n)
		}
	}
	if err := c.recountMarkersLocked(); err != nil {
		c.log.Debug("imgcache: recount markers", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Freshness turns a Cache-Control header into how long an entry stays fresh: the
// max-age clamped to 1..30 days, 7 days when there is none, and 1 day for
// no-store/no-cache (the entry is still revalidated daily).
func Freshness(cacheControl string) time.Duration {
	for _, part := range strings.Split(strings.ToLower(cacheControl), ",") {
		part = strings.TrimSpace(part)
		if part == "no-store" || part == "no-cache" {
			return freshMin
		}
		if v, ok := strings.CutPrefix(part, "max-age="); ok {
			secs, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
			if err != nil {
				continue
			}
			d := time.Duration(secs) * time.Second
			if secs > int64(freshMax/time.Second) {
				d = freshMax
			}
			return min(max(d, freshMin), freshMax)
		}
	}
	return freshDefault
}

// SweepResult counts what a sweep removed.
type SweepResult struct {
	IdleExpired int64
	NegExpired  int64
	Evicted     int64
	Hosts       int64
}

// Rows is the total removed.
func (r SweepResult) Rows() int64 { return r.IdleExpired + r.NegExpired + r.Evicted + r.Hosts }

// Sweep flushes access times, removes entries idle for the expiry period and
// failures past their retry-after, evicts down to the cap, and prunes old host
// hints. With vacuum it then checkpoints and VACUUMs the index (the nightly job).
func (c *Cache) Sweep(ctx context.Context, vacuum bool) (SweepResult, error) {
	var r SweepResult
	if c.closed.Load() {
		return r, ErrClosed
	}
	now := c.now()
	c.mu.Lock()
	err := func() error {
		if err := c.flushLocked(); err != nil {
			return err
		}
		idleBefore := now.Add(-c.o.IdleExpiry).Unix()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			keys, err := c.keysLocked(ctx, "SELECT key FROM entries WHERE status = 'ok' AND last_access_at < ? LIMIT ?", idleBefore, evictBatch)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				break
			}
			for _, k := range keys {
				// A failing index stops the pass: the next batch would select the same keys forever.
				if err := c.dropLocked(k, false); err != nil {
					return err
				}
			}
			r.IdleExpired += int64(len(keys))
		}
		// Expired in-progress markers stay two more days, so a thumbnail that
		// crashes the process again finds its count and backs off further.
		res, err := c.wr.ExecContext(ctx, "DELETE FROM entries WHERE status = 'neg' AND fresh_until < ? AND (neg_reason <> ? OR fresh_until < ?)",
			now.Unix(), InProgress, now.Add(-2*negMax).Unix())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil {
			r.NegExpired = n
			c.negN.Add(-n)
		}
		if err := c.recountMarkersLocked(); err != nil {
			return err
		}
		if c.negOverCap() {
			c.pruneNegLocked()
		}
		res, err = c.wr.ExecContext(ctx, "DELETE FROM hosts WHERE updated_at < ?", now.Add(-hostHintExpiry).Unix())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil {
			r.Hosts = n
		}
		if cp := c.maxBytes.Load(); cp > 0 && c.load() > cp {
			before := c.evictions.Load()
			if err := c.evictLocked(ctx, cp*evictTargetPct/100); err != nil {
				return err
			}
			r.Evicted = c.evictions.Load() - before
		}
		return nil
	}()
	c.mu.Unlock()
	if err != nil {
		return r, err
	}
	if vacuum {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, err := c.wr.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return r, err
		}
		if _, err := c.wr.ExecContext(ctx, "VACUUM"); err != nil {
			return r, err
		}
	}
	return r, nil
}

// HostHint is the request shape that last worked against a host.
type HostHint struct {
	UA      string // "kipple" or "browser"
	Referer string // "none" or "self" (the image's own origin)
}

// HostHint returns the saved hotlink hint for host.
func (c *Cache) HostHint(host string) (HostHint, bool) {
	if c.closed.Load() || host == "" {
		return HostHint{}, false
	}
	var h HostHint
	err := c.rd.QueryRow("SELECT ua, referer FROM hosts WHERE host = ?", strings.ToLower(host)).Scan(&h.UA, &h.Referer)
	return h, err == nil
}

// SetHostHint saves (or, for the default shape, forgets) the hint for host.
func (c *Cache) SetHostHint(host string, h HostHint) error {
	host = strings.ToLower(host)
	if c.closed.Load() || host == "" || len(host) > maxHintHostLen {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.UA == "kipple" && h.Referer == "none" {
		_, err := c.wr.Exec("DELETE FROM hosts WHERE host = ?", host)
		return err
	}
	if _, err := c.wr.Exec("INSERT OR REPLACE INTO hosts (host, referer, ua, updated_at) VALUES (?, ?, ?, ?)", host, h.Referer, h.UA, c.now().Unix()); err != nil {
		return err
	}
	// The table is bounded (the 90-day expiry alone would let a feed of
	// ever-new hosts grow it without limit): past the cap the least recently
	// updated hints go, never the one just written.
	_, err := c.wr.Exec(`DELETE FROM hosts WHERE host <> ?1 AND host IN
		(SELECT host FROM hosts WHERE host <> ?1 ORDER BY updated_at DESC, host LIMIT -1 OFFSET ?2)`, host, max(c.maxHosts-1, 0))
	return err
}

// truncateURL cuts u to maxStoredURLLength bytes on a rune boundary.
func truncateURL(u string) string {
	if len(u) <= maxStoredURLLength {
		return u
	}
	return strings.ToValidUTF8(u[:maxStoredURLLength], "")
}
