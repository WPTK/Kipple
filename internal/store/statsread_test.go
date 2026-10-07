package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The read rule (design §8, issue #120): read time >= 10 s, or a scroll >= 25 with read time >= 3 s.
// A scroll alone is never a read.
func TestStatsIsRead(t *testing.T) {
	for _, tc := range []struct {
		legacy       bool
		secs, scroll int64
		want         bool
	}{
		{false, 2, 100, false}, // 2.9 s arrives as 2 (ingest truncates): a full scroll is not enough
		{false, 0, 100, false}, // a flick with no time at all
		{false, 3, 25, true},   // both floors exactly
		{false, 3, 24, false},  // 3 s needs the scroll
		{false, 10, 0, true},   // time alone
		{false, 9, 24, false},  // just under both
		{false, 9, 25, true},   // under 10 s but scrolled with more than 3 s
		{false, 2, 25, false},  // scrolled, under 3 s
		{true, 0, 0, true},     // legacy: unknowable, counted
	} {
		require.Equal(t, tc.want, statsIsRead(tc.legacy, tc.secs, tc.scroll), "%+v", tc)
	}
}

// readCase is one session on its own date: its read_time slices and scroll (-1 for no scroll row).
type readCase struct {
	date   string
	slices []int
	scroll int
}

func (c readCase) total() int64 {
	var n int64
	for _, s := range c.slices {
		n += int64(s)
	}
	return n
}

func (e *env) putCase(i int, c readCase) {
	e.t.Helper()
	sk := fmt.Sprintf("s%d", i)
	item := int64(100 + i)
	e.putStat("open", c.date, item, 1, sk, nil, "F")
	for _, v := range c.slices {
		e.putStat("read_time", c.date, item, 1, sk, v, "F")
	}
	if c.scroll >= 0 {
		e.putStat("scroll", c.date, item, 1, sk, c.scroll, "F")
	}
}

// streakDates runs sqlStreaks itself and returns every date it counts, so each boundary case can be
// checked on its own (the streak numbers alone would hide which dates qualified).
func (e *env) streakDates(cut int64) map[string]bool {
	e.t.Helper()
	rows, err := e.db.Reader().QueryContext(e.ctx, sqlStreaks, cut, 0, StatsReadScroll, StatsReadSeconds, int64(1)<<40, StatsReadScrollSeconds)
	require.NoError(e.t, err)
	got := map[string]bool{}
	require.NoError(e.t, eachRow(rows, func() error {
		var d string
		if err := rows.Scan(&d); err != nil {
			return err
		}
		got[d] = true
		return nil
	}))
	return got
}

// The summary's classification and the streak query's SQL apply the same rule: every combination
// of read time and scroll around the thresholds lands on the same side in both.
func TestStatsReadRuleMatchesStreaks(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-01-01", 1, 1, "legacy", nil, "F")    // before any timed row: legacy
	e.putStat("read_time", "2026-01-02", 1, 1, "legacy", 1, "F") // the first timed row: every case below is tracked
	var cases []readCase
	d := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	for _, secs := range [][]int{nil, {2}, {1, 1}, {3}, {1, 2}, {9}, {10}, {4, 6}, {11}} {
		for _, sc := range []int{-1, 0, 24, 25, 100} {
			cases = append(cases, readCase{date: d.Format(dateLayout), slices: secs, scroll: sc})
			d = d.AddDate(0, 0, 2) // a gap day between cases, so the dates stay apart
		}
	}
	for i, c := range cases {
		e.putCase(i, c)
	}
	cut, err := statsLegacyCutoff(e.ctx, e.db.Reader(), int64(1)<<40)
	require.NoError(t, err)
	streak := e.streakDates(cut)
	require.True(t, streak["2026-01-01"], "the legacy open counts")

	out := e.summaryAt(cases[0].date, cases[len(cases)-1].date, time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC))
	byDate := map[string]int{}
	for _, dd := range out.Daily {
		byDate[dd.Date] = dd.ItemsRead
	}
	reads := 0
	for _, c := range cases {
		sc := int64(c.scroll)
		if sc < 0 {
			sc = 0
		}
		want := statsIsRead(false, c.total(), sc)
		if want {
			reads++
		}
		require.Equal(t, want, streak[c.date], "streak for %+v", c)
		require.Equal(t, want, byDate[c.date] == 1, "summary for %+v", c)
	}
	require.Equal(t, reads, out.Totals.ItemsRead)
	require.Equal(t, 0, out.Totals.LegacyOpens, "the legacy open is outside this range")
	require.Equal(t, len(cases), out.Totals.Opens)
	var bounces, tracked int
	for _, s := range out.Sources {
		bounces += s.Bounces
		tracked += s.TrackedOpens
	}
	require.Equal(t, len(cases)-reads, bounces)
	require.Equal(t, len(cases), tracked)
}

func (e *env) summaryAt(from, to string, now time.Time) *StatsSummary {
	e.t.Helper()
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: from, To: to, Now: now})
	require.NoError(e.t, err)
	return out
}

// The owner's boundary cases, through the whole summary and the streaks.
func TestStatsReadBoundaries(t *testing.T) {
	e := newEnv(t)                                         // today is 2026-09-24 in UTC
	e.putStat("open", "2026-09-01", 1, 1, "old", nil, "F") // legacy: before the first timed row
	e.putCase(1, readCase{"2026-09-10", []int{2}, 100})    // 2.9 s (stored as 2) + 100 %: bounce
	e.putCase(2, readCase{"2026-09-11", []int{3}, 25})     // 3 s + 25 %: read
	e.putCase(3, readCase{"2026-09-12", []int{10}, 0})     // 10 s + 0 %: read
	e.putCase(4, readCase{"2026-09-13", []int{9}, 24})     // 9 s + 24 %: bounce
	e.putCase(5, readCase{"2026-09-14", nil, 100})         // a flick, no time: bounce
	e.putCase(6, readCase{"2026-09-15", []int{1, 2}, 30})  // 3 s over two slices + 30 %: read

	out := e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, 7, out.Totals.Opens)
	require.Equal(t, 4, out.Totals.ItemsRead, "legacy, 3 s + 25, 10 s, 1+2 s + 30")
	require.Equal(t, 4, out.Totals.DaysActive)
	require.Equal(t, 1, out.Totals.LegacyOpens)
	require.EqualValues(t, 2+3+10+9+3, out.Totals.ActiveSeconds, "active time counts every session, read or not")
	byDate := map[string]int{}
	for _, d := range out.Daily {
		byDate[d.Date] = d.ItemsRead
	}
	for d, want := range map[string]int{"2026-09-01": 1, "2026-09-10": 0, "2026-09-11": 1, "2026-09-12": 1, "2026-09-13": 0, "2026-09-14": 0, "2026-09-15": 1} {
		require.Equal(t, want, byDate[d], d)
	}
	require.Len(t, out.Sources, 1)
	s := out.Sources[0]
	require.Equal(t, 6, s.TrackedOpens, "the legacy open is not tracked")
	require.Equal(t, 3, s.Bounces)
	require.InDelta(t, 0.5, *s.BounceRate, 1e-9)
	require.EqualValues(t, 3+10+3, s.TimedSeconds, "only read opens with time")
	require.Equal(t, 3, s.TimedItems)
	require.NotNil(t, out.Behavior.LongestRead)
	require.EqualValues(t, 10, out.Behavior.LongestRead.Seconds, "the 9 s bounce is not a read, so not the longest read")

	// Streaks: 01, 11, 12, 15 are read days; the bounces on 10, 13 and 14 do not join them.
	st := out.Streaks
	require.Equal(t, 2, st.Longest)
	require.Equal(t, "2026-09-12", *st.LongestEnd)
	require.Equal(t, 0, st.Current)

	// Turning the 13th into a read (one more second makes 10 s) joins 11..13 into a run of three.
	e.putStat("read_time", "2026-09-13", 104, 1, "s4", 1, "F")
	st = e.summary("2026-09-01", "2026-09-24").Streaks
	require.Equal(t, 3, st.Longest)
	require.Equal(t, "2026-09-13", *st.LongestEnd)
}

// A session split across two local dates (an article left open over midnight): the summary counts
// only the part inside the range, the streaks count the whole session on the open's date.
func TestStatsReadSplitSessions(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-01", 1, 1, "old", nil, "F") // legacy, earlier than every timed row below
	// A: open on the 20th with 2 s, 8 s more after midnight: 10 s in total.
	e.putStat("open", "2026-09-20", 10, 1, "A", nil, "F")
	e.putStat("read_time", "2026-09-20", 10, 1, "A", 2, "F")
	e.putStat("read_time", "2026-09-21", 10, 1, "A", 8, "F")
	// B: open on the 22nd, scrolled 25 % that day, 3 s of time the next day.
	e.putStat("open", "2026-09-22", 11, 1, "B", nil, "F")
	e.putStat("scroll", "2026-09-22", 11, 1, "B", 25, "F")
	e.putStat("read_time", "2026-09-23", 11, 1, "B", 3, "F")

	one := e.summary("2026-09-20", "2026-09-20")
	require.Equal(t, 1, one.Totals.Opens)
	require.Equal(t, 0, one.Totals.ItemsRead, "2 s inside the range is a bounce")
	both := e.summary("2026-09-20", "2026-09-21")
	require.Equal(t, 1, both.Totals.ItemsRead, "10 s once the range covers both days")

	b1 := e.summary("2026-09-22", "2026-09-22")
	require.Equal(t, 0, b1.Totals.ItemsRead, "a 25 % scroll with no time in range is a bounce")
	b2 := e.summary("2026-09-22", "2026-09-23")
	require.Equal(t, 1, b2.Totals.ItemsRead, "the scroll and 3 s together")

	// A time slice alone in the range (its open is outside) is active time but no open and no read.
	tail := e.summary("2026-09-21", "2026-09-21")
	require.Equal(t, 0, tail.Totals.Opens)
	require.Equal(t, 0, tail.Totals.ItemsRead)
	require.EqualValues(t, 8, tail.Totals.ActiveSeconds)

	// Streaks are all time and use the whole session: both opens' dates count.
	dates := e.streakDates(mustCut(t, e))
	require.True(t, dates["2026-09-20"])
	require.True(t, dates["2026-09-22"])
	require.False(t, dates["2026-09-21"], "no open that day")
	require.False(t, dates["2026-09-23"], "no open that day")
}

func mustCut(t *testing.T, e *env) int64 {
	t.Helper()
	cut, err := statsLegacyCutoff(e.ctx, e.db.Reader(), int64(1)<<40)
	require.NoError(t, err)
	return cut
}

// Range edges: an open on the first and on the last day of a range is in it; a day outside is not.
func TestStatsReadRangeEdges(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-01", 1, 1, "old", nil, "F")
	e.putCase(1, readCase{"2026-09-09", []int{10}, -1}) // the day before the range
	e.putCase(2, readCase{"2026-09-10", []int{3}, 25})  // first day: read
	e.putCase(3, readCase{"2026-09-17", []int{2}, 25})  // last day: bounce
	e.putCase(4, readCase{"2026-09-18", []int{10}, -1}) // the day after
	out := e.summary("2026-09-10", "2026-09-17")
	require.Equal(t, 2, out.Totals.Opens)
	require.Equal(t, 1, out.Totals.ItemsRead)
	require.Equal(t, 1, out.Totals.DaysActive)
	require.Equal(t, 0, out.Totals.LegacyOpens)
	require.Equal(t, "2026-09-10", out.Daily[0].Date)
	require.Equal(t, 1, out.Daily[0].ItemsRead)
	require.Equal(t, "2026-09-17", out.Daily[len(out.Daily)-1].Date)
	require.Equal(t, 0, out.Daily[len(out.Daily)-1].ItemsRead)
	require.Equal(t, 1, out.Sources[0].Bounces)

	// The legacy open is counted, and reported, only when it is in the range.
	all := e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, 1, all.Totals.LegacyOpens)
	require.Equal(t, 4, all.Totals.ItemsRead, "legacy, the 9th, the 10th, the 18th")
}

// The summary queries stay on indexes: no plan step scans stats_events (or its aliases) without one.
func TestStatsSummaryPlansHaveNoTableScan(t *testing.T) {
	e := newEnv(t)
	for _, h := range statsHinted() {
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+h.sql, h.args...)
		require.NoError(t, err, h.name)
		require.NoError(t, eachRow(rows, func() error {
			var a, b, c int
			var d string
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				return err
			}
			for _, name := range []string{"stats_events", "e", "r"} {
				if d == "SCAN "+name {
					t.Errorf("%s: unindexed scan %q", h.name, d)
				}
			}
			return nil
		}))
	}
}
