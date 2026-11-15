package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedUnread adds n unread items (ids 1..n) and m unread ledger rows (ids n+1..n+m) to the feed.
func (e *env) seedUnread(feed int64, n, m int) {
	e.exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1)
		INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
		SELECT i, ?2, 0, 0, 1, 1, 'u' || i, 'c', 't' FROM s`, n, feed)
	e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 + 1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?3)
		INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
		SELECT i, ?2, 'l' || i, 0, 1, 1 FROM s`, n, feed, m)
}

// One batch of a mark-all-read changes at most its limit of rows in each table, oldest first, and says
// when more are left, so its transaction stays short however large the scope.
func TestMarkAllReadBatchIsBounded(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://batch.example/feed")
	e.seedUnread(feed, 10, 5)
	var n int64
	var more bool
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		var err error
		n, more, err = markAllReadBatch(ctx, tx, MarkScope{FeedID: feed}, 1<<40, 100, 3)
		return err
	})
	require.EqualValues(t, 3, n)
	require.True(t, more)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE read = 1 AND id <= 3"), "oldest first")
	require.Equal(t, 3, e.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		var err error
		n, more, err = markAllReadBatch(ctx, tx, MarkScope{FeedID: feed}, 1<<40, 100, 100)
		return err
	})
	require.EqualValues(t, 7, n)
	require.False(t, more)
}

// MarkAllReadBatched ends with the same state as one transaction over the whole scope: every unread item
// and ledger row at or below the cutoff read, the same count returned, and nothing above the cutoff touched.
func TestMarkAllReadBatchedMarksEverything(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://batched.example/feed")
	other := e.addFeed("http://other.example/feed")
	items, ledger := 2*MarkAllBatch+7, MarkAllBatch+3
	e.seedUnread(feed, items, ledger)
	e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
		VALUES (?1, ?2, 0, 0, 1, 1, 'o1', 'c', 't')`, items+ledger+1, other)
	top, err := e.db.MaxCommittedID(e.ctx)
	require.NoError(t, err)

	// A cutoff above everything committed is clamped; another feed is outside the scope.
	n, err := e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: feed}, top+1_000_000, 500)
	require.NoError(t, err)
	require.EqualValues(t, items, n)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", feed))
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", feed))
	require.Equal(t, items, e.count("SELECT count(*) FROM items WHERE read = 1 AND read_at = 500 AND state_changed_at = 500"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", other))

	// Nothing left: a repeat changes nothing.
	n, err = e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: feed}, top, 600)
	require.NoError(t, err)
	require.Zero(t, n)

	// The cutoff keeps a later item unread.
	e.seedUnreadAbove(other, 3, top)
	n, err = e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: other}, top, 700)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", other))
}

// seedUnreadAbove adds n unread items with ids above after.
func (e *env) seedUnreadAbove(feed int64, n int, after int64) {
	e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 + 1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?3)
		INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
		SELECT i, ?2, 0, 0, 1, 1, 'a' || i, 'c', 't' FROM s`, after, feed, n)
}
