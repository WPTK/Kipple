package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The ledger half of a feed-scoped mark-all-as-read (Reader API and web app) reads only the feed's unread
// ledger rows, on a fresh database and after ANALYZE: it must not walk every ledger row of the feed through
// the (feed_id, uid) key and filter afterwards.
func TestFeedScopedLedgerMarkPlan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var feeds []int64
	for f := range 3 {
		feeds = append(feeds, e.addFeed(fmt.Sprintf("https://a.example/%d", f)))
	}
	for i := int64(1); i <= 900; i++ {
		e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 't' || ?1, ?1 % 10 != 0, 1, 1)`, 10_000+i, feeds[i%3])
	}
	plan := func(q string, args ...any) string {
		// The writer ran ANALYZE, so it holds the new statistics; a pooled reader may have read the schema before.
		rows, err := e.db.writer.QueryContext(e.ctx, "EXPLAIN QUERY PLAN "+q, args...)
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
	_, _, ledgerBatched, batchedArgs, err := markAllSQL(MarkScope{FeedID: feeds[0]}, false, int64(1)<<62, 1, MarkAllBatch)
	require.NoError(t, err)
	_, _, feedWhere, base := markSelectSQL(MarkScope{FeedID: feeds[0]}, MarkFilter{}, int64(1)<<62, "", false, false)
	_, _, ledgerFolder, folderArgs, err := markAllSQL(MarkScope{FolderID: 1}, false, int64(1)<<62, 1, MarkAllBatch)
	require.NoError(t, err)
	_, _, folderWhere, folderBase := markSelectSQL(MarkScope{FolderTreeID: 1}, MarkFilter{}, int64(1)<<62, "", false, false)
	ledger := map[string]struct {
		sql  string
		args []any
	}{
		"MarkAllReadBatched": {ledgerBatched, batchedArgs},
		"MarkScopeRead":      {"UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :max" + feedWhere + " AND " + notDeletingItemSQL + " RETURNING id, feed_id", base},
		// The folder forms: the Reader API's label folder and the web app's folder with its subfolders.
		"MarkAllReadBatched in a folder": {ledgerFolder, folderArgs},
		"MarkScopeRead in a folder":      {"UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :max" + folderWhere + " AND " + notDeletingItemSQL + " RETURNING id, feed_id", folderBase},
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			_, err := e.db.writer.ExecContext(e.ctx, "ANALYZE")
			require.NoError(t, err)
		}
		for name, st := range ledger {
			p := plan(st.sql, st.args...)
			label := fmt.Sprintf("%s analyze=%v\n%s", name, analyze, p)
			require.Contains(t, p, "idx_trimmed_feed_unread (feed_id=? AND id<?)", label)
			require.NotContains(t, p, "sqlite_autoindex_trimmed_items_1", label)
			require.NotContains(t, p, "SCAN trimmed_items", label)
		}
	}
	// The index does not change which rows are marked: only the feed's unread ledger rows, up to the bound.
	var n int64
	require.NoError(t, e.db.writer.QueryRowContext(e.ctx, "SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", feeds[0]).Scan(&n))
	require.Positive(t, n)
	res, err := e.db.writer.ExecContext(e.ctx, ledgerBatched, batchedArgs...)
	require.NoError(t, err)
	got, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, n, got)
	require.Equal(t, 0, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", feeds[0]))
	require.Positive(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM trimmed_items WHERE feed_id != ? AND read = 0", feeds[0]))
}
