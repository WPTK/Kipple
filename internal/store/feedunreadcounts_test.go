package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A feed with nothing unread (all read, or no items at all) is not in the map: a `counts` event reader takes a
// missing feed as zero. A feed with unread items is, with its count, and a read item is not counted.
func TestFeedUnreadCountsOmitsFeedsWithNothingUnread(t *testing.T) {
	e := newEnv(t)
	withUnread := e.addFeed("https://ex.com/a")
	allRead := e.addFeed("https://ex.com/b")
	empty := e.addFeed("https://ex.com/c")
	add := func(id, feed int64, read int) {
		e.exec(`INSERT INTO items (id, feed_id, read, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, ?3, 1, 1, 'u' || ?1, 'c', 't')`, id, feed, read)
	}
	add(1, withUnread, 0)
	add(2, withUnread, 0)
	add(3, withUnread, 1)
	add(4, allRead, 1)

	per, err := e.db.FeedUnreadCounts(e.ctx)
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{withUnread: 2}, per)
	require.NotContains(t, per, allRead)
	require.NotContains(t, per, empty)

	// The feed that just emptied drops out; the map is never nil.
	e.exec("UPDATE items SET read = 1 WHERE feed_id = ?", withUnread)
	per, err = e.db.FeedUnreadCounts(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, per)
	require.Empty(t, per)
}
