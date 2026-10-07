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
	// 610 new items in three chunks, oldest first (the last inserts 120). The trim keeps the newest 100: the 10
	// first-fetched items (g0..g9 were published 10 to 1 minutes before base, newer than the 620-item
	// document dates them) and 90 new, all from the last chunk.
	info := e.fetchBody(id, rss(numbered(620)...))
	require.Equal(t, 610, info.New)
	require.EqualValues(t, 520, info.Trimmed)
	// The last chunk's 30 trimmed items were never visible: taken back. The first two chunks' 490 were
	// visible between the chunks and may have been opened: they stay counted, and in the row.
	require.Equal(t, 580, e.newsDaily(id, base), "counted on the first chunk's day, less the last chunk's 30 trimmed")
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
	early := e.ids(`SELECT id FROM trimmed_items WHERE feed_id = ? ORDER BY id LIMIT 1`, id)[0]
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ? AND ? BETWEEN first_item AND last_item", id, early))
}

// An item of the feed between two counted runs splits them even when a trim has moved it to the ledger
// before the next run: it may have been read while it was kept.
func TestFeedDailyNewTrimmedItemBetweenRunsSplitsThem(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(1)...))
	e.mkFilter(newFilter("mark_read", "readme"))
	e.fetchBody(id, rss(append(numbered(1), spec{guid: "a", age: -time.Second}, spec{guid: "r", title: "readme", age: -2 * time.Second})...))
	r := e.ids(`SELECT id FROM items WHERE feed_id = ? AND title = 'readme'`, id)[0]
	e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) SELECT id, feed_id, uid, 1, 0, 0 FROM items WHERE id = ?`, r)
	e.exec(`DELETE FROM items WHERE id = ?`, r)
	e.fetchBody(id, rss(append(numbered(1), spec{guid: "b", age: -3 * time.Second})...))
	require.Equal(t, 2, e.newsDaily(id, base))
	require.Equal(t, 2, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id), "a, then b after the trimmed item")
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ? AND ? BETWEEN first_item AND last_item", id, r))
}

// A trim that takes counted items from one of two rows of a day corrects that row only.
func TestFeedDailyNewTrimCorrectionTwoRows(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(48)...)) // 48 to 1 minutes old
	e.mkFilter(newFilter("mark_read", "readme"))
	// Inserted oldest first: a1 (3 h old), a2, then the read-marked r, then b1, b2. Keeping the newest 50
	// of 53 trims a1 and the two oldest backlog items.
	e.fetchBody(id, rss(append(numbered(48),
		spec{guid: "a1", age: 3 * time.Hour}, spec{guid: "a2", age: 30 * time.Second},
		spec{guid: "r", title: "readme", age: -1 * time.Second},
		spec{guid: "b1", age: -2 * time.Second}, spec{guid: "b2", age: -3 * time.Second})...))
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND title = 'title a1'", id))
	rows := e.ids(`SELECT new_items FROM feed_daily_new WHERE feed_id = ? ORDER BY first_item`, id)
	require.Equal(t, []int64{1, 2}, rows, "a1 is taken back from the first row; the second keeps b1 and b2")
}

// A trim that removes every counted item of the day deletes the row rather than leave a 0.
func TestFeedDailyNewTrimToZeroDeletesTheRow(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(50)...))
	// Record what the chunk writes, so the test sees the row exist before the trim takes it back.
	e.exec("CREATE TABLE seen (n INTEGER)")
	e.exec("CREATE TRIGGER seen_daily AFTER INSERT ON feed_daily_new BEGIN INSERT INTO seen VALUES (NEW.new_items); END")
	// Two new items, both older than the 50 kept: the trim removes both.
	info := e.fetchBody(id, rss(append(numbered(50), spec{guid: "o1", age: time.Hour}, spec{guid: "o2", age: 2 * time.Hour})...))
	require.Equal(t, 2, info.New)
	require.EqualValues(t, 2, info.Trimmed)
	require.Equal(t, 2, e.count("SELECT sum(n) FROM seen"), "the chunk counted both")
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"), "the trim took both back and deleted the row")
}

// Editing a feed's URL points it at a document never fetched: its first success is a backlog, like a
// new subscription's, and is not counted. The edit touches nothing else the first success decides: the
// feed keeps its last success, and a custom name equal to the new document's title stays.
func TestFeedDailyNewSkipsTheFirstFetchAfterAURLEdit(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	nu := "http://a.example/other"
	_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &nu, Cols: map[string]any{"custom_title": "Feed"}})
	require.NoError(t, err)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND last_success_at IS NOT NULL AND url_succeeded = 0", id))
	e.fetchBody(id, rss(newer(50)...)) // the document's title is "Feed"
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"), "the new URL's backlog is not counted")
	require.Equal(t, "Feed", scalar[string](t, e.db.Reader(), "SELECT COALESCE(custom_title, '') FROM feeds WHERE id = ?", id))
	require.Equal(t, 1, e.count("SELECT url_succeeded FROM feeds WHERE id = ?", id))
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

// A permanent redirect migration moves the same document to a new URL: the feed keeps url_succeeded,
// so the migrating fetch and the next one count their new items.
func TestFeedDailyNewRedirectMigrationKeepsCounting(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	newURL := "https://a.example/feed"
	res := e.okResult(e.snap(id), rss(append(numbered(2), newer(1)...)...))
	res.FinalURL = newURL
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: newURL, Kind: "permanent", Count: 3}
	require.True(t, e.commit(res).Migrated)
	require.Equal(t, 1, e.count("SELECT url_succeeded FROM feeds WHERE id = ? AND url = ?", id, newURL))
	require.Equal(t, 1, e.newsDaily(id, base))
	e.fetchBody(id, rss(append(numbered(2), newer(3)...)...))
	require.Equal(t, 3, e.newsDaily(id, base), "the fetch at the migrated URL counts too")
}

// Discovery turns a page address into the feed it links: the first document of that feed is its backlog
// and is not counted; the next fetch's new items are.
func TestFeedDailyNewDiscoveredFeedSkipsItsFirstDocument(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	e.commitDiscovered(e.discovered(id, "https://blog.example/feed.xml"))
	require.Zero(t, e.count("SELECT url_succeeded FROM feeds WHERE id = ?", id))
	e.fetchBody(id, rss(numbered(20)...))
	require.Zero(t, e.count("SELECT count(*) FROM feed_daily_new"), "the discovered feed's backlog is not counted")
	e.fetchBody(id, rss(append(numbered(20), newer(2)...)...))
	require.Equal(t, 2, e.newsDaily(id, base))
}
