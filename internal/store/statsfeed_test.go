package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A summary of one feed counts exactly what a database holding only that feed's rows would, by the
// same rules; the other feed's rows are left out of every count, the streaks included.
func TestStatsSummaryOneFeed(t *testing.T) {
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
