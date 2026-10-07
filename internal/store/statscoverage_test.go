package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// covered_from skips what a comparison must not read as quiet days: before the first row, a delete, and a
// stretch with recording off; timed_from also waits for the first timed event.
func TestStatsCoverageReportsGaps(t *testing.T) {
	e := newEnv(t)
	require.Nil(t, e.summary("2026-09-01", "2026-09-24").CoveredFrom, "no rows, no history")
	e.putStat("open", "2026-09-02", 1, 1, "a", nil, "F")
	e.putStat("open", "2026-09-10", 2, 1, "b", nil, "F")
	s := e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-02", *s.CoveredFrom)
	require.Nil(t, s.TimedFrom, "nothing was ever timed")
	e.putStat("read_time", "2026-09-10", 2, 1, "b", 20, "F")
	s = e.summary("2026-09-01", "2026-09-24")
	require.NotNil(t, s.TimedFrom)
	require.GreaterOrEqual(t, *s.TimedFrom, *s.CoveredFrom)

	// Deleting a range leaves a gap through its last day.
	_, err := StatsDelete(e.ctx, e.db, "2026-09-02", "2026-09-05", nil)
	require.NoError(t, err)
	require.Equal(t, "2026-09-10", *e.summary("2026-09-01", "2026-09-24").CoveredFrom, "the first row is now the 10th")
	e.putStat("open", "2026-09-07", 3, 1, "c", nil, "F")
	require.Equal(t, "2026-09-07", *e.summary("2026-09-01", "2026-09-24").CoveredFrom, "first row is the 7th, after the gap's end")
	e.putStat("open", "2026-09-04", 4, 1, "d", nil, "F")
	require.Equal(t, "2026-09-06", *e.summary("2026-09-01", "2026-09-24").CoveredFrom, "a row inside the gap does not reopen it")

	// A delete that removes nothing is not a gap.
	_, err = StatsDelete(e.ctx, e.db, "2026-09-20", "2026-09-22", nil)
	require.NoError(t, err)
	require.Equal(t, "2026-09-06", *e.summary("2026-09-01", "2026-09-24").CoveredFrom)

	// Recording off then on: the days between are a gap through the day it came back on (today, the 24th).
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"stats.enabled": false}))
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"stats.enabled": true}))
	require.Equal(t, "2026-09-25", *e.summary("2026-09-01", "2026-09-24").CoveredFrom)

	// Turning it on while already on records nothing, and a gap never moves back.
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"stats.enabled": true}))
	_, err = StatsDelete(e.ctx, e.db, "2026-09-01", "2026-09-04", nil)
	require.NoError(t, err)
	require.Equal(t, "2026-09-25", *e.summary("2026-09-01", "2026-09-24").CoveredFrom)
}

// A gap never reaches past today: deleting every row, or a range that ends in the future, must leave
// the marker where a later gap can still move it.
func TestStatsDeleteGapStopsAtToday(t *testing.T) {
	e := newEnv(t) // today is 2026-09-24
	marker := func(day string) int {
		return e.count(`SELECT count(*) FROM settings WHERE key = 'sys.stats_gap_end' AND value = ?`, `"`+day+`"`)
	}
	e.putStat("open", "2026-09-02", 1, 1, "a", nil, "F")
	_, err := StatsDelete(e.ctx, e.db, "", "", nil) // delete all
	require.NoError(t, err)
	require.Equal(t, 1, marker("2026-09-24"))
	e.putStat("open", "2026-09-30", 2, 1, "b", nil, "F")
	require.Equal(t, "2026-09-30", *e.summary("2026-09-01", "2026-09-24").CoveredFrom)

	e.putStat("open", "2026-09-05", 3, 1, "c", nil, "F")
	_, err = StatsDelete(e.ctx, e.db, "2026-01-01", "2026-12-31", nil) // a range that ends in the future
	require.NoError(t, err)
	require.Equal(t, 1, marker("2026-09-24"))

	// A later off and on still moves it.
	e.clk.Advance(48 * time.Hour)
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"stats.enabled": false}))
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"stats.enabled": true}))
	require.Equal(t, 1, marker("2026-09-26"))
}
