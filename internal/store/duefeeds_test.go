package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Audit L2: DueFeedsExcept leaves out held hosts and blocked ids in the query,
// so the LIMIT is spent on feeds the scheduler can start.
func TestDueFeedsExceptFiltersBeforeTheLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	held1 := e.addFeed("http://held.example/1")
	held2 := e.addFeed("http://held.example/2")
	busy := e.addFeed("http://a.example/busy")
	free := e.addFeed("http://b.example/free")
	e.exec("UPDATE feeds SET next_fetch_at = 1 WHERE id IN (?, ?)", held1, held2)
	e.exec("UPDATE feeds SET next_fetch_at = 2 WHERE id = ?", busy)
	e.exec("UPDATE feeds SET next_fetch_at = 3 WHERE id = ?", free)
	now := e.clk.Now().Add(time.Hour).Unix()
	set := e.db.FetchSettings(e.ctx)

	ids := func(hosts []string, skip []int64, limit int) []int64 {
		snaps, err := e.db.DueFeedsExcept(e.ctx, set, now, limit, hosts, skip)
		require.NoError(t, err)
		out := []int64{}
		for _, s := range snaps {
			out = append(out, s.ID)
		}
		return out
	}
	require.Equal(t, []int64{held1, held2}, ids(nil, nil, 2), "unfiltered, the oldest-due fill the page")
	require.Equal(t, []int64{busy, free}, ids([]string{"held.example"}, nil, 2))
	require.Equal(t, []int64{free}, ids([]string{"held.example"}, []int64{busy}, 2))
	require.Equal(t, []int64{held2, free}, ids(nil, []int64{held1, busy}, 2))

	plain, err := e.db.DueFeeds(e.ctx, set, now, 10)
	require.NoError(t, err)
	require.Len(t, plain, 4)
}

// EnabledFeeds lists the soonest due first, and by id among feeds due at the same time; a disabled feed is left out.
func TestEnabledFeedsAreListedDueFirst(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.addFeed("http://a.example/1")
	b := e.addFeed("http://b.example/2")
	c := e.addFeed("http://c.example/3")
	d := e.addFeed("http://d.example/4")
	off := e.addFeed("http://e.example/5")
	e.exec("UPDATE feeds SET next_fetch_at = 300 WHERE id = ?", a)
	e.exec("UPDATE feeds SET next_fetch_at = 100 WHERE id = ?", b)
	e.exec("UPDATE feeds SET next_fetch_at = 300 WHERE id = ?", c)
	e.exec("UPDATE feeds SET next_fetch_at = 200 WHERE id = ?", d)
	e.exec("UPDATE feeds SET next_fetch_at = 1, enabled = 0 WHERE id = ?", off)

	snaps, err := e.db.EnabledFeeds(e.ctx, e.db.FetchSettings(e.ctx))
	require.NoError(t, err)
	var ids []int64
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	require.Equal(t, []int64{b, d, a, c}, ids)
}
