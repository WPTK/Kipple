package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Deleting many feeds is one commitment (issue #486): every feed that exists is marked in a single transaction,
// so a caller that gives up part-way leaves marked feeds that are never fetched and that the next start finishes,
// never a library that is half deleted with the rest still fetching.
func TestDeleteFeedsMarksTheWholeSetFirst(t *testing.T) {
	withTrimBatch(t, 50)
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 120)
	b := e.loadFeed("http://b.example/feed", 120)
	c := e.loadFeed("http://c.example/feed", 10)

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	afterPurgeBatch = func() { cancel() } // the client goes away after the first batch of the first feed
	t.Cleanup(func() { afterPurgeBatch = nil })
	gone, err := e.db.DeleteFeeds(ctx, []int64{a, b, c, 9999}, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, gone)
	afterPurgeBatch = nil

	require.Equal(t, 3, e.count("SELECT count(*) FROM feeds WHERE id IN (?, ?, ?) AND enabled = 0 AND url LIKE 'kipple:deleting:%'", a, b, c),
		"all three are marked at once, not one after the other")
	for _, id := range []int64{a, b, c} {
		require.NotContains(t, e.dueIDs(), id)
	}

	done, err := e.db.ResumeFeedDeletes(e.ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a, b, c}, done)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id IN (?, ?, ?)", a, b, c))
}

func TestDeleteFeedsRemovesTheSetAndSkipsMissingIDs(t *testing.T) {
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 5)
	b := e.loadFeed("http://b.example/feed", 5)
	keep := e.loadFeed("http://keep.example/feed", 5)
	e.exec("UPDATE items SET starred = 1 WHERE feed_id = ? AND title = 'title g1'", a)

	gone, err := e.db.DeleteFeeds(e.ctx, []int64{a, b, 9999}, false)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a, b}, gone)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id IN (?, ?)", a, b))
	require.Equal(t, 5, e.count("SELECT count(*) FROM items WHERE feed_id = ?", keep), "a feed outside the set is untouched")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.disabled_reason = 'archive' AND i.starred = 1"),
		"the starred item went to the archive")

	gone, err = e.db.DeleteFeeds(e.ctx, []int64{a, b}, false)
	require.NoError(t, err)
	require.Empty(t, gone, "a repeat of the call is not an error")
	require.ErrorIs(t, e.db.DeleteFeed(e.ctx, a, false), ErrFeedNotFound)
}

func TestDeleteFeedsRefusesTheArchiveWithStarredBeforeMarkingAnything(t *testing.T) {
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 3)
	e.exec("UPDATE items SET starred = 1 WHERE feed_id = ? AND title = 'title g0'", a)
	_, err := e.db.DeleteFeeds(e.ctx, []int64{a}, false)
	require.NoError(t, err)
	var arch int64
	require.NoError(t, e.db.Reader().QueryRow("SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&arch))
	b := e.loadFeed("http://b.example/feed", 3)

	_, err = e.db.DeleteFeeds(e.ctx, []int64{b, arch}, false)
	require.ErrorIs(t, err, ErrArchiveHasStarred)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND enabled = 1", b), "the other feed was not marked")
	require.Contains(t, e.dueIDs(), b)

	gone, err := e.db.DeleteFeeds(e.ctx, []int64{b, arch}, true)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{b, arch}, gone)
}
