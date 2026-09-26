// Package store is Kipple's SQLite persistence layer (design.md section 2).
//
// Driver: modernc.org/sqlite v1.59.0 (SQLite 3.53.4) with modernc.org/libc v1.75.7,
// the exact libc version in the driver's own go.mod. Both are pinned by go.mod; do not
// bump one without the other, and re-run the store tests on every bump.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"

	"github.com/WPTK/kipple/internal/clock"
)

// Pool sizes and page caches (KiB) per design.md 2.1.
const (
	readerConns = 4

	writerCacheKiB   = 16000
	readerCacheKiB   = 4000
	snapshotCacheKiB = 2000

	writeTimeout = 10 * time.Second
)

// DB owns the writer pool, the reader pool and the commit gate. The snapshot pool is
// opened on demand (see openSnapshot) and closed by its user.
type DB struct {
	path      string
	backupDir string
	log       *slog.Logger

	writer *sql.DB
	reader *sql.DB
	gate   chan struct{}
	// snap is the one-slot exclusion shared by the nightly snapshot and the
	// backup export (both run VACUUM INTO on the snapshot pool).
	snap chan struct{}

	clock clock.Clock
	alloc *IDAlloc

	// migrations overrides the embedded set (tests only).
	migrations []migration

	noMigrate    bool
	noCheckpoint bool

	holder atomic.Pointer[holder]

	// ftAll caches fetch.fulltext_all; SetSettings invalidates it.
	ftAll boolCache
	// filterGen counts filter writes; fcache holds the rule set compiled at one generation.
	filterGen atomic.Uint64
	fcache    filterCache

	// ftPend is the set of items queued for ingest extraction (the Reader hold).
	ftPend ftPending

	closeOnce sync.Once
	closeErr  error
}

// Options configures Open.
type Options struct {
	// Path is the database file (e.g. /data/kipple.db).
	Path string
	// BackupDir receives pre-migration snapshots. Defaults to <dir of Path>/backup.
	BackupDir string
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Clock defaults to the wall clock; tests inject a fake.
	Clock clock.Clock
	// NoMigrate opens the database as it is: an older schema (or a database that
	// was never initialised) is refused instead of migrated, and no
	// pre-migration snapshot or optimize runs. For the `import` and
	// `api-password` subcommands, which must never change the schema under a
	// running server.
	NoMigrate bool
	// NoCheckpoint makes Close skip the WAL_TRUNCATE checkpoint. A short-lived
	// CLI opened next to a running server must not stall or truncate the
	// server's WAL; SQLite's automatic checkpointing takes care of it.
	NoCheckpoint bool
}

var ofdOnce sync.Once

// buildDSN returns the DSN for a pool. kind is "writer", "reader" or "snapshot".
func buildDSN(path, kind string) string {
	cache := writerCacheKiB
	switch kind {
	case "reader":
		cache = readerCacheKiB
	case "snapshot":
		cache = snapshotCacheKiB
	}
	q := url.Values{}
	if kind == "writer" {
		q.Set("_txlock", "immediate")
	}
	pragmas := []string{
		"busy_timeout(5000)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(ON)",
		"temp_store(MEMORY)",
		fmt.Sprintf("cache_size(-%d)", cache),
		"journal_size_limit(67108864)",
		"mmap_size(0)",
		"analysis_limit(400)",
	}
	if kind == "reader" {
		pragmas = append(pragmas, "query_only(1)")
	}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	// The query is built with url.Values (it percent-encodes the pragma parentheses, which the
	// driver decodes); the path is escaped separately so '?', '#' and '%' in it stay literal.
	return "file:" + escapePath(path) + "?" + q.Encode()
}

// escapePath percent-escapes a filesystem path for use in a SQLite file: URI.
func escapePath(path string) string {
	u := url.URL{Path: filepath.ToSlash(path)}
	return u.EscapedPath()
}

// checkForeign refuses a database file that is not a Kipple database, using a throwaway
// read-only connection with no pragmas, so a foreign file is never switched to WAL or
// otherwise written before the refusal. A missing or empty file is fine (fresh).
func checkForeign(ctx context.Context, path string) error {
	if st, err := os.Stat(path); err != nil || st.Size() == 0 {
		return nil
	}
	ro, err := sql.Open("sqlite", "file:"+escapePath(path)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("store: open for inspection: %w", err)
	}
	defer ro.Close()
	ro.SetMaxOpenConns(1)
	var appID, objects int
	if err := ro.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return fmt.Errorf("store: inspect database: %w", err)
	}
	if err := ro.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&objects); err != nil {
		return fmt.Errorf("store: inspect database: %w", err)
	}
	if !(appID == 0 && objects == 0) && appID != ApplicationID {
		return errors.New("store: not a Kipple database")
	}
	return nil
}

// Open opens (creating if needed) the database, migrates it to the latest schema and
// returns the pools. It refuses a file that is not a Kipple database or is newer than
// this binary.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, errors.New("store: empty database path")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	backup := opts.BackupDir
	if backup == "" {
		backup = filepath.Join(filepath.Dir(opts.Path), "backup")
	}

	if opts.NoMigrate {
		// Refuse before anything is created: opening a missing file would leave a
		// stray, empty database behind.
		if st, err := os.Stat(opts.Path); err != nil || st.Size() == 0 {
			return nil, errors.New("store: database does not exist yet; start `kipple serve` once first")
		}
	}

	// Design 2.1: before the first open. Only effective on Linux; elsewhere it errors,
	// which is expected and harmless.
	ofdOnce.Do(func() {
		if _, err := sqlite.OFDLocking(true); err != nil {
			log.Debug("store: OFD locking unavailable", "err", err)
		}
	})

	clk := opts.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	d := &DB{path: opts.Path, backupDir: backup, log: log, gate: make(chan struct{}, 1), snap: make(chan struct{}, 1), clock: clk,
		noMigrate: opts.NoMigrate, noCheckpoint: opts.NoCheckpoint}

	if err := checkForeign(ctx, opts.Path); err != nil {
		return nil, err
	}

	var err error
	d.writer, err = sql.Open("sqlite", buildDSN(opts.Path, "writer"))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	d.writer.SetMaxOpenConns(1)
	d.writer.SetMaxIdleConns(1)
	d.writer.SetConnMaxLifetime(0)

	if err := d.initWriter(ctx); err != nil {
		d.writer.Close()
		return nil, err
	}
	if err := selfCheck(ctx, d.writer); err != nil {
		d.writer.Close()
		return nil, err
	}
	if err := d.migrate(ctx); err != nil {
		d.writer.Close()
		return nil, err
	}

	d.reader, err = sql.Open("sqlite", buildDSN(opts.Path, "reader"))
	if err != nil {
		d.writer.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	d.reader.SetMaxOpenConns(readerConns)
	d.reader.SetMaxIdleConns(readerConns)
	d.reader.SetConnMaxIdleTime(0)
	d.reader.SetConnMaxLifetime(0)
	if err := d.reader.PingContext(ctx); err != nil {
		d.writer.Close()
		d.reader.Close()
		return nil, fmt.Errorf("store: ping reader: %w", err)
	}
	if d.alloc, err = seedIDAlloc(ctx, d.reader, clk, log); err != nil {
		d.writer.Close()
		d.reader.Close()
		return nil, err
	}
	return d, nil
}

// initWriter connects, runs PRAGMA optimize=0x10002 once and verifies foreign keys.
func (d *DB) initWriter(ctx context.Context) error {
	if _, err := d.writer.ExecContext(ctx, "PRAGMA optimize=0x10002"); err != nil {
		return fmt.Errorf("store: initial optimize: %w", err)
	}
	return d.checkForeignKeys(ctx)
}

func (d *DB) checkForeignKeys(ctx context.Context) error {
	var fk int
	if err := d.writer.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("store: read foreign_keys: %w", err)
	}
	if fk != 1 {
		return errors.New("store: foreign_keys is not enabled on the writer")
	}
	return nil
}

// openSnapshot opens the snapshot pool (one connection, not query_only). The caller closes it.
func (d *DB) openSnapshot() (*sql.DB, error) {
	s, err := sql.Open("sqlite", buildDSN(d.path, "snapshot"))
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(1)
	return s, nil
}

// Reader returns the read-only pool. Never hold a read transaction across network writes.
func (d *DB) Reader() *sql.DB { return d.reader }

// Clock returns the store's time source.
func (d *DB) Clock() clock.Clock { return d.clock }

// IDs returns the item id allocator.
func (d *DB) IDs() *IDAlloc { return d.alloc }

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// AcquireGate takes the one-slot commit gate. Fetch workers call it before WithWrite so a
// burst of feed commits cannot starve an API write; API writers skip it. Call the returned
// function exactly once to release.
func (d *DB) AcquireGate(ctx context.Context) (release func(), err error) {
	select {
	case d.gate <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-d.gate }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close closes the readers, checkpoints the WAL (TRUNCATE, unless opened with
// NoCheckpoint) and closes the writer. It is idempotent.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var errs []error
		if d.reader != nil {
			if err := d.reader.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if !d.noCheckpoint {
			var busy, logFrames, checkpointed int
			if err := d.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
				errs = append(errs, fmt.Errorf("checkpoint: %w", err))
			} else if busy != 0 {
				errs = append(errs, fmt.Errorf("checkpoint: busy (log=%d checkpointed=%d)", logFrames, checkpointed))
			}
		}
		if err := d.writer.Close(); err != nil {
			errs = append(errs, err)
		}
		d.closeErr = errors.Join(errs...)
	})
	return d.closeErr
}

func ensureDir(dir string) error { return os.MkdirAll(dir, 0o755) }
