package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sanitize"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	db  *DB
	clk *clock.Fake
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(base)
	db, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &env{t: t, db: db, clk: clk, ctx: context.Background()}
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	return scalar[int](e.t, e.db.Reader(), q, args...)
}

func (e *env) addFeed(url string) int64 {
	e.t.Helper()
	id, err := e.db.AddFeed(e.ctx, NewFeed{URL: url, AllowPrivateNet: true})
	require.NoError(e.t, err)
	return id
}

func (e *env) snap(id int64) fetch.Snapshot {
	e.t.Helper()
	s, ok, err := e.db.FeedSnapshot(e.ctx, e.db.FetchSettings(e.ctx), id)
	require.NoError(e.t, err)
	require.True(e.t, ok)
	s.Trigger = fetch.TriggerScheduled
	return s
}

type spec struct {
	guid, title, body string
	age               time.Duration // published = base - age
	noDate            bool
}

func rss(specs ...spec) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title><link>https://ex.com/</link>`)
	for _, s := range specs {
		if s.title == "" {
			s.title = "title " + s.guid
		}
		if s.body == "" {
			s.body = "<p>body " + s.guid + "</p>"
		}
		fmt.Fprintf(&b, `<item><guid>%s</guid><title>%s</title><link>https://ex.com/%s</link><description><![CDATA[%s]]></description>`,
			s.guid, s.title, s.guid, s.body)
		if !s.noDate {
			fmt.Fprintf(&b, `<pubDate>%s</pubDate>`, base.Add(-s.age).Format(time.RFC1123Z))
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// numbered makes n items g0..g(n-1); g0 is the oldest, published one minute apart.
func numbered(n int) []spec {
	out := make([]spec, n)
	for i := range out {
		out[i] = spec{guid: fmt.Sprintf("g%d", i), age: time.Duration(n-i) * time.Minute}
	}
	return out
}

func (e *env) okResult(snap fetch.Snapshot, body []byte) *fetch.Result {
	e.t.Helper()
	feed, err := fetch.ParseFeed(body, fetch.ParseOptions{FeedURL: snap.URL, DedupMode: snap.DedupMode, Content: sanitize.Content})
	require.NoError(e.t, err)
	now := e.clk.Now()
	return &fetch.Result{
		Snap: snap, StartedAt: now, Outcome: fetch.OutcomeOK, Status: 200, Feed: feed, FinalURL: snap.URL,
		SetValidators: true, ETag: `"e1"`, LastModified: "lm1", BodyHash: "bh1",
		Redirect: fetch.RedirectDecision{Action: fetch.RedirectClear}, TTLHintS: 0,
		NextFetchAt: now.Add(30 * time.Minute), CurrentDelayS: 1800, Notes: feed.Notes,
	}
}

func (e *env) commit(res *fetch.Result) CommitInfo {
	e.t.Helper()
	info, err := e.db.CommitFetch(e.ctx, res)
	require.NoError(e.t, err)
	return info
}

func (e *env) fetchBody(id int64, body []byte) CommitInfo {
	return e.commit(e.okResult(e.snap(id), body))
}

func TestCommitInsertUpdateAndOrdering(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")

	info := e.fetchBody(id, rss(
		spec{guid: "newest", age: time.Hour}, spec{guid: "oldest", age: 3 * time.Hour}, spec{guid: "mid", age: 2 * time.Hour}))
	require.Equal(t, 3, info.New)
	// ids ascend with publication order regardless of document order
	require.Equal(t, []string{"oldest", "mid", "newest"}, func() []string {
		rows, err := e.db.Reader().Query(`SELECT i.title FROM items i ORDER BY id`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			out = append(out, strings.TrimPrefix(s, "title "))
		}
		return out
	}())
	require.Equal(t, 3, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'body'"), "FTS rows follow the inserts")

	// feed bookkeeping
	require.Equal(t, "Feed", scalar[string](t, e.db.Reader(), "SELECT title FROM feeds WHERE id=?", id))
	require.Equal(t, "https://ex.com/", scalar[string](t, e.db.Reader(), "SELECT site_url FROM feeds WHERE id=?", id))
	require.Equal(t, `"e1"`, scalar[string](t, e.db.Reader(), "SELECT etag FROM feeds WHERE id=?", id))
	require.Equal(t, base.Add(30*time.Minute).Unix(), scalar[int64](t, e.db.Reader(), "SELECT next_fetch_at FROM feeds WHERE id=?", id))
	require.Equal(t, base.Unix(), scalar[int64](t, e.db.Reader(), "SELECT last_new_items_at FROM feeds WHERE id=?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome='ok' AND new_items=3 AND first_item_id IS NOT NULL"))
	require.Equal(t, info.NewIDs[0], scalar[int64](t, e.db.Reader(), "SELECT first_item_id FROM fetch_log"))
	require.Equal(t, info.NewIDs[2], scalar[int64](t, e.db.Reader(), "SELECT last_item_id FROM fetch_log"))
	require.Equal(t, info.NewIDs[2], scalar[int64](t, e.db.Reader(), "SELECT CAST(value AS INTEGER) FROM settings WHERE key='sys.id_high_water'"))

	// user state survives a content update; an identical refetch does nothing
	e.exec("UPDATE items SET read=1, starred=1 WHERE title='title mid'")
	info = e.fetchBody(id, rss(spec{guid: "newest", age: time.Hour}, spec{guid: "oldest", age: 3 * time.Hour}, spec{guid: "mid", age: 2 * time.Hour}))
	require.Zero(t, info.New+info.Updated)

	e.clk.Advance(time.Hour)
	info = e.fetchBody(id, rss(
		spec{guid: "newest", age: time.Hour}, spec{guid: "oldest", age: 3 * time.Hour},
		spec{guid: "mid", age: 2 * time.Hour, title: "renamed", body: "<p>entirely new text</p>"}))
	require.Equal(t, 1, info.Updated)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title='renamed' AND read=1 AND starred=1 AND content_changed_at=?", e.clk.Now().Unix()))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'entirely'"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'renamed' AND 0"))
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")
}

func TestMissingDateUsesCrawlTimeAndClampsFuture(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(
		spec{guid: "undated", noDate: true},
		spec{guid: "future", age: -400 * 24 * time.Hour},
		spec{guid: "old", age: 48 * time.Hour}))
	crawl := scalar[int64](t, e.db.Reader(), "SELECT id/1000000 FROM items WHERE title='title undated'")
	require.Equal(t, crawl, scalar[int64](t, e.db.Reader(), "SELECT published_at FROM items WHERE title='title undated'"))
	require.Equal(t, crawl, scalar[int64](t, e.db.Reader(), "SELECT sort_at FROM items WHERE title='title undated'"))
	fid := scalar[int64](t, e.db.Reader(), "SELECT id FROM items WHERE title='title future'")
	require.Equal(t, fid/1000000+86400, scalar[int64](t, e.db.Reader(), "SELECT sort_at FROM items WHERE id=?", fid), "future date clamped to crawl + 24h")
	require.Greater(t, scalar[int64](t, e.db.Reader(), "SELECT published_at FROM items WHERE id=?", fid), fid/1000000+86400, "published_at itself is untouched")
}

func TestRetentionCapStarredExemptAndLedger(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id) // unlimited while loading
	e.fetchBody(id, rss(numbered(300)...))
	require.Equal(t, 300, e.count("SELECT count(*) FROM items"))

	// star the 5 oldest so they would be the first to go
	e.exec("UPDATE items SET starred = 1 WHERE id IN (SELECT id FROM items ORDER BY id LIMIT 5)")
	e.exec("UPDATE items SET read = 1 WHERE id IN (SELECT id FROM items ORDER BY id LIMIT 20 OFFSET 5)")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 245, n)
	require.Equal(t, 55, e.count("SELECT count(*) FROM items"), "50 unstarred + 5 starred")
	require.Equal(t, 5, e.count("SELECT count(*) FROM items WHERE starred = 1"))
	require.Equal(t, 245, e.count("SELECT count(*) FROM trimmed_items"))
	require.Equal(t, 245, e.count("SELECT count(*) FROM trimmed_content"))
	require.Equal(t, 20, e.count("SELECT count(*) FROM trimmed_items WHERE read = 1"), "the ledger keeps the read flag")
	require.Equal(t, 225, e.count("SELECT trimmed_unread_count FROM feeds WHERE id = ?", id))
	require.NotZero(t, e.count("SELECT trimmed_unread_since FROM feeds WHERE id = ?", id))
	require.Equal(t, 55, e.count("SELECT count(*) FROM item_content"), "content cascades with the item")
	// the 50 newest unstarred survive
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE starred = 0 AND title >= 'title g250' AND title <= 'title g299'"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome='trim_only' AND trimmed_items=245 AND trigger='retention'"))
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")
	require.Equal(t, 55, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'body'"), "FTS rows removed with the trim")

	// tombstones: re-seeing the whole document inserts nothing and bumps last_seen_at
	e.clk.Advance(2 * time.Hour)
	info := e.fetchBody(id, rss(numbered(300)...))
	require.Zero(t, info.New, "a tombstoned uid is never re-inserted")
	require.Equal(t, 55, e.count("SELECT count(*) FROM items"))
	require.Equal(t, 245, e.count("SELECT count(*) FROM trimmed_items WHERE last_seen_at = ?", e.clk.Now().Unix()))

	// unstarring makes an item eligible at the next trim
	e.exec("UPDATE items SET starred = 0 WHERE starred = 1")
	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 5, n)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))
}

func TestRetentionUnlimitedAndDefault(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(320)...))
	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, 320, e.count("SELECT count(*) FROM items"), "N = 0 is unlimited")

	// inherit: default 250 applies, and retention.default is read from settings
	e.exec("UPDATE feeds SET retention = NULL WHERE id = ?", id)
	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 70, n)
	e.exec(`INSERT INTO settings(key, value) VALUES ('retention.default', '100')`)
	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 150, n)
	require.Equal(t, 100, e.count("SELECT count(*) FROM items"))
}

func TestRetentionRetainUntilHoldAndRanking(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	specs := numbered(60)
	// a backfilled, very old post published last in the document
	specs = append(specs, spec{guid: "backfill", age: 400 * 24 * time.Hour})
	e.fetchBody(id, rss(specs...))
	e.exec("UPDATE items SET retain_until = ? WHERE title = 'title g0'", e.clk.Now().Unix()+3600)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 10, n)
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE title='title backfill'"), "the backfilled old item ranks last and is trimmed first")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title='title g0'"), "a live retain_until hold is exempt")
	require.Equal(t, 51, e.count("SELECT count(*) FROM items"))

	// once the hold lapses it is eligible again
	e.clk.Advance(2 * time.Hour)
	n, err = e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestRetentionStubsOnlyWhenRestoreDays(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(60)...))
	e.exec(`INSERT INTO settings(key, value) VALUES ('retention.restore_days', '0')`)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	_, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerRetention)
	require.NoError(t, err)
	require.Equal(t, 10, e.count("SELECT count(*) FROM trimmed_items"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM trimmed_content"), "restore_days = 0 writes no stubs")

	// the ledger upsert refreshes read/trimmed_at when an id is trimmed again
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
}

func TestFirstFetchTrimIsNotCountedAsUnreadBacklog(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)
	info := e.fetchBody(id, rss(numbered(300)...))
	require.Equal(t, 300, info.New)
	require.EqualValues(t, 250, info.Trimmed)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))
	require.Equal(t, 250, e.count("SELECT count(*) FROM trimmed_items"))
	require.Zero(t, e.count("SELECT trimmed_unread_count FROM feeds WHERE id = ?", id), "same-transaction trims are excluded")
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE trimmed_items = 250"))
}

func TestTrimRunsOnNotModified(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(80)...))
	e.exec("UPDATE feeds SET retention = 50 WHERE id = ?", id)

	res := &fetch.Result{
		Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeNotModified, Status: 304,
		SetValidators: true, ETag: `"e1"`, LastModified: "lm2", FinalURL: "http://a.example/feed",
		Redirect: fetch.RedirectDecision{Action: fetch.RedirectClear}, NextFetchAt: e.clk.Now().Add(time.Hour), CurrentDelayS: 3600,
	}
	info := e.commit(res)
	require.EqualValues(t, 30, info.Trimmed)
	require.Equal(t, 50, e.count("SELECT count(*) FROM items"))
	require.Equal(t, "lm2", scalar[string](t, e.db.Reader(), "SELECT last_modified FROM feeds WHERE id=?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome='not_modified' AND trimmed_items=30"))
	require.Equal(t, 0, e.count("SELECT consecutive_failures FROM feeds WHERE id=?", id))
}

func TestChunkedCommitOverFiveHundred(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	info := e.fetchBody(id, rss(numbered(620)...))
	require.Equal(t, 620, info.New)
	require.Equal(t, 620, e.count("SELECT count(*) FROM items"))
	for i := 1; i < len(info.NewIDs); i++ {
		require.Greater(t, info.NewIDs[i], info.NewIDs[i-1])
	}
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log"), "one log row for the whole fetch")
	require.Equal(t, 620, e.count("SELECT new_items FROM fetch_log"))
	// publication order == id order across chunk boundaries
	require.Equal(t, "title g0", scalar[string](t, e.db.Reader(), "SELECT title FROM items ORDER BY id LIMIT 1"))
	require.Equal(t, "title g619", scalar[string](t, e.db.Reader(), "SELECT title FROM items ORDER BY id DESC LIMIT 1"))

	// with a cap, the trim runs once at the end
	e.exec("UPDATE feeds SET retention = 100 WHERE id = ?", id)
	e.exec("DELETE FROM items")
	e.exec("DELETE FROM trimmed_items")
	info = e.fetchBody(id, rss(numbered(620)...))
	require.EqualValues(t, 520, info.Trimmed)
	require.Zero(t, e.count("SELECT trimmed_unread_count FROM feeds WHERE id = ?", id))
}

func TestFirstSuccessAndInitialRead(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	cut := base.Add(-90 * time.Minute).Unix()
	e.exec("UPDATE feeds SET custom_title = 'Feed', initial_read_before = ? WHERE id = ?", cut, id)
	e.fetchBody(id, rss(numbered(3)...)) // published base-3m, -2m, -1m: all after the cutoff
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM feeds WHERE custom_title IS NOT NULL"), "custom_title equal to the document title is cleared")
	require.Equal(t, 0, e.count("SELECT count(*) FROM feeds WHERE initial_read_before IS NOT NULL"))

	id2 := e.addFeed("http://b.example/feed")
	e.exec("UPDATE feeds SET initial_read_before = ? WHERE id = ?", base.Add(-150*time.Second).Unix(), id2)
	e.fetchBody(id2, rss(numbered(3)...))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 1", id2), "only the item older than the cutoff is read")
	require.Contains(t, scalar[string](t, e.db.Reader(), "SELECT note FROM fetch_log WHERE feed_id = ?", id2), "initial_read: 1")
}

func TestChurnNote(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(25)...))
	var all []spec
	for i := 0; i < 25; i++ {
		all = append(all, spec{guid: fmt.Sprintf("new%d", i), age: time.Duration(i) * time.Second})
	}
	e.fetchBody(id, rss(all...))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE '%guid_churn_suspected%' AND keep = 1"))
	require.Equal(t, 50, e.count("SELECT count(*) FROM items WHERE read = 0"), "items stay unread")
}

func TestErrorCommit(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	fail := func(class, msg string, status int, gone bool) {
		res := &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError, ErrClass: class, ErrMsg: msg,
			Status: status, Gone: gone, NextFetchAt: e.clk.Now().Add(2 * time.Hour), CurrentDelayS: 7200, Notes: []string{"retry_after=600s"}}
		require.NoError(t, e.db.CommitFetchError(e.ctx, res))
	}
	fail("http", "HTTP 500", 500, false)
	fail("http", "HTTP 500", 500, false)
	require.Equal(t, 2, e.count("SELECT consecutive_failures FROM feeds WHERE id=?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE last_error='HTTP 500' AND last_error_class='http' AND last_status=500 AND enabled=1"))
	require.Equal(t, base.Add(2*time.Hour).Unix(), scalar[int64](t, e.db.Reader(), "SELECT next_fetch_at FROM feeds WHERE id=?", id))
	require.Equal(t, 2, e.count("SELECT count(*) FROM fetch_log WHERE outcome='error' AND error_class='http' AND note='retry_after=600s'"))

	// a success resets the count but keeps last_error
	e.fetchBody(id, rss(numbered(1)...))
	require.Equal(t, 0, e.count("SELECT consecutive_failures FROM feeds WHERE id=?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE last_error IS NOT NULL AND last_error_at IS NOT NULL"))

	fail("gone", "410 Gone", 410, true)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE enabled=0 AND disabled_reason='gone'"))
}

func TestFetchLogCap(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	old := base.Add(-30 * 24 * time.Hour).Unix()
	for i := 0; i < 70; i++ {
		e.exec(`INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome) VALUES (?, 'scheduled', ?, 1, 'ok')`, id, old+int64(i))
	}
	e.exec(`INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, keep) VALUES (?, 'scheduled', ?, 1, 'ok', 1)`, id, old)
	e.fetchBody(id, rss(numbered(1)...))
	// old rows beyond the newest 50 go, except keep rows; the fresh row is always retained
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE keep = 1"))
	require.LessOrEqual(t, e.count("SELECT count(*) FROM fetch_log"), 52)
	require.GreaterOrEqual(t, e.count("SELECT count(*) FROM fetch_log"), 50, "the 50-row floor holds")
}

func TestRedirectMigration(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	newURL := "https://a.example/feed"

	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.FinalURL = newURL
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: newURL, Kind: "permanent", Count: 3}
	e.commit(res)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE url = ? AND url_original = 'http://a.example/feed' AND url_original_key = 'a.example/feed' AND url_key = 'a.example/feed' AND redirect_to IS NULL", newURL))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE keep = 1 AND note LIKE '%redirect_migrated: http://a.example/feed -> https://a.example/feed%'"))
	found, ok, err := FindFeedByURL(e.ctx, e.db.Reader(), "http://a.example/feed")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, id, found, "pre-migration URLs still resolve via url_original_key")

	// a target owned by another feed is refused with a note
	other := e.addFeed("http://b.example/feed")
	res = e.okResult(e.snap(id), rss(numbered(1)...))
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: "https://b.example/feed", Kind: "permanent", Count: 3}
	e.commit(res)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND url = ? AND redirect_to = 'https://b.example/feed' AND redirect_kind = 'permanent'", id, newURL))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE ?", fmt.Sprintf("%%redirect_target_owned_by_feed %d%%", other)))

	// set / clear
	res = e.okResult(e.snap(id), rss(numbered(1)...))
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectSet, To: "https://t.example/f", Kind: "temporary"}
	e.commit(res)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_kind = 'temporary' AND redirect_count = 0", id))
	e.commit(e.okResult(e.snap(id), rss(numbered(1)...)))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to IS NULL", id))
}

func TestRekey(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	e.fetchBody(id, rss(numbered(4)...))
	e.exec("UPDATE items SET read = 1, starred = 1 WHERE title = 'title g1'")
	before := scalar[int64](t, e.db.Reader(), "SELECT id FROM items WHERE title='title g1'")

	// the feed now serves the same links under new guids, plus one genuinely new article
	var specs []spec
	for i := 0; i < 4; i++ {
		s := numbered(4)[i]
		s.guid = "new-" + s.guid
		specs = append(specs, s)
	}
	doc := string(rss(specs...))
	for i := 0; i < 4; i++ { // keep the original links so URLs match by url
		doc = strings.ReplaceAll(doc, fmt.Sprintf("https://ex.com/new-g%d", i), fmt.Sprintf("https://ex.com/g%d", i))
	}
	e.exec("UPDATE feeds SET rekey_pending = 1 WHERE id = ?", id)
	doc = strings.Replace(doc, "</channel>", `<item><guid>fresh</guid><title>fresh</title><link>https://ex.com/fresh</link></item></channel>`, 1)
	res := e.okResult(e.snap(id), []byte(doc))
	info := e.commit(res)
	require.Equal(t, 1, info.New, "only the unmatched item is inserted")
	require.Equal(t, 5, e.count("SELECT count(*) FROM items"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND starred = 1 AND uid LIKE 'g:%'", before), "state kept, uid replaced")
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE title='fresh' AND read = 1"), "leftover new items are inserted read")
	require.Equal(t, 0, e.count("SELECT rekey_pending FROM feeds WHERE id = ?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE keep = 1 AND note LIKE '%rekeyed: 4%'"))
}

func TestIDAllocMonotonicAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kipple.db")
	clk := clock.NewFake(base)
	db, err := Open(context.Background(), Options{Path: path, Clock: clk})
	require.NoError(t, err)
	a := db.IDs().Next()
	b := db.IDs().Next()
	require.Equal(t, base.UnixMicro(), a)
	require.Equal(t, a+1, b, "ids never repeat within a microsecond")
	clk.Set(base.Add(-time.Hour)) // clock steps back
	require.Equal(t, b+1, db.IDs().Next())
	require.Greater(t, db.IDs().Skew(), 59*time.Minute)

	id, err := db.AddFeed(context.Background(), NewFeed{URL: "http://a.example/f"})
	require.NoError(t, err)
	e := &env{t: t, db: db, clk: clk, ctx: context.Background()}
	e.fetchBody(id, rss(numbered(2)...))
	last := db.IDs().Last()
	require.NoError(t, db.Close())

	db2, err := Open(context.Background(), Options{Path: path, Clock: clock.NewFake(base.Add(-2 * time.Hour))})
	require.NoError(t, err)
	defer db2.Close()
	require.Equal(t, last+1, db2.IDs().Next(), "seeded from the high-water mark and MAX(items.id)")
}

func TestCommitKeepsSiteURLWhenFeedHasNoLink(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET site_url = 'https://kept.example/' WHERE id = ?", id)
	body := []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title>` +
		`<item><guid>x</guid><title>t</title><description>d</description></item></channel></rss>`)
	e.fetchBody(id, body)
	require.Equal(t, "https://kept.example/", scalar[string](t, e.db.Reader(), "SELECT site_url FROM feeds WHERE id=?", id))
}
