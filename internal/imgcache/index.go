package imgcache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the driver; the main database uses it too
)

const schemaVersion = "1"

const schema = `
CREATE TABLE IF NOT EXISTS entries (
  key TEXT PRIMARY KEY,
  url TEXT NOT NULL,
  flags INTEGER NOT NULL,
  variant TEXT NOT NULL DEFAULT 'orig',
  status TEXT NOT NULL CHECK (status IN ('ok','neg')),
  content_type TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  sha256 TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  last_modified TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NOT NULL,
  fresh_until INTEGER NOT NULL,
  last_access_at INTEGER NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0,
  neg_status INTEGER NOT NULL DEFAULT 0,
  neg_reason TEXT NOT NULL DEFAULT '',
  neg_count INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_lru ON entries(last_access_at) WHERE status = 'ok';
CREATE INDEX IF NOT EXISTS idx_neg ON entries(fresh_until) WHERE status = 'neg';
CREATE TABLE IF NOT EXISTS hosts (
  host TEXT PRIMARY KEY,
  referer TEXT NOT NULL CHECK (referer IN ('none','self')),
  ua TEXT NOT NULL CHECK (ua IN ('kipple','browser')),
  updated_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT, WITHOUT ROWID;
`

func dsn(path string) string {
	q := url.Values{}
	for _, p := range []string{
		"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(NORMAL)",
		"cache_size(-2000)", "mmap_size(0)", "temp_store(MEMORY)", "journal_size_limit(8388608)",
	} {
		q.Add("_pragma", p)
	}
	u := url.URL{Path: filepath.ToSlash(path)}
	return "file:" + u.EscapedPath() + "?" + q.Encode()
}

// openIndex opens (creating) index.db with its own pools: one writer connection
// and a small reader pool. A file that is corrupt or from another schema is
// renamed aside and a fresh one created: it is a cache, so that is safe.
func openIndex(dir string, now func() time.Time) (wr, rd *sql.DB, rebuilt bool, err error) {
	path := filepath.Join(dir, "index.db")
	wr, rd, err = tryOpenIndex(path)
	if err == nil {
		return wr, rd, false, nil
	}
	if _, serr := os.Stat(path); serr != nil {
		return nil, nil, false, err // not a bad file: a real I/O problem
	}
	if qerr := quarantine(path, now()); qerr != nil {
		return nil, nil, false, fmt.Errorf("imgcache: index unusable (%v) and could not be moved aside: %w", err, qerr)
	}
	wr, rd, err = tryOpenIndex(path)
	return wr, rd, true, err
}

func tryOpenIndex(path string) (wr, rd *sql.DB, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	wr, err = sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, nil, err
	}
	wr.SetMaxOpenConns(1)
	wr.SetMaxIdleConns(1)
	wr.SetConnMaxLifetime(0)
	fail := func(e error) (*sql.DB, *sql.DB, error) {
		_ = wr.Close()
		if rd != nil {
			_ = rd.Close()
		}
		return nil, nil, e
	}
	var qc string
	if err := wr.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&qc); err != nil {
		return fail(err)
	}
	if qc != "ok" {
		return fail(fmt.Errorf("imgcache: index quick_check: %s", qc))
	}
	// An existing file with tables but another schema version is not ours.
	var tables int
	if err := wr.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil {
		return fail(err)
	}
	if tables > 0 {
		var v string
		err := wr.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='schema_version'").Scan(&v)
		if err != nil || v != schemaVersion {
			return fail(fmt.Errorf("imgcache: index schema version %q, want %q (%v)", v, schemaVersion, err))
		}
	}
	if _, err := wr.ExecContext(ctx, schema); err != nil {
		return fail(err)
	}
	if _, err := wr.ExecContext(ctx, "INSERT OR IGNORE INTO meta(key, value) VALUES ('schema_version', ?)", schemaVersion); err != nil {
		return fail(err)
	}
	rd, err = sql.Open("sqlite", dsn(path))
	if err != nil {
		return fail(err)
	}
	rd.SetMaxOpenConns(4)
	rd.SetMaxIdleConns(4)
	rd.SetConnMaxLifetime(0)
	if err := rd.PingContext(ctx); err != nil {
		return fail(err)
	}
	return wr, rd, nil
}

// quarantine renames a bad index (and its WAL/SHM) aside, keeping only the
// newest such copy.
func quarantine(path string, now time.Time) error {
	dir := filepath.Dir(path)
	old, _ := filepath.Glob(filepath.Join(dir, "index.db.bad-*"))
	for _, o := range old {
		_ = os.Remove(o)
	}
	dst := fmt.Sprintf("%s.bad-%d", path, now.Unix())
	if err := os.Rename(path, dst); err != nil {
		if rmErr := os.Remove(path); rmErr != nil {
			return errors.Join(err, rmErr)
		}
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return nil
}
