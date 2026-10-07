package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func (e *env) putDailyNew(feed int64, date string, n int) {
	e.t.Helper()
	e.exec(`INSERT INTO feed_daily_new (feed_id, local_date, new_items) VALUES (?,?,?)`, feed, date, n)
}

// read puts one open of item with enough read time to count as read.
func (e *env) read(date string, item, feed int64) {
	e.t.Helper()
	s := "s" + date + string(rune('a'+item))
	e.putStat("open", date, item, feed, s, nil, "F")
	e.putStat("read_time", date, item, feed, s, 20, "F")
}

func rateOf(t *testing.T, r StatsReadRate) float64 {
	t.Helper()
	require.NotNil(t, r.Rate)
	return *r.Rate
}

// The read rate divides the items read by the counted arrivals over one window: the range, from the day
// reads are complete (covered_from) and the day after arrivals were first counted. No window or nothing
// new is no data (nil), never 0%.
func TestStatsReadRate(t *testing.T) {
	e := newEnv(t) // today is 2026-09-24

	e.read("2026-09-10", 1, 1)
	s := e.summary("2026-09-01", "2026-09-24")
	require.Nil(t, s.ReadRateFrom, "no arrival was ever counted")
	require.Nil(t, s.ReadRate.Rate)
	require.Nil(t, s.Sources[0].ReadRate.Rate)

	// Arrivals were first counted on the 1st (a day that may be partial); reads are complete from the 5th.
	e.putStat("open", "2026-09-05", 9, 2, "early", nil, "F")
	e.putDailyNew(1, "2026-09-01", 100) // before the window
	e.putDailyNew(1, "2026-09-10", 4)
	e.putDailyNew(1, "2026-09-20", 6)
	e.putDailyNew(3, "2026-09-15", 10) // a feed with arrivals and no activity
	e.putStat("open", "2026-09-11", 2, 1, "bounce", nil, "F")
	e.putStat("read_time", "2026-09-11", 2, 1, "bounce", 2, "F")
	e.read("2026-09-12", 3, 1)

	s = e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-05", *s.ReadRateFrom)
	src := map[string]StatsSource{}
	for _, x := range s.Sources {
		src[x.FeedID] = x
	}
	require.Equal(t, 2, src["1"].ReadRate.ItemsRead, "a bounce is not a read")
	require.Equal(t, 10, src["1"].ReadRate.NewItems, "the first day's arrivals are left out")
	require.InDelta(t, 0.2, rateOf(t, src["1"].ReadRate), 1e-9)
	require.Equal(t, 1, src["2"].ReadRate.ItemsRead, "the legacy open counts as read")
	require.Nil(t, src["2"].ReadRate.Rate, "nothing new arrived: a dash, not a percentage")
	require.Equal(t, StatsReadRate{ItemsRead: 3, NewItems: 20, Rate: s.ReadRate.Rate}, s.ReadRate, "all feeds, quiet ones too")
	require.InDelta(t, 0.15, rateOf(t, s.ReadRate), 1e-9)

	// One feed: its own rate, over the same window.
	one, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", FeedID: 1, Now: base})
	require.NoError(t, err)
	require.Equal(t, 2, one.ReadRate.ItemsRead)
	require.Equal(t, 10, one.ReadRate.NewItems)
	quiet, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", FeedID: 3, Now: base})
	require.NoError(t, err)
	require.Empty(t, quiet.Sources)
	require.Equal(t, 0.0, rateOf(t, quiet.ReadRate), "arrivals and no reads is 0%")

	// The window starts at range.from when that is later, and a range before it has no window.
	s = e.summary("2026-09-12", "2026-09-24")
	require.Equal(t, "2026-09-12", *s.ReadRateFrom)
	require.Equal(t, 16, s.ReadRate.NewItems, "the 15th and 20th")
	require.Nil(t, e.summary("2026-09-01", "2026-09-04").ReadRateFrom)

	// More read than arrived in the window (older items): the counts stay exact, the rate stops at 1.
	e.putDailyNew(4, "2026-09-22", 1)
	e.read("2026-09-22", 5, 4)
	e.read("2026-09-22", 6, 4)
	for _, x := range e.summary("2026-09-01", "2026-09-24").Sources {
		if x.FeedID == "4" {
			require.Equal(t, StatsReadRate{ItemsRead: 2, NewItems: 1, Rate: x.ReadRate.Rate}, x.ReadRate)
			require.Equal(t, 1.0, rateOf(t, x.ReadRate))
		}
	}

	// A gap in the reads (statistics off, a delete, a restore) moves the window past it.
	e.exec(`INSERT INTO settings (key, value) VALUES ('sys.stats_gap_end', '"2026-09-22"')`)
	s = e.summary("2026-09-01", "2026-09-24")
	require.Equal(t, "2026-09-23", *s.ReadRateFrom)
	require.Nil(t, s.ReadRate.Rate, "nothing arrived since the gap")
}
