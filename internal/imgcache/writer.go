package imgcache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Writer is one download in progress. Feed it the whole body with Write and end
// with Commit; anything else (Abort, an error, an oversize body) leaves nothing
// behind, so a partial body is never cached. Not safe for concurrent use.
type Writer struct {
	c    *Cache
	key  string
	url  string
	flag int
	f    *os.File
	h    hash.Hash
	n    int64
	err  error
	done bool
	// settled: the bytes written have been taken off the in-progress count
	// (at Commit or Abort).
	settled bool
}

// settle takes this download's bytes off the cache's in-progress count, once.
func (w *Writer) settle() {
	if !w.settled {
		w.settled = true
		w.c.tmpBytes.Add(-w.n)
	}
}

// Begin opens a temp file for key. It refuses (ErrDisabled, ErrDiskLow) when
// the cache is off or writing expected more bytes would leave the volume under
// its free-space floor; the caller then just streams without caching.
func (c *Cache) Begin(key, url string, flags int, expected int64) (*Writer, error) {
	if c.closed.Load() {
		return nil, ErrClosed
	}
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if !validKey(key) {
		return nil, fmt.Errorf("imgcache: bad key %q", key)
	}
	if expected > c.o.MaxObject {
		return nil, ErrTooLarge
	}
	if err := c.checkDisk(max(expected, 0)); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(c.tmpDir, "dl-*") // 0600
	if err != nil {
		return nil, err
	}
	return &Writer{c: c, key: key, url: url, flag: flags, f: f, h: sha256.New()}, nil
}

// checkDisk refuses when free space minus the incoming bytes is under the floor.
// A refusal also trims the cache to half its cap (at most once a minute) so the
// space the cache itself holds comes back.
func (c *Cache) checkDisk(incoming int64) error {
	free, total, err := c.o.DiskSpace(c.dir)
	if err != nil {
		return nil // cannot tell: do not block caching on a failed statfs
	}
	floor := c.floor(total)
	if free >= floor+uint64(incoming) { //nolint:gosec // incoming is >= 0
		c.lowDisk.Store(false)
		return nil
	}
	c.lowDisk.Store(true)
	now := c.now().Unix()
	if last := c.lastWarn.Load(); now-last >= int64(lowDiskWarnEvery/time.Second) && c.lastWarn.CompareAndSwap(last, now) {
		c.log.Warn("imgcache: free disk space is under the floor; not caching new images",
			"free_bytes", free, "floor_bytes", floor)
	}
	if last := c.lastLowEvict.Load(); now-last >= int64(lowDiskEvictEvery/time.Second) && c.lastLowEvict.CompareAndSwap(last, now) {
		if cp := c.maxBytes.Load(); cp > 0 && c.load() > cp*lowDiskTargetPct/100 {
			c.mu.Lock()
			c.noteEvict(c.evictLocked(c.ctx, cp*lowDiskTargetPct/100), "imgcache: low-disk eviction")
			c.mu.Unlock()
		}
	}
	return ErrDiskLow
}

// Write implements io.Writer. It fails with ErrTooLarge once the body passes the
// per-object limit; the caller should Abort and stop teeing.
func (w *Writer) Write(p []byte) (int, error) {
	if w.done {
		return 0, errors.New("imgcache: write after commit or abort")
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.n+int64(len(p)) > w.c.o.MaxObject {
		w.err = ErrTooLarge
		return 0, w.err
	}
	n, err := w.f.Write(p)
	w.h.Write(p[:n])
	w.n += int64(n)
	w.c.tmpBytes.Add(int64(n)) // in-progress bytes count against the cap until Commit or Abort
	if err != nil {
		w.err = err
	}
	return n, err
}

// OpenReader opens a second, read-only handle on the download so another
// goroutine can serve the bytes already written while the body is still
// arriving (the proxy decouples a slow client from its source this way). Call
// it before Commit or Abort. The handle stays readable after either: Commit
// renames the file into place and Abort deletes it, and an open handle keeps
// the data on both Unix and Windows (it is opened with FILE_SHARE_DELETE
// there). Only read below what Write has returned; the caller closes it.
func (w *Writer) OpenReader() (*os.File, error) {
	if w.done {
		return nil, errors.New("imgcache: reader after commit or abort")
	}
	return openShared(w.f.Name())
}

// Abort discards the download. Safe to call after Commit (no-op).
func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.settle()
	name := w.f.Name()
	_ = w.f.Close()
	_ = os.Remove(name)
}

// Commit publishes the download: the temp file is renamed into place and the
// entry inserted. On any error the temp file is removed and nothing is cached.
func (w *Writer) Commit(m Meta) error {
	if w.done {
		return errors.New("imgcache: commit after commit or abort")
	}
	if w.err != nil || w.n == 0 {
		w.Abort()
		if w.err != nil {
			return w.err
		}
		return errors.New("imgcache: empty body")
	}
	name := w.f.Name()
	w.settle() // from here the bytes are either an entry (charged) or gone
	if err := w.f.Close(); err != nil {
		w.done = true
		_ = os.Remove(name)
		return err
	}
	w.done = true
	c := w.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.Enabled() {
		_ = os.Remove(name)
		return ErrDisabled
	}
	if m.FreshFor <= 0 {
		m.FreshFor = freshDefault
	}
	variant := m.Variant
	if variant == "" {
		variant = variantOrig
	}
	final := c.path(w.key)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		_ = os.Remove(name)
		return err
	}
	var oldStatus, oldReason string
	var oldSize int64
	var oldURLLen int
	err := c.wr.QueryRow("SELECT status, size, neg_reason, length(CAST(url AS BLOB)) FROM entries WHERE key = ?", w.key).Scan(&oldStatus, &oldSize, &oldReason, &oldURLLen)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = os.Remove(name)
		return err
	}
	// Rename over the old file. On Windows this fails while a reader holds the
	// old file open; then the new copy is simply not cached.
	if err := os.Rename(name, final); err != nil {
		_ = os.Remove(name)
		return err
	}
	now := c.now()
	sum := hex.EncodeToString(w.h.Sum(nil))
	if len(w.url) > maxStoredURLLength {
		w.url = truncateURL(w.url)
	}
	if _, err := c.wr.Exec(`INSERT OR REPLACE INTO entries (key, url, flags, variant, status, content_type, size, sha256, etag, last_modified,
		fetched_at, fresh_until, last_access_at) VALUES (?, ?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.key, w.url, w.flag, variant, m.ContentType, w.n, sum, m.ETag, m.LastModified,
		now.Unix(), now.Add(m.FreshFor).Unix(), now.Unix()); err != nil {
		_ = os.Remove(final)
		return err
	}
	c.forgetAccess(w.key)
	switch {
	case exists && oldStatus == statusOK:
		c.used.Add(w.n - oldSize)
		c.charged.Add(-c.cost(oldSize, oldURLLen))
	case exists:
		c.negN.Add(-1)
		if oldReason == InProgress {
			c.markN.Add(-1)
		}
		c.used.Add(w.n)
		c.files.Add(1)
	default:
		c.used.Add(w.n)
		c.files.Add(1)
	}
	c.charged.Add(c.cost(w.n, len(w.url)))
	if cp := c.maxBytes.Load(); cp > 0 && c.load() > cp {
		c.noteEvict(c.evictLocked(c.ctx, cp*evictTargetPct/100), "imgcache: eviction")
	}
	return nil
}

// repair reconciles the index with the files on disk (startup, before any use):
// temp files are deleted, rows whose file is missing or the wrong size are
// deleted, files without a row are deleted, and the counters are loaded.
func (c *Cache) repair() error {
	if ents, err := os.ReadDir(c.tmpDir); err == nil {
		for _, e := range ents {
			_ = os.RemoveAll(filepath.Join(c.tmpDir, e.Name()))
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.wr.Query("SELECT key, size FROM entries WHERE status = 'ok'")
	if err != nil {
		return fmt.Errorf("imgcache: repair: %w", err)
	}
	known := map[string]struct{}{}
	var bad []string
	for rows.Next() {
		var k string
		var size int64
		if err := rows.Scan(&k, &size); err != nil {
			_ = rows.Close()
			return fmt.Errorf("imgcache: repair: %w", err)
		}
		st, serr := os.Stat(c.path(k))
		if !validKey(k) || serr != nil || !st.Mode().IsRegular() || st.Size() != size {
			bad = append(bad, k)
			continue
		}
		known[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("imgcache: repair: %w", err)
	}
	_ = rows.Close()
	for _, k := range bad {
		if _, err := c.wr.Exec("DELETE FROM entries WHERE key = ?", k); err != nil {
			return fmt.Errorf("imgcache: repair: %w", err)
		}
		if validKey(k) {
			_ = os.Remove(c.path(k))
		}
	}
	var orphans int
	_ = filepath.WalkDir(c.filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if _, ok := known[d.Name()]; !ok || filepath.Base(filepath.Dir(p)) != d.Name()[:min(2, len(d.Name()))] {
			orphans++
			_ = os.Remove(p)
		}
		return nil
	})
	if ents, err := os.ReadDir(c.filesDir); err == nil {
		for _, e := range ents { // empty shard directories
			if e.IsDir() {
				_ = os.Remove(filepath.Join(c.filesDir, e.Name())) // fails (harmlessly) when not empty
			}
		}
	}
	if err := c.recountLocked(); err != nil {
		return fmt.Errorf("imgcache: repair: %w", err)
	}
	if len(bad) > 0 || orphans > 0 {
		c.log.Warn("imgcache: repaired at startup", "rows_without_file", len(bad), "files_without_row", orphans)
	}
	return nil
}
