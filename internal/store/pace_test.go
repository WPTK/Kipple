package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// base is 2026-09-24 12:00 UTC: today is the 24th, the last complete day the 23rd, and a 28-day
// window runs 2026-08-27 to 2026-09-23.

// ago is the local date n days before the base day.
func ago(n int) string { return base.AddDate(0, 0, -n).Format("2006-01-02") }

// paceFeed adds a feed subscribed days days before the base time.
func (e *env) paceFeed(url string, days int) int64 {
	id := e.addFeed(url)
	e.exec("UPDATE feeds SET created_at = ? WHERE id = ?", base.AddDate(0, 0, -days).Unix(), id)
	return id
}

// perDay writes n items on each of the days from..to days ago (from >= to).
func (e *env) perDay(feed int64, from, to, n int) {
	for d := from; d >= to; d-- {
		e.putDailyNew(feed, ago(d), n, int64(d)*10+1, int64(d)*10+int64(n))
	}
}

func (e *env) paces() map[int64]float64 {
	e.t.Helper()
	p, err := e.db.FeedPaces(e.ctx, e.clk.Now())
	require.NoError(e.t, err)
	return p
}

func TestFeedPacesEmptyTableHasNone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.paceFeed("http://a.example/feed", 60)
	require.Empty(t, e.paces(), "nothing counted yet: no feed has a pace")
}

// A feed with no new items over counted days has a pace of zero, not none.
func TestFeedPacesZeroDays(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	quiet := e.paceFeed("http://quiet.example/feed", 60)
	busy := e.paceFeed("http://busy.example/feed", 60)
	e.perDay(busy, 28, 1, 2)
	p := e.paces()
	require.Contains(t, p, quiet)
	require.Zero(t, p[quiet])
	require.InDelta(t, 2, p[busy], 1e-9)
}

func TestFeedPacesLeavesTodayOut(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.paceFeed("http://a.example/feed", 60)
	e.perDay(id, 28, 1, 1)
	e.putDailyNew(id, ago(0), 500, 9001, 9500)
	require.InDelta(t, 1, e.paces()[id], 1e-9, "today is not a complete day")
}

func TestFeedPacesNeedsSevenCountedDays(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := e.paceFeed("http://old.example/feed", 60)
	e.perDay(old, 28, 1, 1)

	// Subscribed on the 21st: counted days are the 22nd and 23rd.
	young := e.paceFeed("http://young.example/feed", 3)
	e.perDay(young, 2, 1, 5)
	require.NotContains(t, e.paces(), young)

	// Subscribed 8 days ago: the 7 days after it are enough; 7 days ago leaves only 6.
	eight := e.paceFeed("http://eight.example/feed", 8)
	seven := e.paceFeed("http://seven.example/feed", 7)
	e.perDay(eight, 7, 1, 1)
	e.perDay(seven, 6, 1, 1)
	p := e.paces()
	require.InDelta(t, 1, p[eight], 1e-9)
	require.NotContains(t, p, seven)
	require.Contains(t, p, old)
}

func TestFeedPacesCountingBeganRecently(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.paceFeed("http://a.example/feed", 90)
	e.perDay(id, 5, 1, 3) // the first row is 5 days back: counting began then
	require.NotContains(t, e.paces(), id, "5 counted days are too few")

	e.perDay(id, 7, 6, 3) // two more days earlier: 7 days
	require.InDelta(t, 3, e.paces()[id], 1e-9)
}

func TestFeedPacesCapsAt28Days(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.paceFeed("http://a.example/feed", 200)
	e.perDay(id, 60, 29, 100) // outside the window
	e.perDay(id, 28, 1, 2)
	require.InDelta(t, 2, e.paces()[id], 1e-9, "only the last 28 complete days count")
}

// The day boundary is the account's time zone: at 12:00 UTC it is already the 25th in Auckland, so
// the 24th is a complete day there and today in UTC.
func TestFeedPacesFollowTheTimeZone(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.paceFeed("http://a.example/feed", 90)
	e.perDay(id, 28, 1, 1)
	e.putDailyNew(id, ago(0), 28, 9001, 9028)
	require.InDelta(t, 1, e.paces()[id], 1e-9, "UTC: the 24th is today")

	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{"tz": "Pacific/Auckland"}))
	// Window 2026-08-28..2026-09-24: the 28 items of the 24th join, the items of 08-27 drop out.
	require.InDelta(t, float64(27+28)/28, e.paces()[id], 1e-9)
}

func TestFeedPacesLeavesOutTheArchiveFeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.paceFeed("http://a.example/feed", 90)
	arch := e.paceFeed("http://arch.example/feed", 90)
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive' WHERE id = ?", arch)
	e.perDay(id, 28, 1, 1)
	e.perDay(arch, 28, 1, 1)
	p := e.paces()
	require.Contains(t, p, id)
	require.NotContains(t, p, arch)
}

// UIFeeds carries the pace on each listed feed: null until it has one.
func TestUIFeedsCarryThePace(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := e.paceFeed("http://old.example/feed", 90)
	young := e.paceFeed("http://young.example/feed", 2)
	e.perDay(old, 28, 1, 4)
	feeds, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.NoError(t, err)
	got := map[int64]*float64{}
	for _, f := range feeds {
		got[f.ID] = f.Pace
	}
	require.NotNil(t, got[old], fmt.Sprint(got))
	require.InDelta(t, 4, *got[old], 1e-9)
	require.Nil(t, got[young])
}
