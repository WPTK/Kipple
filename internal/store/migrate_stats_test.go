package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// A database that already holds planner statistics (an install that has run ANALYZE) and is upgraded from
// schema 21 to the latest has none for the indexes the upgrade adds or rebuilds; the PRAGMA optimize that Open
// runs after the migrations gathers them. The three queries those indexes serve must plan onto them straight
// after the upgrade, without waiting for the nightly optimize: the unread list, the per-feed ledger half of a
// mark-as-read, and the feed list's starred count.
func TestUpgradeFromSchema21PlansOntoTheNewIndexes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var feeds []int64
	for f := range 3 {
		feeds = append(feeds, e.addFeed(fmt.Sprintf("https://a.example/%d", f)))
	}
	// Ids carry the crawl second in their millions, and every sort_at is at or before it, so the sort_at
	// migration leaves the rows as they are.
	e.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
		INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash, word_count, title, url)
		SELECT i * 1000000, ?2 + i % 3, i % 10 = 3, i % 50 = 0, i, i, 'u' || i, 'c', 't', (i * 7919) % 3000 + 1,
			'A title of some length ' || i, 'https://a.example/' || i FROM n`, 3000, feeds[0])
	for i := int64(1); i <= 900; i++ {
		e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 't' || ?1, ?1 % 10 != 0, 1, 1)`, 10_000+i, feeds[i%3])
	}
	fresh := schemaObjects(t, e.db.Reader())

	// Step back to schema 21: the two indexes 0022 and 0023 add are gone, the unread-sort index is the
	// narrow one, and the planner statistics are those of that schema.
	e.exec("DROP INDEX idx_items_starred_feed")
	e.exec("DROP INDEX idx_trimmed_feed_unread")
	e.exec("DROP INDEX idx_items_unread_sort")
	e.exec(narrowUnreadSort)
	e.exec("ANALYZE")
	require.Positive(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM sqlite_stat1 WHERE tbl = 'items'"))
	require.Positive(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM sqlite_stat1 WHERE tbl = 'trimmed_items'"))
	e.exec("PRAGMA user_version = 21")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db := reopen(t, path)
	require.Equal(t, fresh, schemaObjects(t, db.Reader()))
	for _, idx := range []string{"idx_items_starred_feed", "idx_trimmed_feed_unread", "idx_items_unread_sort"} {
		require.Positive(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM sqlite_stat1 WHERE idx = ?", idx), idx+" has statistics")
	}

	for name, q := range lengthQueries(t, false) {
		p := planOf(t, db.writer, e.ctx, q.sql, q.args)
		require.Contains(t, p, "COVERING INDEX idx_items_unread_sort", name+"\n"+p)
	}
	_, _, ledgerBatched, batchedArgs, err := markAllSQL(MarkScope{FeedID: feeds[0]}, false, int64(1)<<62, 1, MarkAllBatch)
	require.NoError(t, err)
	p := planOf(t, db.writer, e.ctx, ledgerBatched, batchedArgs)
	require.Contains(t, p, "idx_trimmed_feed_unread (feed_id=? AND id<?)", p)
	require.NotContains(t, p, "sqlite_autoindex_trimmed_items_1", p)
	p = planOf(t, db.writer, e.ctx, starredCountSQL, nil)
	require.Contains(t, p, "COVERING INDEX idx_items_starred_feed", p)
}
