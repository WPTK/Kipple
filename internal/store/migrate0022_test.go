package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 0022 adds the starred-by-feed index and changes no row: a schema-21 database (the index dropped and
// user_version stepped back) upgrades to the same objects as a fresh one.
func TestMigration0022AddsTheIndex(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("https://a.example/feed")
	for i := int64(1); i <= 30; i++ {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, 0, ?1 % 3 = 0, ?1, ?1, 'u' || ?1, 'c', 't')`, i, feed)
	}
	fresh := schemaObjects(t, e.db.Reader())
	e.exec("DROP INDEX idx_items_starred_feed")
	e.exec("PRAGMA user_version = 21")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db := reopen(t, path)
	r := db.Reader()
	require.Equal(t, "CREATE INDEX idx_items_starred_feed ON items(feed_id) WHERE starred = 1",
		scalar[string](t, r, "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_items_starred_feed'"))
	require.Equal(t, fresh, schemaObjects(t, r))
	require.Equal(t, 30, scalar[int](t, r, "SELECT count(*) FROM items"))
	require.Equal(t, 10, scalar[int](t, r, "SELECT count(*) FROM items WHERE starred = 1"))
}

// The feed list's query counts starred items from the index alone, on a fresh database and after ANALYZE,
// and the counts are right.
func TestStarredFeedCountPlan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var feeds []int64
	for f := range 3 {
		feeds = append(feeds, e.addFeed(fmt.Sprintf("https://a.example/%d", f)))
	}
	for i := int64(1); i <= 900; i++ {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, 0, ?1 % 50 = 0, ?1, ?1, 'u' || ?1, 'c', 't')`, i, feeds[i%3])
	}
	require.Equal(t, "CREATE INDEX idx_items_starred_feed ON items(feed_id) WHERE starred = 1",
		scalar[string](t, e.db.Reader(), "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_items_starred_feed'"), "a fresh database has the index")
	for _, analyze := range []bool{false, true} {
		if analyze {
			_, err := e.db.writer.ExecContext(e.ctx, "ANALYZE")
			require.NoError(t, err)
		}
		// The writer ran ANALYZE, so it holds the new statistics; a pooled reader may have read the schema before.
		rows, err := e.db.writer.QueryContext(e.ctx, "EXPLAIN QUERY PLAN "+starredCountSQL)
		require.NoError(t, err)
		var b strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			b.WriteString(detail + "\n")
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		label := fmt.Sprintf("analyze=%v\n%s", analyze, b.String())
		require.Contains(t, b.String(), "COVERING INDEX idx_items_starred_feed", label)
		require.NotContains(t, b.String(), "TEMP B-TREE", label)
	}
	list, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.NoError(t, err)
	total := int64(0)
	for _, f := range list {
		total += f.StarredCount
	}
	require.Equal(t, int64(18), total)
}
