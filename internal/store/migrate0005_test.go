package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// downgradeTo4 turns a current database back into a schema-4 one: the old unicode61-only index.
func downgradeTo4(t *testing.T, e *env) {
	t.Helper()
	for _, s := range []string{
		undo0006,
		`DROP TABLE items_fts`,
		`CREATE VIRTUAL TABLE items_fts USING fts5(title, author, content_text, content='item_search', content_rowid='id', tokenize='unicode61 remove_diacritics 2')`,
		`INSERT INTO items_fts(items_fts) VALUES ('rebuild')`,
		`PRAGMA user_version = 4`,
	} {
		e.exec(s)
	}
}

func ftsCheck5(t *testing.T, e *env, db *DB) {
	t.Helper()
	require.NoError(t, db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items_fts(items_fts, rank) VALUES ('integrity-check', 1)`)
		return err
	}))
}

func TestMigration0005FreshSchema(t *testing.T) {
	db, _ := openTest(t)
	r := db.Reader()
	require.GreaterOrEqual(t, LatestVersion(), 5)
	require.Contains(t, scalar[string](t, r, "SELECT sql FROM sqlite_master WHERE name = 'items_fts'"), "porter unicode61 remove_diacritics 2")
	require.Equal(t, "bm25(4.0, 2.0, 1.0)", scalar[string](t, r, "SELECT v FROM items_fts_config WHERE k = 'rank'"))
	require.Equal(t, 5, scalar[int](t, r, "SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE '%fts%'"))
	requireCleanIntegrity(t, r)
}

func TestMigration0005OnPopulatedSchema4(t *testing.T) {
	e := newEnv(t)
	ids := seedSearch(t, e,
		sitem{"Running the marathon", "Ann", "she runs every morning"},
		sitem{"Computers", "Bob", "computing machines"},
		sitem{"Café", "Cy", "naïve visitors"},
	)
	// Some churn first: a trimmed-away and an updated row.
	e.exec("UPDATE items SET title = 'Marathon running' WHERE id = ?", ids[0])
	downgradeTo4(t, e)
	require.Empty(t, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'run'")) // no stemming yet
	before := e.count("SELECT count(*) FROM items_fts")
	titles := scalar[string](t, e.db.Reader(), "SELECT group_concat(title, '|') FROM (SELECT title FROM items ORDER BY id)")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	r := db.Reader()
	require.Equal(t, before, scalar[int](t, r, "SELECT count(*) FROM items_fts"))
	require.Equal(t, titles, scalar[string](t, r, "SELECT group_concat(title, '|') FROM (SELECT title FROM items ORDER BY id)"))
	require.Equal(t, 1, scalar[int](t, r, "SELECT count(*) FROM items_fts WHERE items_fts MATCH 'run'"))
	require.Equal(t, 1, scalar[int](t, r, "SELECT count(*) FROM items_fts WHERE items_fts MATCH 'computer'"))
	require.Equal(t, 1, scalar[int](t, r, "SELECT count(*) FROM items_fts WHERE items_fts MATCH 'cafe'"))
	requireCleanIntegrity(t, r)
	ftsCheck5(t, e, db)
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-4-*.db"))
	require.Len(t, snaps, 1)

	// The triggers still keep the index in sync: insert, update, delete.
	more := seedMore(t, e, sitem{"Jumping frogs", "Dee", "frogs jump high"})
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'jumped'"))
	e.exec("UPDATE items SET title = 'Swimming fish' WHERE id = ?", more)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'swim'"))
	e.exec("UPDATE item_content SET content_text = 'fish glide' WHERE item_id = ?", more)
	require.Equal(t, 0, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'frogs'"))
	ftsCheck5(t, e, db)
	e.exec("DELETE FROM items WHERE id = ?", more)
	require.Equal(t, 0, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'swim'"))
	ftsCheck5(t, e, db)
}

// seedMore adds one item to the first feed (seedSearch makes its own feed).
func seedMore(t *testing.T, e *env, it sitem) int64 {
	t.Helper()
	fid := int64(scalar[int](t, e.db.Reader(), "SELECT min(id) FROM feeds"))
	id := int64(1_700_000_100_000_000)
	e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
		VALUES (?,?,0,0,5,5,10,?,?,?,?,?,?)`, id, fid, fmt.Sprintf("g%d", id), "c", "t", "https://x/more", it.title, it.author)
	e.exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?,?,?)`, id, "<p>"+it.text+"</p>", it.text)
	return id
}

func TestMigration0005RollsBackOnFailure(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e, sitem{"Running", "Ann", "runs"})
	downgradeTo4(t, e)
	ms, err := loadMigrations()
	require.NoError(t, err)
	bad := append([]migration(nil), ms...)
	last := bad[4]
	// The real file then a failing statement: the old index must survive untouched.
	bad[4] = migration{version: 5, name: last.name, sql: last.sql + "\nCREATE TABLE items (a);"}
	e.db.migrations = bad
	require.Error(t, e.db.migrate(e.ctx))
	v, err := e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 4, v)
	require.NotContains(t, scalar[string](t, e.db.Reader(), "SELECT sql FROM sqlite_master WHERE name = 'items_fts'"), "porter")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'runs'"))
	require.Zero(t, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'run'"))
	// The real migration then applies cleanly.
	e.db.migrations = nil
	require.NoError(t, e.db.migrate(e.ctx))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'run'"))
}

func TestOlderBinaryRefusesSchema5(t *testing.T) {
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 5)
	db.migrations = ms[:4]
	require.ErrorContains(t, db.migrate(context.Background()), "newer than this binary")
}

// The search join still uses the FTS index and the items primary key (design §2.3 plans).
func TestSearchPlan(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e, sitem{"a", "b", "c"})
	rows, err := e.db.Reader().QueryContext(e.ctx, `EXPLAIN QUERY PLAN SELECT i.id FROM items_fts JOIN items i ON i.id = items_fts.rowid
		WHERE items_fts MATCH ? AND i.muted_by IS NULL ORDER BY items_fts.rank LIMIT 10`, `"a" AND ("c" OR "c"*)`)
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan.WriteString(detail + "\n")
	}
	require.Contains(t, plan.String(), "VIRTUAL TABLE INDEX")
	require.Contains(t, plan.String(), "SEARCH i USING INTEGER PRIMARY KEY")
}
