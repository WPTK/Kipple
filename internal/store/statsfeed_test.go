package store

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A summary of one feed counts exactly what a database holding only that feed's rows would, by the
// same rules; the other feed's rows are left out of every count, the streaks included.
func TestStatsSummaryOneFeed(t *testing.T) {
	t.Parallel()
	rows := func(e *env, withOther bool) {
		e.putStat("open", "2026-09-10", 1, 1, "a", nil, "One") // legacy: before the first read time
		e.putStat("open", "2026-09-20", 2, 1, "b", nil, "One")
		e.putStat("read_time", "2026-09-20", 2, 1, "b", 30, "One")
		e.putStat("open", "2026-09-21", 3, 1, "c", nil, "One")
		e.putStat("read_time", "2026-09-21", 3, 1, "c", 2, "One") // a bounce
		e.putStat("star", "2026-09-21", 3, 1, "", nil, "One")
		if withOther {
			e.putStat("open", "2026-09-22", 9, 2, "x", nil, "Two")
			e.putStat("read_time", "2026-09-22", 9, 2, "x", 600, "Two")
			e.putStat("open", "2026-09-23", 8, 2, "y", nil, "Two")
			e.putStat("read_time", "2026-09-23", 8, 2, "y", 60, "Two")
			e.putStat("star", "2026-09-23", 8, 2, "", nil, "Two")
			e.putStat("open_original", "2026-09-23", 8, 2, "", nil, "Two")
		}
	}
	both, only := newEnv(t), newEnv(t)
	rows(both, true)
	rows(only, false)
	both.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	got, err := StatsSummaryFor(both.ctx, both.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base, FeedID: 1})
	require.NoError(t, err)
	want := only.summary("2026-09-01", "2026-09-24")
	require.Equal(t, want.Totals, got.Totals)
	require.Equal(t, want.Daily, got.Daily)
	require.Nil(t, got.Streaks, "streaks are all feeds; one feed's summary leaves them out")
	require.Equal(t, want.Heatmap, got.Heatmap)
	require.Equal(t, want.Behavior, got.Behavior)
	require.Equal(t, want.Sources, got.Sources)
	require.Len(t, got.Sources, 1)
	require.Equal(t, 2, got.Totals.ItemsRead, "the legacy open and the 30 s read")
	// The feed's rows start with the first row of all, so its coverage is that of all statistics.
	all := both.summary("2026-09-01", "2026-09-24")
	require.Equal(t, all.CoveredFrom, got.CoveredFrom)
	require.Equal(t, all.TimedFrom, got.TimedFrom)
	require.NotNil(t, all.Streaks)
}

// A feed subscribed after statistics began is covered only from its subscription day: the days
// before it are not zeros, so a comparison reaching back past it has too little history.
func TestStatsSummaryOneFeedCoverageStartsWithTheFeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	old := e.addFeed("https://old.example/feed")
	young := e.addFeed("https://young.example/feed")
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Unix(), old)
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC).Unix(), young)
	e.putStat("open", "2026-08-01", 1, old, "a", nil, "Old")
	e.putStat("read_time", "2026-08-01", 1, old, "a", 30, "Old")
	e.putStat("open", "2026-09-20", 2, young, "b", nil, "Young")
	sum := func(f int64) *StatsSummary {
		out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base, FeedID: f})
		require.NoError(t, err)
		return out
	}
	all := e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-08-01", *all.CoveredFrom)
	require.Equal(t, "2026-08-01", *sum(old).CoveredFrom, "subscribed before the first row: the global start")
	y := sum(young)
	require.Equal(t, "2026-09-14", *y.CoveredFrom, "its subscription day")
	require.Equal(t, "2026-09-14", *y.TimedFrom)

	// A feed whose row is gone starts with its first row.
	e.exec("DELETE FROM feeds WHERE id = ?", young)
	require.Equal(t, "2026-09-20", *sum(young).CoveredFrom)
}

// A feed's never_opened lists only that feed, and only when it had no open in the range.
func TestStatsSummaryOneFeedNeverOpened(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	f1 := e.addFeed("https://one.example/feed")
	f2 := e.addFeed("https://two.example/feed")
	e.exec("UPDATE feeds SET created_at = ?", base.Add(-30*24*time.Hour).Unix())
	e.putStat("open", "2026-09-20", 1, f1, "a", nil, "One")
	sum := func(f int64) *StatsSummary {
		out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base, FeedID: f})
		require.NoError(t, err)
		return out
	}
	require.Empty(t, sum(f1).NeverOpened)
	n := sum(f2).NeverOpened
	require.Len(t, n, 1)
	require.Equal(t, strconv.FormatInt(f2, 10), n[0].FeedID)
	require.Empty(t, sum(f2).Sources)
}

// The feed's start only ever moves coverage forward: a later global gap or a later first read time
// still wins, and a subscribed feed with no rows starts on its subscription day.
func TestStatsSummaryOneFeedCoverageEdges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	old := e.addFeed("https://old.example/feed")
	young := e.addFeed("https://young.example/feed")
	quiet := e.addFeed("https://quiet.example/feed")
	at := func(y, m, d int) int64 { return time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC).Unix() }
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", at(2026, 6, 1), old)
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", at(2026, 9, 10), young)
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", at(2026, 9, 12), quiet)
	e.putStat("open", "2026-08-01", 1, old, "a", nil, "Old")     // legacy: nothing timed yet
	e.putStat("open", "2026-09-18", 2, old, "b", nil, "Old")     // the first timed session
	e.putStat("read_time", "2026-09-18", 2, old, "b", 30, "Old") // timed_from: the 19th
	e.putStat("open", "2026-09-20", 3, young, "c", nil, "Young")
	sum := func(f int64) *StatsSummary {
		out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base, FeedID: f})
		require.NoError(t, err)
		return out
	}

	y := sum(young)
	require.Equal(t, "2026-09-10", *y.CoveredFrom, "the feed started after the first row")
	require.Equal(t, "2026-09-19", *y.TimedFrom, "the first read time is later than the feed's start, so it wins")

	q := sum(quiet)
	require.Equal(t, "2026-09-12", *q.CoveredFrom, "no rows: its subscription day")
	require.Equal(t, "2026-09-19", *q.TimedFrom)
	require.Zero(t, q.Totals.Opens)

	// A global gap after the young feed's start: coverage resumes the day after the gap for it too.
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, '\"2026-09-15\"')", SettingStatsGapEnd)
	y = sum(young)
	require.Equal(t, "2026-09-16", *y.CoveredFrom)
	require.Equal(t, "2026-09-19", *y.TimedFrom)
	require.Equal(t, "2026-09-16", *sum(old).CoveredFrom)
}

// The feed's start date takes seven index seeks, however many rows the feed holds. Run with
// KIPPLE_PERF=1 to time it, and one feed's summary against all feeds', on a feed of 100,000 rows.
func TestStatsFeedStartPerf(t *testing.T) {
	t.Parallel()
	if testing.Short() || os.Getenv("KIPPLE_PERF") == "" {
		t.Skip("seeds a million stats rows; set KIPPLE_PERF=1")
	}
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	end := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	seedStats(t, e, 1_000_000, end)
	const big = 9999
	tx, err := e.db.writer.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	for i := 0; i < 100_000; i++ {
		ts := end.Unix() - int64(i)*300
		lt := time.Unix(ts, 0).UTC()
		kind := "read_time"
		if i%10 == 0 {
			kind = "open"
		}
		_, err := tx.ExecContext(e.ctx, `INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
			value, session_key) VALUES (?,?,?,?,?, 'web', ?, ?, 'Big', 10, ?)`, ts, lt.Format(dateLayout), lt.Hour(), int(lt.Weekday()), kind, 1_000_000+i/10, big, fmt.Sprintf("big%d", i/10))
		require.NoError(t, err)
	}
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
	var maxID int64
	require.NoError(t, e.db.Reader().QueryRowContext(e.ctx, "SELECT MAX(id) FROM stats_events").Scan(&maxID))
	var start string
	t.Logf("feed start, seeks: %v", best(func() {
		var err error
		start, err = statsFeedStart(e.ctx, e.db.Reader(), time.UTC, big, maxID)
		require.NoError(t, err)
	}))
	t.Logf("feed start, MIN(local_date) scan of the feed's rows: %v", best(func() {
		var d string
		require.NoError(t, e.db.Reader().QueryRowContext(e.ctx, "SELECT MIN(local_date) FROM stats_events INDEXED BY idx_stats_feed WHERE feed_id = ? AND id <= ?", big, maxID).Scan(&d))
		require.Equal(t, start, d)
	}))
	from := end.AddDate(0, 0, -29).Format(dateLayout)
	for _, f := range []int64{0, big} {
		t.Logf("month summary, feed %d: %v", f, best(func() {
			_, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "month", From: from, To: end.Format(dateLayout), Now: end, FeedID: f})
			require.NoError(t, err)
		}))
	}
}
