package store

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// putDailyNew writes one feed_daily_new row whose counted ids span first..last.
func (e *env) putDailyNew(feed int64, date string, n int, first, last int64) {
	e.t.Helper()
	e.exec(`INSERT INTO feed_daily_new (local_date, feed_id, new_items, first_item, last_item) VALUES (?,?,?,?,?)`, date, feed, n, first, last)
}

// read puts one open of item with enough read time to count as read.
func (e *env) read(date string, item, feed int64) {
	e.t.Helper()
	s := fmt.Sprintf("s%s-%d", date, item)
	e.putStat("open", date, item, feed, s, nil, "F")
	e.putStat("read_time", date, item, feed, s, 20, "F")
}

func rateOf(t *testing.T, r StatsReadRate) float64 {
	t.Helper()
	require.NotNil(t, r.Rate)
	return *r.Rate
}

func (e *env) ids(q string, args ...any) []int64 {
	e.t.Helper()
	rows, err := e.db.Reader().QueryContext(e.ctx, q, args...)
	require.NoError(e.t, err)
	var out []int64
	require.NoError(e.t, eachRow(rows, func() error {
		var id int64
		out = append(out, id)
		return rows.Scan(&out[len(out)-1])
	}))
	return out
}

func srcByFeed(s *StatsSummary) map[string]StatsSource {
	m := map[string]StatsSource{}
	for _, x := range s.Sources {
		m[x.FeedID] = x
	}
	return m
}

// A new subscription's first document is a backlog: reading 20 of its 50 items says nothing about the
// feed's new items. The rate is 2 read of the 5 that arrived since, through the real fetch path.
func TestStatsReadRateCountsOnlyItemsThatArrivedCounted(t *testing.T) {
	e := newEnv(t) // the summary's today is 2026-09-24
	e.putStat("open", "2026-09-01", 1, 999, "old", nil, "F")

	e.clk.Set(base.AddDate(0, 0, -5)) // the 19th: subscribe, a 50-item backlog
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(50)...))
	e.clk.Set(base.AddDate(0, 0, -4)) // the 20th: 5 new items
	e.fetchBody(id, rss(append(numbered(50), newer(5)...)...))
	require.Equal(t, 5, e.newsDaily(id, e.clk.Now()))

	for _, it := range e.ids(`SELECT id FROM items WHERE feed_id = ? AND title LIKE 'title g%' ORDER BY id LIMIT 20`, id) {
		e.read("2026-09-21", it, id)
	}
	for _, it := range e.ids(`SELECT id FROM items WHERE feed_id = ? AND title LIKE 'title n%' ORDER BY id LIMIT 2`, id) {
		e.read("2026-09-21", it, id)
	}

	s := e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-20", *s.ReadRateFrom, "where counting began, not where statistics did (the 1st)")
	require.Equal(t, "2026-09-23", *s.ReadRateTo, "today is not a complete day")
	src := srcByFeed(s)[strconv.FormatInt(id, 10)]
	require.Equal(t, 22, src.ItemsRead)
	require.Equal(t, StatsReadRate{ItemsRead: 2, NewItems: 5, Rate: src.ReadRate.Rate}, src.ReadRate)
	require.InDelta(t, 0.4, rateOf(t, src.ReadRate), 1e-9)

	// The first rate is there on the day the data is: a range of only the 20th.
	s = e.summary("2026-09-20", "2026-09-20")
	require.Equal(t, "2026-09-20", *s.ReadRateFrom)
	require.Equal(t, 5, s.ReadRate.NewItems)
}

// On one day: a counted fetch, a URL edit, the new URL's first document (a backlog), and a counted fetch.
// An item muted between two counted ones in one document. Each splits the day into runs of counted ids,
// so reading the backlog or the muted item adds nothing: the reads are of counted items only.
func TestStatsReadRateRunsLeaveOutUncountedItemsBetween(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-01", 1, 999, "old", nil, "F")
	e.clk.Set(base.AddDate(0, 0, -4)) // the 20th
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))                      // the subscription's backlog
	e.fetchBody(id, rss(append(numbered(2), newer(3)...)...)) // counted: n0..n2
	nu := "http://a.example/other"
	_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &nu})
	require.NoError(t, err)
	backlog := []spec{{guid: "b0", age: -10 * time.Second}, {guid: "b1", age: -11 * time.Second}, {guid: "b2", age: -12 * time.Second}}
	e.fetchBody(id, rss(backlog...)) // the new URL's first document: not counted
	e.mkFilter(newFilter("mute", "muteme"))
	e.fetchBody(id, rss(append(backlog,
		spec{guid: "c0", age: -20 * time.Second},
		spec{guid: "m", title: "muteme", age: -21 * time.Second},
		spec{guid: "c1", age: -22 * time.Second})...)) // counted: c0 and c1, with the muted item between
	require.Equal(t, 5, e.newsDaily(id, e.clk.Now()))
	require.Equal(t, 3, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id), "n0..n2, c0, c1")

	for _, it := range e.ids(`SELECT id FROM items WHERE feed_id = ? AND (title LIKE 'title b%' OR title = 'muteme' OR title IN ('title n0', 'title c1'))`, id) {
		e.read("2026-09-21", it, id)
	}
	src := srcByFeed(e.summary("2026-09-01", "2026-09-24"))[strconv.FormatInt(id, 10)]
	require.Equal(t, 6, src.ItemsRead)
	require.Equal(t, StatsReadRate{ItemsRead: 2, NewItems: 5, Rate: src.ReadRate.Rate}, src.ReadRate)
	require.InDelta(t, 0.4, rateOf(t, src.ReadRate), 1e-9)
}

// Counted fetches with nothing of the feed between them extend one row, so a feed that brings an item at
// every fetch keeps one row a day.
func TestFeedDailyNewRunsJoinAcrossFetches(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(2)...))
	for i := 1; i <= 4; i++ {
		e.fetchBody(id, rss(append(numbered(2), newer(i)...)...))
	}
	require.Equal(t, 4, e.newsDaily(id, base))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feed_daily_new WHERE feed_id = ?", id))
}

// The window runs over complete days from where reads are complete (covered_from). No window or nothing
// new is no data (nil), never 0%.
func TestStatsReadRateWindow(t *testing.T) {
	e := newEnv(t) // today is 2026-09-24

	e.read("2026-09-10", 1, 1)
	s := e.summary("2026-09-01", "2026-09-24")
	require.Nil(t, s.ReadRateFrom, "no arrival was ever counted: no window")
	require.Nil(t, s.ReadRate.Rate)
	require.Nil(t, s.Sources[0].ReadRate.Rate)

	// Reads are complete from the 5th.
	e.putStat("open", "2026-09-05", 9, 2, "early", nil, "F")
	e.putDailyNew(1, "2026-09-01", 100, 100, 199) // before the window
	e.putDailyNew(1, "2026-09-10", 4, 1, 4)       // item 1 (read), 2 (a bounce), 3 (read), 4
	e.putDailyNew(1, "2026-09-20", 6, 10, 15)
	e.putDailyNew(3, "2026-09-15", 10, 300, 309) // a feed with arrivals and no activity
	e.putDailyNew(1, "2026-09-24", 7, 20, 26)    // today: not complete
	e.putStat("open", "2026-09-11", 2, 1, "bounce", nil, "F")
	e.putStat("read_time", "2026-09-11", 2, 1, "bounce", 2, "F")
	e.read("2026-09-12", 3, 1)
	e.read("2026-09-12", 150, 1) // arrived on the 1st, before the window
	e.read("2026-09-24", 21, 1)  // arrived today, after the window

	s = e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-05", *s.ReadRateFrom)
	require.Equal(t, "2026-09-23", *s.ReadRateTo)
	src := srcByFeed(s)
	require.Equal(t, 4, src["1"].ItemsRead)
	require.Equal(t, 2, src["1"].ReadRate.ItemsRead, "a bounce is not a read; items from outside the window do not count")
	require.Equal(t, 10, src["1"].ReadRate.NewItems)
	require.InDelta(t, 0.2, rateOf(t, src["1"].ReadRate), 1e-9)
	require.Zero(t, src["2"].ReadRate.ItemsRead)
	require.Nil(t, src["2"].ReadRate.Rate, "nothing new arrived: a dash, not a percentage")
	require.Equal(t, 2, s.ReadRate.ItemsRead)
	require.Equal(t, 20, s.ReadRate.NewItems, "all feeds, quiet ones too")
	require.InDelta(t, 0.1, rateOf(t, s.ReadRate), 1e-9)

	// One feed: its own rate, over the same window.
	one, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", FeedID: 1, Now: base})
	require.NoError(t, err)
	require.Equal(t, StatsReadRate{ItemsRead: 2, NewItems: 10, Rate: one.ReadRate.Rate}, one.ReadRate)
	quiet, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", FeedID: 3, Now: base})
	require.NoError(t, err)
	require.Empty(t, quiet.Sources)
	require.Equal(t, 0.0, rateOf(t, quiet.ReadRate), "arrivals and no reads is 0%")

	// The window starts at range.from when that is later; a range before it, or only today, has none.
	s = e.summary("2026-09-12", "2026-09-24")
	require.Equal(t, "2026-09-12", *s.ReadRateFrom)
	require.Equal(t, 16, s.ReadRate.NewItems, "the 15th and 20th")
	require.Nil(t, e.summary("2026-09-01", "2026-09-04").ReadRateFrom)
	today := e.summary("2026-09-24", "2026-09-24")
	require.Nil(t, today.ReadRateFrom)
	require.Nil(t, today.ReadRate.Rate)
	// A past range ends at its own last day.
	require.Equal(t, "2026-09-20", *e.summary("2026-09-01", "2026-09-20").ReadRateTo)

	// A gap in the reads (statistics off, a delete, a restore) moves the window past it.
	e.exec(`INSERT INTO settings (key, value) VALUES ('sys.stats_gap_end', '"2026-09-21"')`)
	s = e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-22", *s.ReadRateFrom)
	require.Nil(t, s.ReadRate.Rate, "nothing arrived since the gap")
}

// Spans of one feed that overlap (a time zone change moved a day) are merged, so every id in them counts.
func TestStatsArrivalsMergeSpans(t *testing.T) {
	e := newEnv(t)
	e.putDailyNew(1, "2026-09-02", 2, 10, 30)
	e.putDailyNew(1, "2026-09-03", 2, 20, 40)
	e.putDailyNew(1, "2026-09-04", 1, 50, 50)
	e.putDailyNew(2, "2026-09-03", 1, 35, 35) // another feed's row: not read for feed 1
	m, err := statsArrivals(e.ctx, e.db.Reader(), "2026-09-01", "2026-09-30", 1)
	require.NoError(t, err)
	require.Len(t, m, 1)
	a := m[1]
	require.Equal(t, 5, a.n)
	require.Equal(t, [][2]int64{{10, 40}, {50, 50}}, a.spans)
	for id, want := range map[int64]bool{9: false, 10: true, 35: true, 40: true, 45: false, 50: true, 51: false} {
		require.Equal(t, want, a.counts(id), id)
	}
}

// The window of all feeds is one range of the table's key, which holds every column it reads; the window
// of one feed reads only that feed's rows; the first day is one seek. The fetch commit's check for an item
// between two runs reads the id range of both tables, never a feed_id-leading index, also once the
// planner has statistics.
func TestStatsArrivalsPlans(t *testing.T) {
	e := newEnv(t)
	feed := e.addFeed("http://a.example/feed")
	lo, hi := "2026-01-01", "2026-12-31"
	cases := []struct {
		q    string
		args []any
		want []string
	}{
		{sqlDailyNewFirst, nil, []string{"SEARCH feed_daily_new"}},
		{sqlDailyNew, []any{lo, hi}, []string{"SEARCH feed_daily_new USING PRIMARY KEY (local_date>? AND local_date<?)"}},
		{sqlDailyNewFeed, []any{lo, hi, 1}, []string{"SEARCH feed_daily_new USING INDEX idx_feed_daily_feed (feed_id=? AND local_date>? AND local_date<?)"}},
		{sqlItemBetween, []any{1, 2, 1}, []string{"SEARCH items USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)",
			"SEARCH trimmed_items USING INTEGER PRIMARY KEY (rowid>? AND rowid<?)"}},
	}
	plan := func(q string, args []any) string {
		var p string
		rows, err := e.db.Reader().QueryContext(e.ctx, "EXPLAIN QUERY PLAN "+q, args...)
		require.NoError(t, err)
		require.NoError(t, eachRow(rows, func() error {
			var a, b, c int
			var d string
			err := rows.Scan(&a, &b, &c, &d)
			p += d + "\n"
			return err
		}))
		return p
	}
	for _, analyzed := range []bool{false, true} {
		if analyzed {
			for f := 1; f <= 100; f++ {
				e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?, ?, ?, 1, 0, 0)`, f, feed, fmt.Sprint(f))
			}
			e.exec("ANALYZE")
		}
		for _, c := range cases {
			p := plan(c.q, c.args)
			for _, w := range c.want {
				require.Contains(t, p, w, "%s (analyzed %v)", c.q, analyzed)
			}
		}
	}
}

// The fetch commit's between check costs a day's id range, not the feed's items and ledger: a feed with
// 10,000 items and a 100,000-row ledger among 50,000 other items; see design §2.5 (0018) for the figure.
func TestFeedDailyNewBetweenPerf(t *testing.T) {
	if testing.Short() || os.Getenv("KIPPLE_PERF") == "" {
		t.Skip("seeds 160,000 rows; set KIPPLE_PERF=1")
	}
	e := newEnv(t)
	feed, other := e.addFeed("http://a.example/feed"), e.addFeed("http://b.example/feed")
	tx, err := e.db.writer.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	item, err := tx.PrepareContext(e.ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash) VALUES (?, ?, 0, ?, ?, '', '')`)
	require.NoError(t, err)
	ledger, err := tx.PrepareContext(e.ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?, ?, ?, 1, 0, 0)`)
	require.NoError(t, err)
	id := int64(1)
	for i := 0; i < 100_000; i++ {
		_, err := ledger.ExecContext(e.ctx, id, feed, fmt.Sprint("t", i))
		require.NoError(t, err)
		id++
	}
	for i := 0; i < 60_000; i++ {
		f := other
		if i%6 == 0 {
			f = feed
		}
		_, err := item.ExecContext(e.ctx, id, f, id, fmt.Sprint("i", i))
		require.NoError(t, err)
		id++
	}
	require.NoError(t, item.Close())
	require.NoError(t, ledger.Close())
	require.NoError(t, tx.Commit())
	e.exec("ANALYZE")
	// The join case: the last 1,000 ids hold none of the feed's items, so EXISTS has to rule them all out.
	e.exec(`DELETE FROM items WHERE id > ? AND feed_id = ?`, id-1000, feed)
	unforced := strings.ReplaceAll(sqlItemBetween, "+feed_id", "feed_id")
	for _, q := range []struct{ name, sql string }{{"forced id range", sqlItemBetween}, {"planner's choice", unforced}} {
		var b time.Duration
		for i := 0; i < 5; i++ {
			s := time.Now()
			var between bool
			require.NoError(t, e.db.Reader().QueryRowContext(e.ctx, q.sql, id-1000, id, feed).Scan(&between))
			require.False(t, between)
			if d := time.Since(s); i == 0 || d < b {
				b = d
			}
		}
		t.Logf("between check, %s: %v", q.name, b)
	}
}

// The summary of a feed whose window rows are many stays one pass; see design §8 Read rate for the figure.
func TestStatsReadRatePerf(t *testing.T) {
	if testing.Short() || os.Getenv("KIPPLE_PERF") == "" {
		t.Skip("seeds a million stats rows and 500,000 arrival rows; set KIPPLE_PERF=1")
	}
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	end := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	seedStats(t, e, 1_000_000, end)
	tx, err := e.db.writer.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	ins, err := tx.PrepareContext(e.ctx, `INSERT INTO feed_daily_new (local_date, feed_id, new_items, first_item, last_item) VALUES (?,?,?,?,?)`)
	require.NoError(t, err)
	id := int64(1)
	for d := 999; d >= 0; d-- {
		day := end.AddDate(0, 0, -d).Format(dateLayout)
		for f := int64(1); f <= 500; f++ {
			_, err := ins.ExecContext(e.ctx, day, f, 10, id, id+9)
			require.NoError(t, err)
			id += 10
		}
	}
	require.NoError(t, ins.Close())
	require.NoError(t, tx.Commit())
	e.exec("ANALYZE")
	best := func(f func()) time.Duration {
		var b time.Duration
		for i := 0; i < 3; i++ {
			s := time.Now()
			f()
			if d := time.Since(s); i == 0 || d < b {
				b = d
			}
		}
		return b
	}
	first := end.AddDate(0, 0, -999).Format(dateLayout)
	for _, f := range []int64{0, 7} {
		t.Logf("arrival rows of feed %d (0: all 500,000) read alone: %v", f, best(func() {
			_, err := statsArrivals(e.ctx, e.db.Reader(), first, end.Format(dateLayout), f)
			require.NoError(t, err)
		}))
	}
	for _, r := range []struct{ key, from string }{{"month", end.AddDate(0, 0, -29).Format(dateLayout)}, {"all", ""}} {
		for _, f := range []int64{0, 7} {
			t.Logf("%s summary, feed %d: %v", r.key, f, best(func() {
				_, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: r.key, From: r.from, To: end.Format(dateLayout), Now: end, FeedID: f})
				require.NoError(t, err)
			}))
		}
	}
}
