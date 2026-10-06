package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// feed_daily_new counts the new, unread items a feed brought in per local day: the read-rate denominator.
func TestFeedDailyNewCountsOnlyUnreadArrivals(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	// numbered(3) is published base-3m, -2m, -1m; the cutoff makes the oldest arrive already read.
	e.exec("UPDATE feeds SET initial_read_before = ? WHERE id = ?", base.Add(-150*time.Second).Unix(), id)
	e.fetchBody(id, rss(numbered(3)...))
	day := base.UTC().Format("2006-01-02")
	require.Equal(t, 2, e.count("SELECT new_items FROM feed_daily_new WHERE feed_id = ? AND local_date = ?", id, day),
		"the item that arrived read is not counted")

	// A later fetch on the same day adds to the row, the next day starts a new one.
	e.fetchBody(id, rss(append(numbered(3), spec{guid: "later", age: 0})...))
	require.Equal(t, 3, e.count("SELECT new_items FROM feed_daily_new WHERE feed_id = ? AND local_date = ?", id, day))
	e.clk.Advance(24 * time.Hour)
	e.fetchBody(id, rss(append(numbered(3), spec{guid: "later", age: 0}, spec{guid: "next", age: 0})...))
	require.Equal(t, 2, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
	require.Equal(t, 1, e.count("SELECT new_items FROM feed_daily_new WHERE feed_id = ? AND local_date = ?",
		id, base.Add(24*time.Hour).UTC().Format("2006-01-02")))
}

func TestFeedDailyNewSurvivesUnsubscribeAndNoNewItemsWritesNothing(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	e.fetchBody(id, rss(numbered(2)...)) // nothing new
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
	require.Equal(t, 2, e.count("SELECT new_items FROM feed_daily_new WHERE feed_id = ?", id))

	e.exec("DELETE FROM feeds WHERE id = ?", id)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id), "no foreign key: the counts outlive the feed")
}
