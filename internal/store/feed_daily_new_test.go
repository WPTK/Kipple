package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// feed_daily_new counts the new, unread items a feed brought in per local day: the read-rate denominator.

// newsDaily is the feed's count for the (UTC) day of at, 0 when there is no row.
func (e *env) newsDaily(id int64, at time.Time) int {
	e.t.Helper()
	return e.count("SELECT COALESCE(sum(new_items), 0) FROM feed_daily_new WHERE feed_id = ? AND local_date = ?",
		id, at.UTC().Format("2006-01-02"))
}

// newer is n items n0..n(n-1) newer than anything numbered makes (published base+1s and later).
func newer(n int) []spec {
	out := make([]spec, n)
	for i := range out {
		out[i] = spec{guid: fmt.Sprintf("n%d", i), age: -time.Duration(i+1) * time.Second}
	}
	return out
}

// The first successful fetch brings the document's backlog, published before the subscription: not
// counted. Later fetches add to today's row; the next day starts a new one.
func TestFeedDailyNewSkipsTheFirstFetchAndSumsPerDay(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(100)...))
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"), "the backlog of the first fetch is not counted")

	e.fetchBody(id, rss(append(numbered(100), newer(2)...)...))
	require.Equal(t, 2, e.newsDaily(id, base))
	e.fetchBody(id, rss(append(numbered(100), newer(3)...)...))
	require.Equal(t, 3, e.newsDaily(id, base), "a later fetch the same day adds to the row")

	e.clk.Advance(24 * time.Hour)
	e.fetchBody(id, rss(append(numbered(100), newer(4)...)...))
	require.Equal(t, 2, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
	require.Equal(t, 1, e.newsDaily(id, base.Add(24*time.Hour)))

	e.fetchBody(id, rss(append(numbered(100), newer(4)...)...)) // nothing new: no write
	require.Equal(t, 1, e.newsDaily(id, base.Add(24*time.Hour)))
}

// An item that arrives already read (the initial-read cutoff, a filter that marks read) or muted was
// never a choice to open: not counted.
func TestFeedDailyNewLeavesOutReadAndMutedArrivals(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(1)...))
	e.mkFilter(newFilter("mute", "muteme"))
	e.mkFilter(newFilter("mark_read", "readme"))
	e.exec("UPDATE feeds SET initial_read_before = ? WHERE id = ?", base.Add(-30*time.Minute).Unix(), id)
	e.fetchBody(id, rss(append(numbered(1),
		spec{guid: "old", age: time.Hour}, // before the cutoff: arrives read
		spec{guid: "m", title: "muteme", age: -time.Second},
		spec{guid: "r", title: "readme", age: -2 * time.Second},
		spec{guid: "ok", age: -3 * time.Second})...))
	require.Equal(t, 5, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 1, e.newsDaily(id, base), "only the unread, unmuted arrival is counted")
}

// A rekey's leftover new items are inserted read: not counted.
func TestFeedDailyNewLeavesOutRekeyLeftovers(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(2)...))
	e.exec("UPDATE feeds SET rekey_pending = 1 WHERE id = ?", id)
	info := e.fetchBody(id, rss(spec{guid: "leftover", age: -time.Second}))
	require.Equal(t, 1, info.New)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title = 'title leftover' AND read = 1"))
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"))
}

// Items the same commit trims (retention keeps the newest N) were never shown: not counted.
func TestFeedDailyNewLeavesOutItemsTheSameCommitTrims(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(50)...))
	// 200 new items, all newer than the 50 kept: the trim keeps the newest 50 of the feed, all of them new.
	info := e.fetchBody(id, rss(append(numbered(50), newer(200)...)...))
	require.Equal(t, 200, info.New)
	require.EqualValues(t, 200, info.Trimmed)
	require.Equal(t, 50, e.newsDaily(id, base), "200 arrived, 50 were kept")
}

// A document over the chunk threshold commits in several transactions; the count covers every chunk.
func TestFeedDailyNewCountsEveryChunk(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(10)...))
	info := e.fetchBody(id, rss(numbered(620)...))
	require.Equal(t, 610, info.New)
	require.Equal(t, 610, e.newsDaily(id, base))
}

// Each chunk counts its own items in its own transaction: a chunk that rolls back takes its count with
// it, and the chunks that committed before it stay counted (their items are durable and unread).
func TestFeedDailyNewRolledBackChunkKeepsTheCommittedChunks(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(10)...))
	// Oldest first in chunks of 250: g600 is in chunk 3, so chunks 1 and 2 (g10..g499) commit.
	e.exec(fmt.Sprintf(`CREATE TRIGGER boom BEFORE INSERT ON items WHEN NEW.uid = 'g:%s' BEGIN SELECT RAISE(ABORT, 'boom'); END`, fetch.H("g600")))
	_, err := e.db.CommitFetch(e.ctx, e.okResult(e.snap(id), rss(numbered(620)...)))
	require.ErrorContains(t, err, "chunk 3/3")
	require.Equal(t, 490, e.newsDaily(id, base), "the committed chunks are counted, the failed one is not")

	e.exec("DROP TRIGGER boom")
	info := e.fetchBody(id, rss(numbered(620)...))
	require.Equal(t, 120, info.New)
	require.Equal(t, 610, e.newsDaily(id, base), "the retry adds what it inserts")
}

// A URL edit between chunks stops the commit: the chunks that committed stay counted.
func TestFeedDailyNewStaleCommitKeepsTheCommittedChunks(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(10)...))
	e.exec(fmt.Sprintf(`CREATE TRIGGER moved AFTER INSERT ON items WHEN NEW.uid = 'g:%s'
		BEGIN UPDATE feeds SET url = 'http://b.example/feed' WHERE id = %d; END`, fetch.H("g10"), id))
	info, err := e.db.CommitFetch(e.ctx, e.okResult(e.snap(id), rss(numbered(620)...)))
	require.NoError(t, err)
	require.True(t, info.Stale)
	require.Equal(t, 240, info.New)
	require.Equal(t, 240, e.newsDaily(id, base))
}

// A commit counts on the day its first chunk ran, so the trim's correction in the last chunk lands on
// the same row as the chunks' counts.
func TestFeedDailyNewTrimCorrectionAcrossChunks(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 100 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(10)...))
	// The commit spans midnight: the clock moves a day on after its first chunk.
	advanced := false
	commitChunkTestHook = func() {
		if !advanced {
			advanced = true
			e.clk.Advance(24 * time.Hour)
		}
	}
	t.Cleanup(func() { commitChunkTestHook = nil })
	// 610 new items in three chunks. The trim keeps the newest 100: the 10 first-fetched items (g0..g9
	// were published 10 to 1 minutes before base, newer than the 620-item document dates them) and 90 new.
	info := e.fetchBody(id, rss(numbered(620)...))
	require.Equal(t, 610, info.New)
	require.EqualValues(t, 520, info.Trimmed)
	require.Equal(t, 90, e.newsDaily(id, base), "counted on the first chunk's day, less the 520 trimmed")
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
}

// A trim that removes every counted item of the day deletes the row rather than leave a 0.
func TestFeedDailyNewTrimToZeroDeletesTheRow(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(50)...))
	// Two new items, both older than the 50 kept: the trim removes both.
	info := e.fetchBody(id, rss(append(numbered(50), spec{guid: "o1", age: time.Hour}, spec{guid: "o2", age: 2 * time.Hour})...))
	require.Equal(t, 2, info.New)
	require.EqualValues(t, 2, info.Trimmed)
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"))
}

// Editing a feed's URL points it at a document never fetched: its first success is a backlog, like a
// new subscription's, and is not counted.
func TestFeedDailyNewSkipsTheFirstFetchAfterAURLEdit(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	nu := "http://a.example/other"
	_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &nu, Cols: map[string]any{}})
	require.NoError(t, err)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ? AND last_success_at IS NOT NULL", id))
	e.fetchBody(id, rss(newer(50)...))
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"), "the new URL's backlog is not counted")
	e.fetchBody(id, rss(append(newer(50), spec{guid: "next", age: -time.Hour})...))
	require.Equal(t, 1, e.newsDaily(id, base))
}

func TestFeedDailyNewSurvivesUnsubscribe(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	e.fetchBody(id, rss(append(numbered(2), newer(2)...)...))
	require.Equal(t, 2, e.newsDaily(id, base))
	e.exec("DELETE FROM feeds WHERE id = ?", id)
	require.Equal(t, 2, e.newsDaily(id, base), "no foreign key: the counts outlive the feed")
}
