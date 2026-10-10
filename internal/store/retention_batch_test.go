package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// withTrimBatch shrinks the per-transaction trim/purge bound for one test.
func withTrimBatch(t *testing.T, n int) {
	t.Helper()
	old := trimBatch
	trimBatch = n
	t.Cleanup(func() { trimBatch = old })
}

// loadFeed adds a feed holding n items g0..g(n-1) (g0 the oldest), untrimmed.
func (e *env) loadFeed(url string, n int) int64 {
	e.t.Helper()
	id := e.addFeed(url)
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(n)...))
	return id
}

// A fetch commit trims at most trimBatch items, the oldest first, and leaves
// the rest for a later trim; TrimOnly then finishes the job in batches with
// one fetch_log row and the same ledger and unread accounting as a one-shot
// trim.
func TestTrimIsBoundedPerTransaction(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 1500)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	info := e.fetchBody(id, rss(numbered(1500)...))
	require.EqualValues(t, 100, info.Trimmed, "the fetch commit trims one bounded batch")
	require.Equal(t, 1400, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE title IN ('title g0', 'title g99')"), "oldest first")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'title g100'"))

	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 1350, n)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE title >= 'title g1450' AND title <= 'title g1499' AND length(title) = 11"))
	require.Equal(t, 1450, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ?", id))
	require.Equal(t, 1450, e.count("SELECT count(*) FROM trimmed_content"))
	require.Equal(t, 1450, e.count("SELECT trimmed_unread_count FROM feeds WHERE id = ?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome = 'trim_only' AND trimmed_items = 1350"),
		"one log row covers every batch")
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")

	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TrimOnly stops between batches when its context ends; what committed stays,
// and the next call resumes.
func TestTrimOnlyResumesAfterCancel(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 600)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	batches := 0
	afterTrimBatch = func() {
		batches++
		if batches == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { afterTrimBatch = nil })
	n, err := e.db.TrimOnly(ctx, id, fetch.TriggerRetention)
	afterTrimBatch = nil
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 200, n, "two batches committed before the cancel")
	require.Equal(t, 400, e.count("SELECT count(*) FROM items"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome = 'trim_only' AND trimmed_items = 200"))

	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 350, n)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))
}

// Batched trimming converges on exactly what one unbounded trim does, with
// muted, starred, held and read items in the mix.
func TestTrimBatchesMatchOneShot(t *testing.T) {
	type outcome struct {
		kept, ledger []string
		unread       int
	}
	run := func(batch int) outcome {
		withTrimBatch(t, batch)
		e := newEnv(t)
		id := e.loadFeed("http://a.example/feed", 400)
		e.exec("UPDATE items SET muted_by = 1, read = 1 WHERE CAST(substr(title, 8) AS INTEGER) % 3 = 0")
		e.exec("UPDATE items SET starred = 1 WHERE CAST(substr(title, 8) AS INTEGER) % 37 = 0")
		e.exec("UPDATE items SET read = 1 WHERE CAST(substr(title, 8) AS INTEGER) % 5 = 0")
		e.exec("UPDATE items SET retain_until = ? WHERE CAST(substr(title, 8) AS INTEGER) % 41 = 1", e.clk.Now().Unix()+3600)
		e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
		_, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
		require.NoError(t, err)
		var o outcome
		o.kept = column(t, e, "SELECT title || ':' || read || ':' || (muted_by IS NOT NULL) FROM items ORDER BY title")
		o.ledger = column(t, e, "SELECT uid || ':' || read FROM trimmed_items ORDER BY uid")
		o.unread = e.count("SELECT trimmed_unread_count FROM feeds WHERE id = ?", id)
		return o
	}
	one := run(1 << 30)
	batched := run(7)
	require.NotEmpty(t, one.ledger)
	require.Equal(t, one, batched)
}

func column(t *testing.T, e *env, q string) []string {
	t.Helper()
	rows, err := e.db.Reader().Query(q)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// DeleteFeed empties a large feed in bounded, durable batches before removing
// it: an interrupted delete leaves the feed (and its starred items) in place and
// a retry finishes it.
func TestDeleteFeedBatchedAndResumable(t *testing.T) {
	withTrimBatch(t, 100)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 1000)
	e.exec("UPDATE items SET starred = 1 WHERE title IN ('title g0', 'title g500')")
	// a ledger to purge as well
	e.exec("UPDATE feeds SET retention = 500 WHERE id = ?", id)
	_, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Equal(t, 498, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ?", id))

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	batches := 0
	afterPurgeBatch = func() {
		batches++
		if batches == 2 {
			cancel()
		}
	}
	t.Cleanup(func() { afterPurgeBatch = nil })
	require.ErrorIs(t, e.db.DeleteFeed(ctx, id, false), context.Canceled)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ?", id), "the feed row goes last")
	require.Equal(t, 302, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id), "two batches of 100 committed")
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND starred = 1", id), "starred items are never purged")

	afterPurgeBatch = nil
	require.NoError(t, e.db.DeleteFeed(e.ctx, id, false))
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ?", id))
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ?", id))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.disabled_reason = 'archive' AND i.starred = 1"))
	require.Equal(t, 2, e.count("SELECT count(*) FROM items"))
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")
}

// The archive feed is never purged while it holds starred items: the refusal
// still changes nothing. With delete_starred everything goes.
func TestDeleteArchiveRefusalChangesNothing(t *testing.T) {
	withTrimBatch(t, 10)
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 60)
	e.exec("UPDATE items SET starred = 1") // every item moves to the archive
	require.NoError(t, e.db.DeleteFeed(e.ctx, id, false))
	arch := int64(e.count("SELECT id FROM feeds WHERE disabled_reason = 'archive'"))
	e.exec("UPDATE items SET starred = 0 WHERE CAST(substr(title, 8) AS INTEGER) >= 5")

	require.ErrorIs(t, e.db.DeleteFeed(e.ctx, arch, false), ErrArchiveHasStarred)
	require.Equal(t, 60, e.count("SELECT count(*) FROM items WHERE feed_id = ?", arch), "unstarred archive items stay too")
	_, skipped, err := e.db.UnsubscribeSkipped(e.ctx, []FeedRef{{ID: arch}})
	require.NoError(t, err)
	require.Equal(t, []int64{arch}, skipped)
	require.Equal(t, 60, e.count("SELECT count(*) FROM items WHERE feed_id = ?", arch))

	require.NoError(t, e.db.DeleteFeed(e.ctx, arch, true))
	require.Zero(t, e.count("SELECT count(*) FROM items"))
}

// Unsubscribe from the Reader API empties each feed in bounded batches too and
// keeps its starred items.
func TestUnsubscribeBatched(t *testing.T) {
	withTrimBatch(t, 50)
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 300)
	b := e.loadFeed("http://b.example/feed", 120)
	e.exec("UPDATE items SET starred = 1 WHERE feed_id = ? AND title = 'title g7'", a)
	batches := 0
	afterPurgeBatch = func() { batches++ }
	t.Cleanup(func() { afterPurgeBatch = nil })
	ids, skipped, err := e.db.UnsubscribeSkipped(e.ctx, []FeedRef{{ID: a}, {ID: b}, {ID: 999}})
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.ElementsMatch(t, []int64{a, b}, ids)
	require.GreaterOrEqual(t, batches, 300/50+120/50, "items went in bounded batches")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items"), "only the starred item survives, in the archive")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.disabled_reason = 'archive'"))
}

// A trim whose caller knows the feed holds at most N items reads nothing more: given a total
// that understates a feed over its cap, nothing goes, which shows the count and the trim-set
// window did not run. With the true total, or none (-1), the feed is trimmed to its cap.
func TestTrimSkipsAFeedWithinItsCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 110)
	e.exec("UPDATE feeds SET retention = 100 WHERE id = ?", id)
	trim := func(total int) int64 {
		t.Helper()
		var n int64
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			n, _, err = trimFeedBatch(ctx, tx, id, e.clk.Now().Unix(), maxInt64, trimBatch, total)
			return err
		}))
		return n
	}
	require.Zero(t, trim(100))
	require.Equal(t, 110, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items"))

	require.EqualValues(t, 10, trim(-1))
	require.Equal(t, 100, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	require.EqualValues(t, 50, trim(100))
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 60, e.count("SELECT count(*) FROM trimmed_items"))
}

// A fetch commit passes the count it knows: a fetch that leaves the feed within its cap trims
// nothing, one that takes it over trims it back to the cap.
func TestFetchCommitTrimsOnlyOverTheCap(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.loadFeed("http://a.example/feed", 40)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	info := e.fetchBody(id, rss(numbered(50)...))
	require.Equal(t, 10, info.New)
	require.Zero(t, info.Trimmed)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))

	info = e.fetchBody(id, rss(numbered(51)...))
	require.Equal(t, 1, info.New)
	require.EqualValues(t, 1, info.Trimmed)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE title = 'title g0'"), "the oldest went")

	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Zero(t, n, "the commit left nothing for a trim job")
}
