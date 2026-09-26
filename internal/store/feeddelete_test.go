package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func (e *env) dueIDs() []int64 {
	e.t.Helper()
	snaps, err := e.db.DueFeeds(e.ctx, e.db.FetchSettings(e.ctx), e.clk.Now().Add(365*24*time.Hour).Unix(), 1000)
	require.NoError(e.t, err)
	var ids []int64
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	return ids
}

// Review finding: purgeFeedItems empties a feed in committed batches before the row goes. Cut off in
// between, the feed stayed subscribed with its history gone, and the scheduler refetched it as
// brand-new unread items under new ids. Now the first step marks it, so an interrupted delete leaves
// a feed that is never fetched again (not by the scheduler, not by a fetch already in flight) until a
// retry finishes the delete.
func TestInterruptedDeleteIsNeverRefetched(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	const url = "http://a.example/feed"
	id := e.loadFeed(url, 1000)
	e.exec("UPDATE items SET starred = 1 WHERE title IN ('title g0', 'title g500')")
	inFlight := e.snap(id) // a fetch that started before the delete
	require.Contains(t, e.dueIDs(), id)

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	batches := 0
	afterPurgeBatch = func() {
		batches++
		if batches == 2 {
			cancel() // the client gives up between batches
		}
	}
	t.Cleanup(func() { afterPurgeBatch = nil })
	require.ErrorIs(t, e.db.DeleteFeed(ctx, id, false), context.Canceled)
	afterPurgeBatch = nil
	require.Equal(t, 800, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND enabled = 0", id))
	require.True(t, strings.HasPrefix(scalar[string](t, e.db.Reader(), "SELECT url FROM feeds WHERE id = ?", id), deletingURLPrefix))

	// The scheduler never picks it.
	require.NotContains(t, e.dueIDs(), id)
	enabled, err := e.db.EnabledFeeds(e.ctx, e.db.FetchSettings(e.ctx))
	require.NoError(t, err)
	for _, s := range enabled {
		require.NotEqual(t, id, s.ID)
	}

	// The fetch in flight commits nothing: its URL is stale.
	maxBefore := e.count("SELECT max(id) FROM items")
	info, err := e.db.CommitFetch(e.ctx, e.okResult(inFlight, rss(numbered(1000)...)))
	require.NoError(t, err)
	require.True(t, info.Stale)
	require.Zero(t, info.New)
	require.Equal(t, 800, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, maxBefore, e.count("SELECT max(id) FROM items"), "no new ids were handed out")
	logs := e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ?", id)
	require.NoError(t, e.db.CommitFetchError(e.ctx, &fetch.Result{Snap: inFlight, StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError,
		ErrMsg: "boom", ErrClass: "network", NextFetchAt: e.clk.Now()}))
	require.Equal(t, logs, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ?", id))

	// The UI cannot switch it back on half-emptied; its old URL is free for a new subscription.
	on := true
	_, err = e.db.PatchFeed(e.ctx, id, FeedPatch{Enabled: &on})
	require.ErrorIs(t, err, ErrFeedNotFound)
	other := e.addFeed(url)
	require.NotEqual(t, id, other)

	// A retry finishes the delete; starred items reach the archive.
	require.NoError(t, e.db.DeleteFeed(e.ctx, id, false))
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ?", id))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.disabled_reason = 'archive' AND i.starred = 1"))
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")
}

// A Reader API unsubscribe that is cut off between batches (the client timed out) leaves marked
// feeds; the next unsubscribe, or ResumeFeedDeletes at startup, finishes them.
func TestUnsubscribeCancelledBetweenBatchesResumes(t *testing.T) {
	withTrimBatch(t, 50)
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 300)
	b := e.loadFeed("http://b.example/feed", 120)
	e.exec("UPDATE items SET starred = 1 WHERE feed_id = ? AND title = 'title g7'", b)
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	afterPurgeBatch = func() { cancel() }
	t.Cleanup(func() { afterPurgeBatch = nil })
	_, _, err := e.db.UnsubscribeSkipped(ctx, []FeedRef{{URL: "http://a.example/feed"}, {ID: b}})
	require.ErrorIs(t, err, context.Canceled)
	afterPurgeBatch = nil
	require.Equal(t, 2, e.count("SELECT count(*) FROM feeds WHERE id IN (?, ?) AND enabled = 0 AND url LIKE 'kipple:deleting:%'", a, b),
		"both are marked, including the one referenced by URL and not yet purged")
	require.NotContains(t, e.dueIDs(), a)
	require.NotContains(t, e.dueIDs(), b)

	done, err := e.db.ResumeFeedDeletes(e.ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a, b}, done)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id IN (?, ?)", a, b))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items"), "only the starred item survives, in the archive")
	done, err = e.db.ResumeFeedDeletes(e.ctx)
	require.NoError(t, err)
	require.Empty(t, done)
}

// A crash between the purge batches (modelled by a panic after a committed batch): the feed is left
// marked, and the next start's ResumeFeedDeletes removes it. An unsubscribe retry works the same.
func TestCrashMidDeleteResumes(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 500)
	afterPurgeBatch = func() { panic("crash") }
	t.Cleanup(func() { afterPurgeBatch = nil })
	require.Panics(t, func() { _ = e.db.DeleteFeed(e.ctx, id, true) })
	afterPurgeBatch = nil
	require.Equal(t, 400, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.NotContains(t, e.dueIDs(), id)

	ids, _, err := e.db.UnsubscribeSkipped(e.ctx, []FeedRef{{ID: id}})
	require.NoError(t, err, "a retry through the Reader API resumes it too")
	require.Equal(t, []int64{id}, ids)
	require.Zero(t, e.count("SELECT count(*) FROM items"))
}
