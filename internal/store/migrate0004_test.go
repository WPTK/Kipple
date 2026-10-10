package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/fetch"
)

func requireCleanIntegrity(t *testing.T, q Querier) {
	t.Helper()
	require.Equal(t, "ok", scalar[string](t, q, "PRAGMA integrity_check"))
	rows, err := q.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	require.NoError(t, err)
	require.False(t, rows.Next(), "foreign_key_check must return no rows")
	require.NoError(t, rows.Close())
}

func columnNames(t *testing.T, q Querier, table string) map[string]bool {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), fmt.Sprintf("SELECT name FROM pragma_table_info('%s')", table))
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out[n] = true
	}
	return out
}

func TestMigration0004FreshSchema(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	r := db.Reader()
	require.GreaterOrEqual(t, LatestVersion(), 4)
	for _, tbl := range []string{"filters", "devices"} {
		require.Equal(t, 1, scalar[int](t, r, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", tbl), tbl)
	}
	require.True(t, columnNames(t, r, "items")["muted_by"])
	require.True(t, columnNames(t, r, "item_content")["categories_json"])
	require.True(t, columnNames(t, r, "trimmed_content")["categories_json"])
	require.True(t, columnNames(t, r, "feeds")["auto_read_days"])
	require.Equal(t, 1, scalar[int](t, r, "SELECT count(*) FROM sqlite_master WHERE name='idx_items_muted'"))
	requireCleanIntegrity(t, r)
}

func TestMigration0004Constraints(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fid := e.addFeed("https://ex.com/feed")
	ok := func(q string, args ...any) { e.exec(q, args...) }
	bad := func(q string, args ...any) {
		t.Helper()
		err := e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q, args...)
			return err
		})
		require.Error(t, err, q)
	}
	ok(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','text','["a"]','mute')`)
	ok(`INSERT INTO filters (scope, feed_id, kind, terms, action) VALUES ('feed', ?, 'regex','["a+b"]','star')`, fid)
	ok(`INSERT INTO filters (scope, folder_id, kind, terms, action) VALUES ('folder', 1, 'text','["a"]','highlight')`)
	bad(`INSERT INTO filters (scope, feed_id, kind, terms, action) VALUES ('global', ?, 'text','["a"]','mute')`, fid)
	bad(`INSERT INTO filters (scope, kind, terms, action) VALUES ('feed','text','["a"]','mute')`)
	bad(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','regex','["a"]','highlight')`)
	bad(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','text','"a"','mute')`)
	bad(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','text','[','mute')`)
	bad(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','text','["a"]','delete')`)
	bad(`INSERT INTO filters (scope, kind, terms, action, fields) VALUES ('global','text','["a"]','mute','{}')`)
	bad(`INSERT INTO devices (id, created_at, last_seen_at) VALUES ('short', 1, 1)`)
	bad(`INSERT INTO devices (id, settings, created_at, last_seen_at) VALUES ('0123456789abcdef', '[]', 1, 1)`)
	ok(`INSERT INTO devices (id, created_at, last_seen_at) VALUES ('0123456789abcdef', 1, 1)`)
	bad(`UPDATE feeds SET auto_read_days = 366 WHERE id = ?`, fid)
	ok(`UPDATE feeds SET auto_read_days = 0 WHERE id = ?`, fid)
	// Deleting the feed and folder cascades their filters.
	ok(`DELETE FROM feeds WHERE id = ?`, fid)
	require.Equal(t, 2, e.count("SELECT count(*) FROM filters"))
	ok(`INSERT INTO folders (id, name) VALUES (9, 'F')`)
	ok(`UPDATE filters SET folder_id = 9 WHERE scope = 'folder'`)
	ok(`DELETE FROM folders WHERE id = 9`)
	require.Equal(t, 1, e.count("SELECT count(*) FROM filters"))
}

// downgradeTo3 turns a current database back into a schema-3 one.
func downgradeTo3(t *testing.T, e *env) {
	t.Helper()
	for _, s := range []string{
		undo0007,
		undo0006,
		`DROP TABLE filters`, `DROP TABLE devices`, `DROP INDEX idx_items_muted`,
		`ALTER TABLE items DROP COLUMN muted_was_read`, `ALTER TABLE items DROP COLUMN muted_by`,
		`ALTER TABLE item_content DROP COLUMN categories_json`,
		`ALTER TABLE trimmed_content DROP COLUMN categories_json`,
		`ALTER TABLE feeds DROP COLUMN auto_read_days`,
		`PRAGMA user_version = 3`,
	} {
		e.exec(s)
	}
}

func TestMigration0004OnPopulatedSchema3(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fid := e.addFeed("https://ex.com/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", fid)
	e.fetchBody(fid, rss(numbered(40)...))
	e.exec(`UPDATE items SET read = 1, read_at = 5 WHERE rowid % 3 = 0`)
	e.exec(`UPDATE items SET starred = 1, starred_at = 6 WHERE rowid % 7 = 0`)
	e.exec(`INSERT INTO item_fulltext (item_id, extracted_at, error) SELECT id, 5, 'x' FROM items LIMIT 3`)
	items := e.count("SELECT count(*) FROM items")
	fts := e.count("SELECT count(*) FROM items_fts")
	titles := scalar[string](t, e.db.Reader(), "SELECT group_concat(title, '|') FROM (SELECT title FROM items ORDER BY id)")
	downgradeTo3(t, e)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	r := db.Reader()
	require.Equal(t, items, scalar[int](t, r, "SELECT count(*) FROM items"))
	require.Equal(t, fts, scalar[int](t, r, "SELECT count(*) FROM items_fts"))
	require.Equal(t, titles, scalar[string](t, r, "SELECT group_concat(title, '|') FROM (SELECT title FROM items ORDER BY id)"))
	require.Equal(t, items, scalar[int](t, r, "SELECT count(*) FROM items WHERE muted_by IS NULL"))
	require.Equal(t, items, scalar[int](t, r, "SELECT count(*) FROM item_content WHERE categories_json IS NULL"))
	require.Equal(t, 0, scalar[int](t, r, "SELECT count(*) FROM filters"))
	requireCleanIntegrity(t, r)
	// The snapshot taken before the migration exists.
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-3-*.db"))
	require.Len(t, snaps, 1)
	// The FTS index is consistent with its content.
	require.NoError(t, db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items_fts(items_fts) VALUES ('integrity-check')`)
		return err
	}))
}

func TestMigration0004RollsBackOnFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	downgradeTo3(t, e)
	ms, err := loadMigrations()
	require.NoError(t, err)
	bad := append([]migration(nil), ms...)
	last := bad[3]
	// The real file, then a statement that fails: everything must roll back.
	bad[3] = migration{version: 4, name: last.name, sql: last.sql + "\nCREATE TABLE filters (a);"}
	e.db.migrations = bad
	require.Error(t, e.db.migrate(e.ctx))
	v, err := e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 3, v)
	require.Equal(t, 0, e.count("SELECT count(*) FROM sqlite_master WHERE name IN ('filters','devices','idx_items_muted')"))
	require.False(t, columnNames(t, e.db.Reader(), "items")["muted_by"])
	// The real migration then applies cleanly.
	e.db.migrations = nil
	require.NoError(t, e.db.migrate(e.ctx))
	v, err = e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
}

// An older binary (three migrations) must refuse a schema-4 database.
func TestOlderBinaryRefusesSchema4(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 4)
	db.migrations = ms[:3]
	err = db.migrate(context.Background())
	require.ErrorContains(t, err, "newer than this binary")
}

// Trim writes categories_json into the restore stub and a restore copies it back.
func TestCategoriesSurviveTrimAndRestore(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	fid := e.addFeed("https://ex.com/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", fid)
	e.fetchBody(fid, rss(numbered(60)...))
	e.exec(`UPDATE item_content SET categories_json = '["news","go"]' WHERE item_id IN (SELECT id FROM items ORDER BY sort_at, id LIMIT 3)`)
	victim := int64(scalar[int](t, e.db.Reader(), "SELECT id FROM items ORDER BY sort_at, id LIMIT 1"))
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", fid)
	_, err := e.db.TrimOnly(e.ctx, fid, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE id = ?", victim))
	require.Equal(t, `["news","go"]`, scalar[string](t, e.db.Reader(), "SELECT categories_json FROM trimmed_content WHERE id = ?", victim))
	require.Equal(t, 10, e.count("SELECT count(*) FROM trimmed_content"))
	require.Equal(t, 7, e.count("SELECT count(*) FROM trimmed_content WHERE categories_json IS NULL"))

	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := SetStarred(ctx, tx, []int64{victim}, true, e.clk.Now().Unix())
		require.Len(t, res.Restored, 1)
		return err
	}))
	require.Equal(t, `["news","go"]`, scalar[string](t, e.db.Reader(), "SELECT categories_json FROM item_content WHERE item_id = ?", victim))
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"), "a restore never brings back a mute")
}

// recordingHandler timestamps the runner's "applied migration" log lines.
type recordingHandler struct {
	mu    sync.Mutex
	names []string
	at    []time.Time
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "store: applied migration" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "name" {
			h.names = append(h.names, a.Value.String())
			h.at = append(h.at, time.Now())
		}
		return true
	})
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

func rawCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer raw.Close()
	out := map[string]int{}
	for _, tbl := range []string{"settings", "folders", "feeds", "items", "item_content", "item_fulltext", "trimmed_items", "trimmed_content", "fetch_log", "stats_events", "sessions"} {
		var n int
		if err := raw.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err != nil {
			continue // table absent at this schema version
		}
		out[tbl] = n
	}
	return out
}

// TestRehearsalOnRealDatabase migrates a COPY of a real phase 1 database through every
// pending migration. It skips when the file is absent (CI). Set KIPPLE_REHEARSAL_DB to
// point at another export.
func TestRehearsalOnRealDatabase(t *testing.T) {
	t.Parallel()
	src := os.Getenv("KIPPLE_REHEARSAL_DB")
	if src == "" {
		src = `<backup-dir>\kipple\kipple-phase1-20260925-202922.db`
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no rehearsal database at %s", src)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "kipple.db")
	in, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, in, 0o600))

	before := rawCounts(t, path)
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	var fromVersion int
	require.NoError(t, raw.QueryRow("PRAGMA user_version").Scan(&fromVersion))
	// Search results on the old unicode61 index, for the before/after comparison below.
	terms := []string{"apple", "google", "security", "news", "climate", "video", "review", "market"}
	oldHits := map[string]map[int64]bool{}
	if fromVersion < 5 {
		for _, term := range terms {
			oldHits[term] = ftsHits(t, raw, term)
		}
	}
	require.NoError(t, raw.Close())

	h := &recordingHandler{}
	start := time.Now()
	db, err := Open(context.Background(), Options{Path: path, Logger: slog.New(h), Clock: clock.NewFake(base)})
	require.NoError(t, err)
	total := time.Since(start)
	ctx := context.Background()
	v, err := db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)

	t0 := time.Now()
	requireCleanIntegrity(t, db.Reader())
	checkTook := time.Since(t0)
	require.Equal(t, "ok", scalar[string](t, db.Reader(), "PRAGMA quick_check"))
	require.Equal(t, before["items"], scalar[int](t, db.Reader(), "SELECT count(*) FROM items"))
	require.Equal(t, before["feeds"], scalar[int](t, db.Reader(), "SELECT count(*) FROM feeds"))
	require.Equal(t, before["items"], scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE muted_by IS NULL"))
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items_fts(items_fts) VALUES ('integrity-check')`)
		return err
	}))
	// Every exact-token hit of the old index is still a hit (porter stems both sides), and stemming
	// only adds rows.
	var sbSearch strings.Builder
	for _, term := range terms {
		newHits := ftsHits(t, db.Reader(), term)
		for id := range oldHits[term] {
			require.True(t, newHits[id], "term %q lost item %d in the porter index", term, id)
		}
		if fromVersion < 5 {
			fmt.Fprintf(&sbSearch, " %s:%d->%d", term, len(oldHits[term]), len(newHits))
		}
	}
	t.Log("search hits old->new:" + sbSearch.String())
	// Strong integrity check: the index against its content view (rank = 1).
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items_fts(items_fts, rank) VALUES ('integrity-check', 1)`)
		return err
	}))
	rehearsalSearchTimings(t, db)
	require.NoError(t, db.Close())
	after := rawCounts(t, path)
	require.Equal(t, before["items"], after["items"])
	require.Equal(t, before["item_content"], after["item_content"])

	var sb strings.Builder
	fmt.Fprintf(&sb, "rehearsal: schema %d -> %d, Open (snapshot + migrations + PRAGMA optimize) %v, integrity+fk checks %v\n", fromVersion, v, total, checkTook)
	prev := start
	for i, n := range h.names {
		fmt.Fprintf(&sb, "  applied %s at +%v (step %v)\n", n, h.at[i].Sub(start), h.at[i].Sub(prev))
		prev = h.at[i]
	}
	fmt.Fprintf(&sb, "  rows before: %v\n  rows after:  %v\n", before, after)
	snaps, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-migration-*.db"))
	for _, s := range snaps {
		if st, err := os.Stat(s); err == nil {
			fmt.Fprintf(&sb, "  snapshot %s (%d bytes)\n", filepath.Base(s), st.Size())
		}
	}
	t.Log("\n" + sb.String())
}

// ftsHits is the set of item ids the raw term matches in items_fts.
func ftsHits(t *testing.T, q Querier, term string) map[int64]bool {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), `SELECT rowid FROM items_fts WHERE items_fts MATCH ?`, `"`+term+`"`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		out[id] = true
	}
	require.NoError(t, rows.Err())
	return out
}

// rehearsalSearchTimings runs the pathological searches of the review (many one-letter words,
// typo plus short words, short explicit prefixes) and the stem-widening words against the real
// library, asserts every page is served under 300 ms and logs the timings (best of 3 runs).
func rehearsalSearchTimings(t *testing.T, db *DB) {
	t.Helper()
	queries := []string{
		"a b c d e f g h i j k l m n o p ",
		"zzqx a e i o u s t ",
		"a* b* c* d* e* f* g* h* i* j* k* l* m* n* o* p* ",
		"apple", "apple ", "police ", "apple pie ", "zzqx apple ",
	}
	var sb strings.Builder
	for _, q := range queries {
		for _, typing := range []bool{false, true} {
			var best time.Duration
			var n int
			var fb bool
			for run := 0; run < 3; run++ {
				t0 := time.Now()
				cards, _, f, err := db.ListCardsFB(context.Background(), CardQuery{View: "all", Query: q, Limit: 50, Typing: typing})
				d := time.Since(t0)
				if errors.Is(err, ErrSearchTooBroad) {
					n, fb = -1, false
				} else {
					require.NoError(t, err, q)
					n, fb = len(cards), f
				}
				if run == 0 || d < best {
					best = d
				}
			}
			fmt.Fprintf(&sb, "\n  %-52q typing=%-5v page=%3d fallback=%-5v %v", q, typing, n, fb, best.Round(time.Millisecond))
			require.Less(t, best, 300*time.Millisecond, "search %q typing=%v", q, typing)
		}
	}
	t.Log("search page timings:" + sb.String())
}
