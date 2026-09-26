package imgcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func newCache(t *testing.T, tune ...func(*Options)) (*Cache, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	o := Options{
		Dir: filepath.Join(t.TempDir(), "imgcache"), MaxBytes: 1 << 20, Now: clk.Now, NoBackgound: true,
		DiskSpace: func(string) (uint64, uint64, error) { return 500 << 30, 800 << 30, nil },
		// The eviction tests use small files and exact byte counts; the block
		// and URL charge has its own tests.
		ByteAccounting: true,
	}
	for _, f := range tune {
		f(&o)
	}
	c, err := Open(o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, clk
}

func reopen(t *testing.T, c *Cache, clk *fakeClock, tune ...func(*Options)) *Cache {
	t.Helper()
	require.NoError(t, c.Close())
	o := c.o
	o.Now = clk.Now
	for _, f := range tune {
		f(&o)
	}
	n, err := Open(o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func put(t *testing.T, c *Cache, name string, size int) string {
	t.Helper()
	url := "http://img.example/" + name
	key := KeyOrig(0, url)
	w, err := c.Begin(key, url, 0, int64(size))
	require.NoError(t, err)
	_, err = w.Write(bytes.Repeat([]byte{'x'}, size))
	require.NoError(t, err)
	require.NoError(t, w.Commit(Meta{ContentType: "image/png", ETag: `"e-` + name + `"`}))
	return key
}

func lookupOK(t *testing.T, c *Cache, key string) (Entry, bool) {
	t.Helper()
	e, ok, err := c.Lookup(context.Background(), key)
	require.NoError(t, err)
	return e, ok
}

func TestKeyCoversVariantFlagsAndURL(t *testing.T) {
	a := Key("orig", 0, "http://a/x")
	require.Len(t, a, 32)
	require.NotEqual(t, a, Key("orig", 1, "http://a/x"))
	require.NotEqual(t, a, Key("thumb", 0, "http://a/x"))
	require.NotEqual(t, a, Key("orig", 0, "http://a/y"))
	sum := sha256.Sum256([]byte("img-v1|orig|0|http://a/x"))
	require.Equal(t, hex.EncodeToString(sum[:])[:32], a)
	require.Equal(t, a, KeyOrig(0, "http://a/x"))
}

func TestCommitLookupServeAndLayout(t *testing.T) {
	c, _ := newCache(t)
	key := put(t, c, "a.png", 1000)
	e, ok := lookupOK(t, c, key)
	require.True(t, ok)
	require.True(t, e.OK)
	require.Equal(t, int64(1000), e.Size)
	require.Equal(t, "image/png", e.ContentType)
	require.Equal(t, `"e-a.png"`, e.ETag)
	sum := sha256.Sum256(bytes.Repeat([]byte{'x'}, 1000))
	require.Equal(t, hex.EncodeToString(sum[:]), e.SHA256)
	require.True(t, e.Fresh(c.now()))

	f, err := c.OpenFile(key)
	require.NoError(t, err)
	b, _ := io.ReadAll(f)
	_ = f.Close()
	require.Len(t, b, 1000)
	require.FileExists(t, filepath.Join(c.Dir(), "v1", key[:2], key))
	require.FileExists(t, filepath.Join(c.Dir(), "index.db"))
	st := c.Stats()
	require.Equal(t, int64(1000), st.UsedBytes)
	require.Equal(t, int64(1), st.Files)
	require.Equal(t, int64(1), st.Hits)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(c.Dir(), "v1", key[:2], key))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
		di, err := os.Stat(c.Dir())
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	}
	_, ok = lookupOK(t, c, KeyOrig(0, "http://nope/"))
	require.False(t, ok)
	require.Equal(t, int64(1), c.Stats().Misses)
}

func TestAbortAndOversizeNeverCache(t *testing.T) {
	c, _ := newCache(t, func(o *Options) { o.MaxObject = 100 })
	url := "http://img.example/p"
	key := KeyOrig(0, url)
	w, err := c.Begin(key, url, 0, -1)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 50))
	require.NoError(t, err)
	w.Abort()
	w.Abort() // idempotent
	_, ok := lookupOK(t, c, key)
	require.False(t, ok)

	w, err = c.Begin(key, url, 0, -1)
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 60))
	require.NoError(t, err)
	_, err = w.Write(make([]byte, 60))
	require.ErrorIs(t, err, ErrTooLarge)
	require.ErrorIs(t, w.Commit(Meta{}), ErrTooLarge)
	_, ok = lookupOK(t, c, key)
	require.False(t, ok)

	_, err = c.Begin(key, url, 0, 101)
	require.ErrorIs(t, err, ErrTooLarge, "a declared length over the limit is refused up front")

	w, err = c.Begin(key, url, 0, -1)
	require.NoError(t, err)
	require.Error(t, w.Commit(Meta{}), "an empty body is not cached")

	ents, _ := os.ReadDir(filepath.Join(c.Dir(), "tmp"))
	require.Empty(t, ents, "no temp file survives")
	require.Zero(t, c.Stats().UsedBytes)
}

func TestLRUEvictionOrderAndTarget(t *testing.T) {
	c, clk := newCache(t, func(o *Options) { o.MaxBytes = 1000 })
	var keys []string
	for _, n := range []string{"a", "b", "c"} {
		keys = append(keys, put(t, c, n, 300))
		clk.Advance(time.Minute)
	}
	// a is the oldest, then touched: b becomes the LRU entry.
	_, ok := lookupOK(t, c, keys[0])
	require.True(t, ok)
	clk.Advance(time.Minute)
	keys = append(keys, put(t, c, "d", 300)) // 1200 > 1000: evict to <= 900
	require.NoError(t, c.Flush())
	_, ok = lookupOK(t, c, keys[1])
	require.False(t, ok, "b was least recently used")
	for _, k := range []string{keys[0], keys[2], keys[3]} {
		_, ok = lookupOK(t, c, k)
		require.True(t, ok)
	}
	st := c.Stats()
	require.Equal(t, int64(900), st.UsedBytes)
	require.Equal(t, int64(1), st.Evictions)
	require.NoFileExists(t, filepath.Join(c.Dir(), "v1", keys[1][:2], keys[1]))

	// A bigger newcomer evicts down to 90% of the cap, oldest first.
	clk.Advance(time.Minute)
	keys = append(keys, put(t, c, "e", 600)) // 1500 -> <= 900
	st = c.Stats()
	require.LessOrEqual(t, st.UsedBytes, int64(900))
	_, ok = lookupOK(t, c, keys[4])
	require.True(t, ok, "the newest entry stays")
}

func TestSetCapShrinkAndDisable(t *testing.T) {
	c, clk := newCache(t, func(o *Options) { o.MaxBytes = 10000 })
	for i := 0; i < 10; i++ {
		put(t, c, fmt.Sprint("i", i), 800)
		clk.Advance(time.Second)
	}
	require.Equal(t, int64(8000), c.Stats().UsedBytes)
	c.SetCap(4000)
	require.Eventually(t, func() bool { return c.Stats().UsedBytes <= 3600 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(4000), c.Stats().MaxBytes)

	// Raising the cap keeps everything and lets it grow.
	c.SetCap(20000)
	put(t, c, "more", 800)

	c.SetCap(0)
	require.Eventually(t, func() bool { return c.Stats().Files == 0 }, 5*time.Second, 10*time.Millisecond)
	require.False(t, c.Enabled())
	_, err := c.Begin(KeyOrig(0, "http://x/"), "http://x/", 0, 1)
	require.ErrorIs(t, err, ErrDisabled)
	require.ErrorIs(t, c.PutNeg(KeyOrig(0, "http://x/"), "http://x/", 0, NegTransient, 500, "x"), ErrDisabled)
	require.Empty(t, walkFiles(t, filepath.Join(c.Dir(), "v1")))
}

func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestIdleExpiryAndAccessFlush(t *testing.T) {
	c, clk := newCache(t)
	old := put(t, c, "old", 100)
	kept := put(t, c, "kept", 100)
	clk.Advance(59 * 24 * time.Hour)
	_, ok := lookupOK(t, c, kept) // an access refreshes the idle clock
	require.True(t, ok)
	clk.Advance(2 * 24 * time.Hour) // old: 61 days idle, kept: 2
	r, err := c.Sweep(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, int64(1), r.IdleExpired)
	_, ok = lookupOK(t, c, old)
	require.False(t, ok)
	_, ok = lookupOK(t, c, kept)
	require.True(t, ok)
	require.Equal(t, int64(100), c.Stats().UsedBytes)
	require.NoFileExists(t, filepath.Join(c.Dir(), "v1", old[:2], old))
}

func TestNegativeCachingTTLsAndBackoff(t *testing.T) {
	c, clk := newCache(t)
	url := "http://img.example/dead"
	key := KeyOrig(0, url)
	require.NoError(t, c.PutNeg(key, url, 0, NegTransient, 503, "503 from source"))
	e, ok := lookupOK(t, c, key)
	require.True(t, ok)
	require.False(t, e.OK)
	require.Equal(t, 503, e.NegStatus)
	require.Equal(t, 10*time.Minute, e.FreshUntil.Sub(e.FetchedAt))
	require.True(t, e.Fresh(clk.Now()))

	clk.Advance(11 * time.Minute)
	_, ok = lookupOK(t, c, key)
	require.False(t, ok, "past the retry-after it is a miss")
	require.NoError(t, c.PutNeg(key, url, 0, NegTransient, 0, "timeout"))
	e, _ = lookupOK(t, c, key)
	require.Equal(t, 20*time.Minute, e.FreshUntil.Sub(e.FetchedAt))
	require.Equal(t, 1, e.NegCount)
	clk.Advance(21 * time.Minute)
	require.NoError(t, c.PutNeg(key, url, 0, NegTransient, 500, "x"))
	e, _ = lookupOK(t, c, key)
	require.Equal(t, 40*time.Minute, e.FreshUntil.Sub(e.FetchedAt))
	for i := 0; i < 10; i++ {
		clk.Advance(25 * time.Hour)
		require.NoError(t, c.PutNeg(key, url, 0, NegTransient, 500, "x"))
	}
	e, _ = lookupOK(t, c, key)
	require.Equal(t, 24*time.Hour, e.FreshUntil.Sub(e.FetchedAt), "capped at 24 h")

	nk := KeyOrig(0, "http://img.example/404")
	require.NoError(t, c.PutNeg(nk, "http://img.example/404", 0, NegPermanent, 404, "not found"))
	e, _ = lookupOK(t, c, nk)
	require.Equal(t, 24*time.Hour, e.FreshUntil.Sub(e.FetchedAt))
	require.Equal(t, int64(2), c.Stats().NegEntries)

	clk.Advance(48 * time.Hour)
	r, err := c.Sweep(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, int64(2), r.NegExpired)
	require.Zero(t, c.Stats().NegEntries)
}

func TestNegReplacesOkEntryAndOkReplacesNeg(t *testing.T) {
	c, _ := newCache(t)
	key := put(t, c, "flip", 500)
	url := "http://img.example/flip"
	require.NoError(t, c.PutNeg(key, url, 0, NegPermanent, 404, "gone"))
	require.Zero(t, c.Stats().UsedBytes)
	require.Zero(t, c.Stats().Files)
	require.Equal(t, int64(1), c.Stats().NegEntries)
	require.NoFileExists(t, filepath.Join(c.Dir(), "v1", key[:2], key))

	w, err := c.Begin(key, url, 0, -1)
	require.NoError(t, err)
	_, _ = w.Write([]byte("hello"))
	require.NoError(t, w.Commit(Meta{ContentType: "image/gif"}))
	st := c.Stats()
	require.Equal(t, int64(5), st.UsedBytes)
	require.Equal(t, int64(1), st.Files)
	require.Zero(t, st.NegEntries)
}

func TestRevalidatedAndReplace(t *testing.T) {
	c, clk := newCache(t)
	key := put(t, c, "r", 100)
	clk.Advance(10 * 24 * time.Hour)
	e, _ := lookupOK(t, c, key)
	require.False(t, e.Fresh(clk.Now()), "stale after the 7 day default")
	require.NoError(t, c.Revalidated(key, 3*24*time.Hour, `"new"`, ""))
	e, _ = lookupOK(t, c, key)
	require.True(t, e.Fresh(clk.Now()))
	require.Equal(t, `"new"`, e.ETag)

	// Replacing the body adjusts the byte count by the difference.
	url := "http://img.example/r"
	w, err := c.Begin(key, url, 0, -1)
	require.NoError(t, err)
	_, _ = w.Write(make([]byte, 40))
	require.NoError(t, w.Commit(Meta{ContentType: "image/png"}))
	require.Equal(t, int64(40), c.Stats().UsedBytes)
	require.Equal(t, int64(1), c.Stats().Files)
}

func TestFreshness(t *testing.T) {
	for cc, want := range map[string]time.Duration{
		"":                              7 * 24 * time.Hour,
		"public, max-age=60":            24 * time.Hour,
		"max-age=172800":                48 * time.Hour,
		"public, max-age=99999999":      30 * 24 * time.Hour,
		"no-store":                      24 * time.Hour,
		"max-age=abc":                   7 * 24 * time.Hour,
		"private, MAX-AGE=259200, x=y ": 72 * time.Hour,
	} {
		require.Equal(t, want, Freshness(cc), cc)
	}
}

func TestSingleFlight(t *testing.T) {
	c, _ := newCache(t)
	key := KeyOrig(0, "http://img.example/f")
	var leaders, followers atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	leaderIn := make(chan struct{})
	var once sync.Once
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			leader, done, release := c.Flight(key)
			if leader {
				leaders.Add(1)
				once.Do(func() { close(leaderIn) })
				time.Sleep(50 * time.Millisecond)
				release()
				release() // idempotent
				return
			}
			followers.Add(1)
			<-done
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), leaders.Load())
	require.Equal(t, int32(19), followers.Load())
	// After the leader is done the key is free again.
	leader, _, release := c.Flight(key)
	require.True(t, leader)
	release()
}

func TestStartupRepair(t *testing.T) {
	c, clk := newCache(t)
	good := put(t, c, "good", 100)
	gone := put(t, c, "gone", 100)
	short := put(t, c, "short", 100)
	root := c.Dir()
	require.NoError(t, c.Close())

	require.NoError(t, os.Remove(filepath.Join(root, "v1", gone[:2], gone)))
	require.NoError(t, os.WriteFile(filepath.Join(root, "v1", short[:2], short), []byte("abc"), 0o600)) // wrong size
	orphanKey := KeyOrig(0, "http://img.example/orphan")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "v1", orphanKey[:2]), 0o700))
	orphan := filepath.Join(root, "v1", orphanKey[:2], orphanKey)
	require.NoError(t, os.WriteFile(orphan, []byte("orphan"), 0o600))
	stray := filepath.Join(root, "v1", "zz-not-a-shard.txt")
	require.NoError(t, os.WriteFile(stray, []byte("x"), 0o600))
	tmp := filepath.Join(root, "tmp", "dl-half")
	require.NoError(t, os.WriteFile(tmp, []byte("half"), 0o600))

	n := reopen(t, c, clk)
	_, ok := lookupOK(t, n, good)
	require.True(t, ok)
	_, ok = lookupOK(t, n, gone)
	require.False(t, ok, "a row without its file is dropped")
	_, ok = lookupOK(t, n, short)
	require.False(t, ok, "a truncated file is dropped")
	require.NoFileExists(t, orphan)
	require.NoFileExists(t, stray)
	require.NoFileExists(t, tmp)
	require.NoFileExists(t, filepath.Join(root, "v1", short[:2], short))
	st := n.Stats()
	require.Equal(t, int64(100), st.UsedBytes)
	require.Equal(t, int64(1), st.Files)
}

func TestCorruptIndexIsRebuilt(t *testing.T) {
	c, clk := newCache(t)
	key := put(t, c, "x", 100)
	root := c.Dir()
	require.NoError(t, c.Close())
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.db"), []byte(strings.Repeat("not a database ", 500)), 0o600))
	_ = os.Remove(filepath.Join(root, "index.db-wal"))
	_ = os.Remove(filepath.Join(root, "index.db-shm"))

	n := reopen(t, c, clk)
	_, ok := lookupOK(t, n, key)
	require.False(t, ok)
	require.Zero(t, n.Stats().Files)
	require.Empty(t, walkFiles(t, filepath.Join(root, "v1")), "files without rows are removed")
	bad, _ := filepath.Glob(filepath.Join(root, "index.db.bad-*"))
	require.Len(t, bad, 1)
	// And it works.
	put(t, n, "again", 10)
	require.Equal(t, int64(10), n.Stats().UsedBytes)
}

func TestOldSchemaIndexIsRebuilt(t *testing.T) {
	c, clk := newCache(t)
	put(t, c, "x", 100)
	_, err := c.wr.Exec("UPDATE meta SET value = '0' WHERE key = 'schema_version'")
	require.NoError(t, err)
	n := reopen(t, c, clk)
	require.Zero(t, n.Stats().Files)
	bad, _ := filepath.Glob(filepath.Join(n.Dir(), "index.db.bad-*"))
	require.Len(t, bad, 1)
}

func TestOpenFileMissingDropsRow(t *testing.T) {
	c, _ := newCache(t)
	key := put(t, c, "m", 100)
	require.NoError(t, os.Remove(filepath.Join(c.Dir(), "v1", key[:2], key)))
	_, err := c.OpenFile(key)
	require.ErrorIs(t, err, ErrMissing)
	_, ok := lookupOK(t, c, key)
	require.False(t, ok)
	require.Zero(t, c.Stats().UsedBytes)
	_, err = c.OpenFile("../../etc/passwd")
	require.ErrorIs(t, err, ErrMissing, "keys are validated before they touch a path")
}

func TestDiskFloorRefusesWritesAndTrims(t *testing.T) {
	var free atomic.Uint64
	free.Store(500 << 30)
	c, clk := newCache(t, func(o *Options) {
		o.MaxBytes = 10000
		o.DiskSpace = func(string) (uint64, uint64, error) { return free.Load(), 800 << 30, nil }
	})
	for i := 0; i < 8; i++ {
		put(t, c, fmt.Sprint("d", i), 1000)
		clk.Advance(time.Second)
	}
	require.Equal(t, int64(8000), c.Stats().UsedBytes)

	free.Store(1 << 30) // under both 2 GiB and 5% of 800 GiB (40 GiB)
	url := "http://img.example/refused"
	_, err := c.Begin(KeyOrig(0, url), url, 0, 100)
	require.ErrorIs(t, err, ErrDiskLow)
	st := c.Stats()
	require.True(t, st.LowDisk)
	require.Equal(t, uint64(40<<30), st.DiskFloor)
	require.LessOrEqual(t, st.UsedBytes, int64(5000), "the cache trims itself to half its cap")
	ents, _ := os.ReadDir(filepath.Join(c.Dir(), "tmp"))
	require.Empty(t, ents)

	free.Store(100 << 30)
	w, err := c.Begin(KeyOrig(0, url), url, 0, 100)
	require.NoError(t, err)
	w.Abort()
	require.False(t, c.Stats().LowDisk)

	// The floor is at least the configured minimum on a small volume.
	c2, _ := newCache(t, func(o *Options) {
		o.MinFree = 1 << 30
		o.DiskSpace = func(string) (uint64, uint64, error) { return 1<<30 + 50, 10 << 30, nil }
	})
	_, err = c2.Begin(KeyOrig(0, url), url, 0, 100) // free - 100 < 1 GiB floor
	require.ErrorIs(t, err, ErrDiskLow)
	w, err = c2.Begin(KeyOrig(0, url), url, 0, 10)
	require.NoError(t, err)
	w.Abort()
}

func TestClear(t *testing.T) {
	c, _ := newCache(t)
	for i := 0; i < 5; i++ {
		put(t, c, fmt.Sprint("c", i), 100)
	}
	require.NoError(t, c.PutNeg(KeyOrig(0, "http://n/"), "http://n/", 0, NegPermanent, 404, "x"))
	require.NoError(t, c.SetHostHint("h.example", HostHint{UA: "browser", Referer: "none"}))
	n, err := c.Clear()
	require.NoError(t, err)
	require.Equal(t, int64(5), n)
	st := c.Stats()
	require.Zero(t, st.UsedBytes+st.Files+st.NegEntries)
	require.Empty(t, walkFiles(t, filepath.Join(c.Dir(), "v1")))
	_, ok := c.HostHint("h.example")
	require.False(t, ok)
	put(t, c, "after", 10) // still usable
	require.Equal(t, int64(10), c.Stats().UsedBytes)
}

func TestHostHints(t *testing.T) {
	c, _ := newCache(t)
	_, ok := c.HostHint("cdn.example")
	require.False(t, ok)
	require.NoError(t, c.SetHostHint("CDN.example", HostHint{UA: "browser", Referer: "self"}))
	h, ok := c.HostHint("cdn.example")
	require.True(t, ok)
	require.Equal(t, HostHint{UA: "browser", Referer: "self"}, h)
	require.NoError(t, c.SetHostHint("cdn.example", HostHint{UA: "kipple", Referer: "none"}))
	_, ok = c.HostHint("cdn.example")
	require.False(t, ok, "the default shape needs no hint")
}

func TestSweepVacuumAndHostExpiry(t *testing.T) {
	c, clk := newCache(t)
	put(t, c, "v", 100)
	require.NoError(t, c.SetHostHint("old.example", HostHint{UA: "browser", Referer: "none"}))
	clk.Advance(91 * 24 * time.Hour)
	r, err := c.Sweep(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, int64(1), r.Hosts)
	require.Equal(t, int64(1), r.IdleExpired)
	require.Equal(t, int64(2), r.Rows())
}

func TestConcurrentUseStaysConsistent(t *testing.T) {
	c, clk := newCache(t, func(o *Options) { o.MaxBytes = 20000 })
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				name := fmt.Sprint("k", (g*7+i)%40)
				url := "http://img.example/" + name
				key := KeyOrig(0, url)
				if _, ok, _ := c.Lookup(context.Background(), key); ok {
					continue
				}
				switch i % 5 {
				case 0:
					_ = c.PutNeg(key, url, 0, NegTransient, 500, "x")
				default:
					w, err := c.Begin(key, url, 0, 500)
					if err != nil {
						continue
					}
					_, _ = w.Write(make([]byte, 400+i))
					if i%7 == 0 {
						w.Abort()
					} else {
						_ = w.Commit(Meta{ContentType: "image/png"})
					}
				}
				clk.Advance(time.Second)
			}
		}()
	}
	wg.Wait()
	require.NoError(t, c.Flush())
	_, err := c.Sweep(context.Background(), false)
	require.NoError(t, err)

	// The counters, the index and the disk agree.
	var sum, n int64
	require.NoError(t, c.rd.QueryRow("SELECT COALESCE(SUM(size),0), count(*) FROM entries WHERE status='ok'").Scan(&sum, &n))
	st := c.Stats()
	require.Equal(t, sum, st.UsedBytes)
	require.Equal(t, n, st.Files)
	require.LessOrEqual(t, st.UsedBytes, int64(20000))
	files := walkFiles(t, filepath.Join(c.Dir(), "v1"))
	require.Len(t, files, int(n))
	var disk int64
	for _, f := range files {
		fi, err := os.Stat(f)
		require.NoError(t, err)
		disk += fi.Size()
	}
	require.Equal(t, sum, disk)
	ents, _ := os.ReadDir(filepath.Join(c.Dir(), "tmp"))
	require.Empty(t, ents)
}

func TestBackgroundLoopFlushesAndCloses(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		clk := &fakeClock{t: time.Now()}
		c, err := Open(Options{
			Dir: filepath.Join(t.TempDir(), "ic"), MaxBytes: 1 << 20, Now: clk.Now,
			FlushEvery: 10 * time.Millisecond, SweepEvery: 10 * time.Millisecond,
			DiskSpace: func(string) (uint64, uint64, error) { return 500 << 30, 800 << 30, nil },
		})
		require.NoError(t, err)
		key := put(t, c, "bg", 50)
		clk.Advance(time.Hour)
		_, ok := lookupOK(t, c, key)
		require.True(t, ok)
		require.Eventually(t, func() bool {
			var at int64
			_ = c.rd.QueryRow("SELECT last_access_at FROM entries WHERE key = ?", key).Scan(&at)
			return at == clk.Now().Unix()
		}, 5*time.Second, 5*time.Millisecond, "the loop flushed the access time")
		c.SetCap(10) // spawns a goroutine too
		require.NoError(t, c.Close())
		require.NoError(t, c.Close(), "Close twice is fine")
	}
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 }, 5*time.Second, 20*time.Millisecond,
		"goroutines after close: %d, before: %d", runtime.NumGoroutine(), before)
}

func TestLookupAfterCloseAndBadKeys(t *testing.T) {
	c, _ := newCache(t)
	_, ok := lookupOK(t, c, "nothex")
	require.False(t, ok)
	require.NoError(t, c.Close())
	_, _, err := c.Lookup(context.Background(), KeyOrig(0, "http://a/"))
	require.ErrorIs(t, err, ErrClosed)
	_, err = c.Begin(KeyOrig(0, "http://a/"), "http://a/", 0, 1)
	require.ErrorIs(t, err, ErrClosed)
	require.False(t, c.Enabled())
}

func TestVariantEntriesCoexistAndCountTowardTheCap(t *testing.T) {
	c, _ := newCache(t, func(o *Options) { o.MaxBytes = 1000 })
	const url = "http://img.example/big.jpg"
	okey, tkey := KeyOrig(0, url), KeyThumb(0, url)
	require.NotEqual(t, okey, tkey)
	require.Equal(t, Key("t800", 0, url), tkey)

	w, err := c.Begin(okey, url, 0, 600)
	require.NoError(t, err)
	_, _ = w.Write(bytes.Repeat([]byte{'o'}, 600))
	require.NoError(t, w.Commit(Meta{ContentType: "image/jpeg"}))
	w, err = c.Begin(tkey, url, 0, 100)
	require.NoError(t, err)
	_, _ = w.Write(bytes.Repeat([]byte{'t'}, 100))
	require.NoError(t, w.Commit(Meta{ContentType: "image/jpeg", Variant: VariantThumb, ETag: "src=abc"}))

	st := c.Stats()
	require.EqualValues(t, 700, st.UsedBytes)
	require.EqualValues(t, 2, st.Files)
	require.EqualValues(t, 1, st.Thumbnails)
	te, ok := lookupOK(t, c, tkey)
	require.True(t, ok)
	require.Equal(t, "src=abc", te.ETag)
	var variant string
	require.NoError(t, c.rd.QueryRow("SELECT variant FROM entries WHERE key = ?", tkey).Scan(&variant))
	require.Equal(t, "t800", variant)

	// Over the cap: the least recently used goes first, whichever variant it is, and the totals follow.
	c.touch(tkey, c.Now().Add(time.Second))
	w, err = c.Begin(KeyOrig(0, url+"2"), url+"2", 0, 500)
	require.NoError(t, err)
	_, _ = w.Write(bytes.Repeat([]byte{'p'}, 500))
	require.NoError(t, w.Commit(Meta{ContentType: "image/png"}))
	st = c.Stats()
	require.LessOrEqual(t, st.UsedBytes, int64(1000))
	var sum int64
	require.NoError(t, c.rd.QueryRow("SELECT COALESCE(SUM(size),0) FROM entries WHERE status = 'ok'").Scan(&sum))
	require.Equal(t, sum, st.UsedBytes)

	// A thumbnail refusal is a negative entry of its own and takes nothing from the original.
	require.NoError(t, c.PutNegVariant(KeyThumb(0, url+"3"), url+"3", 0, VariantThumb, NegPermanent, 0, "animated"))
	require.NoError(t, c.rd.QueryRow("SELECT variant FROM entries WHERE key = ?", KeyThumb(0, url+"3")).Scan(&variant))
	require.Equal(t, "t800", variant)
	require.EqualValues(t, 1, c.Stats().NegEntries)
}

func TestTruncateURLKeepsValidUTF8(t *testing.T) {
	long := strings.Repeat("a", maxStoredURLLength-1) + "é" + "tail" // a two-byte rune straddles the limit
	got := truncateURL(long)
	require.True(t, utf8.ValidString(got))
	require.LessOrEqual(t, len(got), maxStoredURLLength)
	require.Equal(t, strings.Repeat("a", maxStoredURLLength-1), got)
	require.Equal(t, "short", truncateURL("short"))
}
