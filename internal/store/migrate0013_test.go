package store

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schema12WithRows builds a schema-12 database holding items (some starred) and ledger rows (some
// unread) and returns its path. 0013 only adds two indexes, so dropping them from a fresh database
// and stepping user_version back is exactly the schema 12 that 0012 left.
func schema12WithRows(t *testing.T) string {
	t.Helper()
	e := newEnv(t)
	feed := e.addFeed("https://a.example/feed")
	for i := int64(1); i <= 40; i++ {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, ?1 % 2, ?1 % 3 = 0, ?1, ?1 / 2, 'u' || ?1, 'c', 't')`, i, feed)
		e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 't' || ?1, ?1 % 4 != 0, 1, 1)`, 1000+i, feed)
	}
	e.exec("DROP INDEX idx_items_starred_sort")
	e.exec("DROP INDEX idx_trimmed_unread")
	e.exec(undo0017)
	e.exec("PRAGMA user_version = 12")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	return path
}

// 0013 adds the two partial indexes and changes no row.
func TestMigration0013AddsTheIndexes(t *testing.T) {
	t.Parallel()
	path := schema12WithRows(t)
	db := reopen(t, path)
	r := db.Reader()
	for name, def := range map[string]string{
		"idx_items_starred_sort": "CREATE INDEX idx_items_starred_sort ON items(sort_at, id) WHERE starred = 1",
		"idx_trimmed_unread":     "CREATE INDEX idx_trimmed_unread ON trimmed_items(id) WHERE read = 0",
	} {
		require.Equal(t, def, scalar[string](t, r, "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?", name))
	}
	require.Equal(t, 40, scalar[int](t, r, "SELECT count(*) FROM items"))
	require.Equal(t, 13, scalar[int](t, r, "SELECT count(*) FROM items WHERE starred = 1"))
	require.Equal(t, 40, scalar[int](t, r, "SELECT count(*) FROM trimmed_items"))
	require.Equal(t, 10, scalar[int](t, r, "SELECT count(*) FROM trimmed_items WHERE read = 0"))
}

// The queries the indexes are for use them, on a fresh database and after ANALYZE: the Starred
// view's page pick (both keyset directions, with and without a cursor) and the
// ledger half of the global mark-all-as-read, in both the Reader API (MarkAllRead) and the web app
// (MarkScopeRead) forms.
func TestStarredAndLedgerIndexPlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var feeds []int64
	for f := 0; f < 3; f++ {
		feeds = append(feeds, e.addFeed(fmt.Sprintf("https://a.example/%d", f)))
	}
	for i := int64(1); i <= 900; i++ {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, ?1 % 3 != 0, ?1 % 50 = 0, ?1, ?1 / 2, 'u' || ?1, 'c', 't')`, i, feeds[i%3])
		e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 't' || ?1, ?1 % 10 != 0, 1, 1)`, 10_000+i, feeds[i%3])
	}
	plan := func(q string, args ...any) string {
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+q, args...)
		require.NoError(t, err)
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			b.WriteString(detail + "\n")
		}
		require.NoError(t, rows.Err())
		return b.String()
	}
	ledger := map[string]string{
		"MarkAllRead":   "UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts AND " + notDeletingItemSQL,
		"MarkScopeRead": "UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts AND " + notDeletingItemSQL + " RETURNING id, feed_id",
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			_, err := e.db.writer.ExecContext(e.ctx, "ANALYZE")
			require.NoError(t, err)
		}
		for _, oldest := range []bool{false, true} {
			for _, cur := range []*Cursor{nil, {SortAt: 200, ID: 400, Asc: oldest}} {
				q := CardQuery{View: "starred", Oldest: oldest, Cursor: cur, Limit: 30}
				sqlText, args, _, err := listCardsSQL(q)
				require.NoError(t, err)
				p := plan(sqlText, args...)
				label := fmt.Sprintf("%+v analyze=%v\n%s", q, analyze, p)
				require.Contains(t, p, "idx_items_starred_sort", label)
				require.Equal(t, 1, strings.Count(p, "USE TEMP B-TREE FOR ORDER BY"), label) // the outer sort of the page's ids only
			}
		}
		for name, stmt := range ledger {
			p := plan(stmt, sql.Named("ts", int64(1)<<62))
			label := fmt.Sprintf("%s analyze=%v\n%s", name, analyze, p)
			require.Contains(t, p, "idx_trimmed_unread (id<?)", label)
			require.NotContains(t, p, "SCAN trimmed_items", label)
		}
	}
}
