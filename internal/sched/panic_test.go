package sched

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// A panic in the fetch path (a parser bug on a hostile feed) becomes a logged
// parse error for that feed, with the normal backoff; the server and the other
// feeds carry on and the dispatcher's counters stay balanced.
func TestPanicInFetchBecomesParseError(t *testing.T) {
	r := newRig(t, Options{Workers: 1})
	srv := newSrv(t, serveOK)
	bad := r.add(srv.URL+"/bad", nil)
	r.s.inDispatcher(func() {
		r.s.fetchFn = func(ctx context.Context, snap fetch.Snapshot, now time.Time) *fetch.Result {
			if snap.ID == bad {
				panic("parser exploded")
			}
			return r.s.client.Fetch(ctx, snap, now)
		}
	})
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "error", ev["outcome"])
	require.Equal(t, fetch.ClassParse, ev["error_class"])
	require.EqualValues(t, 1, r.failures(bad))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND outcome = 'error' AND error_class = 'parse'", bad))
	require.Greater(t, r.next(bad), r.clk.Now().Unix(), "backed off like any failing feed")

	// The worker survived and its slot was given back: another feed still fetches.
	good := r.add(srv.URL+"/good", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 2)
	require.Equal(t, "ok", r.events("fetch.done")[1]["outcome"])
	var running, flights int
	r.s.inDispatcher(func() { running, flights = r.s.running, len(r.s.flights) })
	require.Zero(t, running)
	require.Zero(t, flights)
	require.EqualValues(t, 0, r.failures(good))
}

// A panic during the commit leaves the stored state unknown: the result is a
// failed commit, which the dispatcher backs off in memory.
func TestPanicInCommitBacksOffInMemory(t *testing.T) {
	r := newRig(t, Options{Workers: 1})
	r.s.inDispatcher(func() {
		r.s.commitFetchFn = func(context.Context, *fetch.Result, time.Duration) (store.CommitInfo, error) {
			panic("store exploded")
		}
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "error", ev["outcome"])
	require.Equal(t, "internal", ev["error_class"])
	var nb time.Time
	var running int
	r.s.inDispatcher(func() { nb, running = r.s.notBefore[id], r.s.running })
	require.True(t, nb.After(r.clk.Now()), "not redispatched every tick")
	require.Zero(t, running)
}
