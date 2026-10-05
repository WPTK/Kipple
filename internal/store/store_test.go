package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTest(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := Open(context.Background(), Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func scalar[T any](t testing.TB, q Querier, query string, args ...any) T {
	t.Helper()
	var v T
	require.NoError(t, q.QueryRowContext(context.Background(), query, args...).Scan(&v))
	return v
}

func TestMigrateFromEmptyAndReopen(t *testing.T) {
	ctx := context.Background()
	db, path := openTest(t)

	latest := LatestVersion()
	require.GreaterOrEqual(t, latest, 1)
	v, err := db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, v)
	require.Equal(t, ApplicationID, scalar[int](t, db.Reader(), "PRAGMA application_id"))

	require.Equal(t, "ok", scalar[string](t, db.Reader(), "PRAGMA integrity_check"))
	rows, err := db.Reader().QueryContext(ctx, "PRAGMA foreign_key_check")
	require.NoError(t, err)
	require.False(t, rows.Next(), "foreign_key_check must return no rows")
	require.NoError(t, rows.Close())

	// Seed rows survive a reopen and the migration is not re-applied.
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM folders WHERE is_default = 1"))
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('tz', '\"UTC\"')")
		return err
	}))
	require.NoError(t, db.Close())

	db2, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	defer db2.Close()
	v, err = db2.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, v)
	require.Equal(t, "\"UTC\"", scalar[string](t, db2.Reader(), "SELECT value FROM settings WHERE key='tz'"))
	require.Equal(t, "ok", scalar[string](t, db2.Reader(), "PRAGMA integrity_check"))
}

func TestPragmasAndPools(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)

	err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		require.Equal(t, 1, scalar[int](t, tx, "PRAGMA foreign_keys"))
		require.Equal(t, "wal", scalar[string](t, tx, "PRAGMA journal_mode"))
		require.Equal(t, 1, scalar[int](t, tx, "PRAGMA synchronous"), "NORMAL = 1")
		require.Equal(t, 5000, scalar[int](t, tx, "PRAGMA busy_timeout"))
		require.Equal(t, -16000, scalar[int](t, tx, "PRAGMA cache_size"))
		return nil
	})
	require.NoError(t, err)

	r := db.Reader()
	require.Equal(t, 4, r.Stats().MaxOpenConnections)
	require.Equal(t, 1, scalar[int](t, r, "PRAGMA query_only"))
	require.Equal(t, 1, scalar[int](t, r, "PRAGMA foreign_keys"))
	_, err = r.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('x', '1')")
	require.Error(t, err, "reader pool is query_only")

	// WithWrite rolls back on error.
	boom := fmt.Errorf("boom")
	err = db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO settings(key, value) VALUES ('y', '1')")
		require.NoError(t, e)
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 0, scalar[int](t, r, "SELECT count(*) FROM settings WHERE key='y'"))
}

func TestOpenRefusals(t *testing.T) {
	ctx := context.Background()

	// Not a Kipple database: application_id 0 with a non-empty schema.
	foreign := filepath.Join(t.TempDir(), "other.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(foreign))
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE t (a)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, err = Open(ctx, Options{Path: foreign})
	require.ErrorContains(t, err, "not a Kipple database")

	// Downgrade guard: user_version above the newest embedded migration.
	db, path := openTest(t)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", LatestVersion()+1))
		return e
	}))
	require.NoError(t, db.Close())
	_, err = Open(ctx, Options{Path: path})
	require.ErrorContains(t, err, "newer than this binary")
}

// TestConcurrentReadWrite hammers the writer and the reader pool at once. It is written to
// be meaningful under `go test -race` (CI); it also asserts readers never see a torn
// state and that nothing deadlocks or hits SQLITE_BUSY.
func TestConcurrentReadWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, _ := openTest(t)

	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS t_conc (a INTEGER NOT NULL, b INTEGER NOT NULL)`)
		return err
	}))

	const writers, perWriter, readers = 4, 50, 8
	var wg sync.WaitGroup
	errs := make(chan error, writers+readers)
	done := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// Half go through the commit gate (fetch-style), half skip it (API-style).
				release := func() {}
				if i%2 == 0 {
					var err error
					if release, err = db.AcquireGate(ctx); err != nil {
						errs <- err
						return
					}
				}
				err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
					// Two rows with equal a and b: a reader must always see an even count
					// whose sum(a) == sum(b) (atomic commit).
					_, err := tx.ExecContext(ctx, "INSERT INTO t_conc VALUES (?, ?), (?, ?)", w, i, w, i)
					return err
				})
				release()
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	var rwg sync.WaitGroup
	for r := 0; r < readers; r++ {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				var n, sa, sb sql.NullInt64
				err := db.Reader().QueryRowContext(ctx, "SELECT count(*), sum(a), sum(b) FROM t_conc").Scan(&n, &sa, &sb)
				if err != nil {
					errs <- err
					return
				}
				if n.Int64%2 != 0 {
					errs <- fmt.Errorf("reader saw odd row count %d (torn transaction)", n.Int64)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(done)
	rwg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, writers*perWriter*2, scalar[int](t, db.Reader(), "SELECT count(*) FROM t_conc"))
}

func TestCommitGateExcludes(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	rel, err := db.AcquireGate(ctx)
	require.NoError(t, err)

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = db.AcquireGate(short)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	rel()
	rel() // idempotent
	rel2, err := db.AcquireGate(ctx)
	require.NoError(t, err)
	rel2()
}

func TestFTSTriggers(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)

	search := func(q string) []int64 {
		rows, err := db.Reader().QueryContext(ctx, "SELECT rowid FROM items_fts WHERE items_fts MATCH ? ORDER BY rowid", q)
		require.NoError(t, err)
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		require.NoError(t, rows.Err())
		return ids
	}
	exec := func(q string, args ...any) {
		require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}))
	}

	exec(`INSERT INTO feeds (url, url_key, host) VALUES ('https://e.example/f', 'e.example/f', 'e.example')`)
	feedID := scalar[int64](t, db.Reader(), "SELECT id FROM feeds WHERE url = 'https://e.example/f'")

	// Insert: items first, then item_content (the FTS insert fires on the content row).
	const id = 1_700_000_000_000_000
	exec(`INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title, author)
	      VALUES (?, ?, 1700000000, 1700000000, 'g:1', 'h', 'h', 'Zeppelin voyage', 'Ann Author')`, id, feedID)
	require.Empty(t, search("zeppelin"), "not searchable until item_content exists")
	exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?, '<p>x</p>', 'quokka habitat notes')`, id)
	require.Equal(t, []int64{id}, search("zeppelin"))
	require.Equal(t, []int64{id}, search("quokka"))
	require.Equal(t, []int64{id}, search("author"))
	require.Equal(t, []int64{id}, search("Zeppelín"), "remove_diacritics")

	// Update title: old term gone, new term found.
	exec(`UPDATE items SET title = 'Dirigible voyage' WHERE id = ?`, id)
	require.Empty(t, search("zeppelin"))
	require.Equal(t, []int64{id}, search("dirigible"))
	require.Equal(t, []int64{id}, search("quokka"))

	// Update content_text: old term gone, new term found.
	exec(`UPDATE item_content SET content_text = 'wombat burrow notes' WHERE item_id = ?`, id)
	require.Empty(t, search("quokka"))
	require.Equal(t, []int64{id}, search("wombat"))

	// Markup-only update (content_html) must not touch the index.
	exec(`UPDATE item_content SET content_html = '<p>changed</p>' WHERE item_id = ?`, id)
	require.Equal(t, []int64{id}, search("wombat"))

	ftsIntegrity(t, db)
}

func TestFTSDeleteAndCascade(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	exec := func(q string, args ...any) {
		require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		}))
	}
	count := func(q string) int {
		return scalar[int](t, db.Reader(), "SELECT count(*) FROM items_fts WHERE items_fts MATCH ?", q)
	}
	exec(`INSERT INTO feeds (url, url_key, host) VALUES ('https://e.example/f', 'e.example/f', 'e.example')`)
	feedID := scalar[int64](t, db.Reader(), "SELECT id FROM feeds")
	for i, word := range []string{"alpha", "bravo"} {
		id := int64(1_700_000_000_000_000 + i)
		exec(`INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
		      VALUES (?, ?, 1, 1, ?, 'h', 'h', ?)`, id, feedID, fmt.Sprintf("g:%d", i), word)
		exec(`INSERT INTO item_content (item_id, content_text) VALUES (?, ?)`, id, word+" body")
	}
	require.Equal(t, 1, count("alpha"))

	exec(`DELETE FROM items WHERE title = 'alpha'`)
	require.Equal(t, 0, count("alpha"))
	require.Equal(t, 1, count("bravo"))

	// Feed delete cascades items, content and FTS rows.
	exec(`DELETE FROM feeds WHERE id = ?`, feedID)
	require.Equal(t, 0, count("bravo"))
	require.Equal(t, 0, scalar[int](t, db.Reader(), "SELECT count(*) FROM item_content"))
	ftsIntegrity(t, db)
}

// ftsIntegrity runs the FTS5 integrity-check command (it errors on any inconsistency).
func ftsIntegrity(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO items_fts(items_fts, rank) VALUES('integrity-check', 1)")
		return err
	}))
}

func TestPreMigrationSnapshotKeepsThree(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, db.preMigrationSnapshot(ctx, 1, 2+i, nil)) // distinct names within the same second
	}
	files, err := filepath.Glob(filepath.Join(db.backupDir, "pre-migration-*.db"))
	require.NoError(t, err)
	require.Len(t, files, 3)
}

func TestForeignFileUntouchedOnRefusal(t *testing.T) {
	ctx := context.Background()
	foreign := filepath.Join(t.TempDir(), "other.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(foreign)+"?_pragma=journal_mode(DELETE)")
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE t (a)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	before, err := os.ReadFile(foreign)
	require.NoError(t, err)
	_, err = Open(ctx, Options{Path: foreign})
	require.ErrorContains(t, err, "not a Kipple database")
	after, err := os.ReadFile(foreign)
	require.NoError(t, err)
	require.Equal(t, before, after, "foreign file bytes must be untouched")
	require.Equal(t, byte(1), after[18], "file-format write version still legacy (not WAL)")
	_, err = os.Stat(foreign + "-wal")
	require.True(t, os.IsNotExist(err), "no WAL file created")
}

func TestWithWriteContextBoundsStatements(t *testing.T) {
	db, _ := openTest(t)
	err := db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		dl, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(writeTimeout), dl, 2*time.Second)
		return nil
	})
	require.NoError(t, err)
}

func TestWithWriteTimeoutLogsHolder(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(lockedWriter{&buf, &mu}, nil))
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := Open(context.Background(), Options{Path: path, Logger: logger})
	require.NoError(t, err)
	defer db.Close()

	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = db.WithWrite(short, func(ctx context.Context, tx *sql.Tx) error { return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-done)

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	require.Contains(t, out, "timed out acquiring the writer")
	require.Contains(t, out, "TestWithWriteTimeoutLogsHolder")
	require.Contains(t, out, "store_test.go")
}

type lockedWriter struct {
	b  *bytes.Buffer
	mu *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func TestSnapshotRetentionKeepsNewest(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	require.NoError(t, ensureDir(db.backupDir))
	base := time.Now().Add(-10 * time.Hour)
	// Names deliberately do not sort like mtimes: retention must go by mtime.
	names := []string{"e", "a", "d", "b", "c"}
	for i, n := range names {
		p := filepath.Join(db.backupDir, "pre-migration-old-"+n+".db")
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(p, base.Add(time.Duration(i)*time.Hour), base.Add(time.Duration(i)*time.Hour)))
	}
	require.NoError(t, db.preMigrationSnapshot(ctx, 1, 2, nil)) // newest now, so 3 kept = new + c + b
	files, err := filepath.Glob(filepath.Join(db.backupDir, "pre-migration-*.db"))
	require.NoError(t, err)
	var got []string
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	require.Len(t, got, 3)
	require.Contains(t, got, "pre-migration-old-c.db")
	require.Contains(t, got, "pre-migration-old-b.db")
	require.Condition(t, func() bool {
		for _, g := range got {
			if strings.HasPrefix(g, "pre-migration-1-2-") {
				return true
			}
		}
		return false
	})
}

func TestMigrateNonFreshPendingTakesSnapshot(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	n := len(ms)
	db.migrations = append(ms, migration{version: n + 1, name: fmt.Sprintf("%04d_extra.sql", n+1), sql: "CREATE TABLE extra_t (a INTEGER);"})

	files, _ := filepath.Glob(filepath.Join(db.backupDir, "pre-migration-*.db"))
	require.Empty(t, files)
	require.NoError(t, db.migrate(ctx))

	files, err = filepath.Glob(filepath.Join(db.backupDir, fmt.Sprintf("pre-migration-%d-%d-*.db", n, n+1)))
	require.NoError(t, err)
	require.Len(t, files, 1)
	v, err := db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, n+1, v)
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM sqlite_master WHERE name='extra_t'"))
}

func TestOpenNoMigrateRefusesOlderOrMissingSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")

	_, err := Open(ctx, Options{Path: path, NoMigrate: true})
	require.ErrorContains(t, err, "does not exist")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "a refused open creates nothing")

	db, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	latest, err := db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), latest)
	require.NoError(t, db.Close())

	// current schema: opens, and does not touch the backup dir or the version
	cli, err := Open(ctx, Options{Path: path, NoMigrate: true, NoCheckpoint: true})
	require.NoError(t, err)
	v, err := cli.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, latest, v)
	require.NoError(t, cli.Close())

	// an older schema is refused, not migrated
	db, err = Open(ctx, Options{Path: path})
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest-1))
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = Open(ctx, Options{Path: path, NoMigrate: true})
	require.ErrorContains(t, err, "older than this binary")
	_, statErr = os.Stat(filepath.Join(filepath.Dir(path), "backup"))
	require.True(t, os.IsNotExist(statErr), "no pre-migration snapshot was taken")
}

func TestCloseNoCheckpointLeavesTheWALAlone(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	server, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	cli, err := Open(ctx, Options{Path: path, NoMigrate: true, NoCheckpoint: true})
	require.NoError(t, err)
	require.NoError(t, cli.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO folders (name, position) VALUES ('X', 99)")
		return err
	}))
	require.NoError(t, cli.Close())
	st, err := os.Stat(path + "-wal")
	require.NoError(t, err, "the server's WAL is still there")
	require.Positive(t, st.Size(), "and was not truncated by the CLI's close")
}
