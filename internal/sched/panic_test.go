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
	t.Parallel()
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
	t.Parallel()
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

// Audit L4: a panic after the commit succeeded (queueing extraction, say) leaves
// a written schedule and a healthy feed: no in-memory backoff, and the result
// keeps what was committed.
func TestPanicAfterCommitDoesNotBackOff(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{Workers: 1})
	r.s.inDispatcher(func() {
		r.s.afterCommit = func(int64) { panic("after the commit") }
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "ok", ev["outcome"])
	require.EqualValues(t, 2, ev["new_items"], "the committed items are reported")
	require.Greater(t, r.next(id), r.clk.Now().Unix(), "the committed schedule stands")
	var backedOff bool
	var fails, running int
	r.s.inDispatcher(func() {
		_, backedOff = r.s.notBefore[id]
		fails, running = r.s.commitFails[id], r.s.running
	})
	require.False(t, backedOff, "a healthy feed is not backed off")
	require.Zero(t, fails)
	require.Zero(t, running)
}

// Audit L4: a panicking trim job writes no schedule, so it must not back off the
// feed's fetches either.
func TestPanicInTrimDoesNotBackOff(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{Workers: 1})
	r.s.inDispatcher(func() {
		r.s.trimFn = func(context.Context, int64, store.TrimBudget) (int64, bool, error) { panic("trim exploded") }
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })
	reply, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	rep := <-reply
	require.Equal(t, fetch.OutcomeError, rep.Outcome)
	var backedOff bool
	r.s.inDispatcher(func() { _, backedOff = r.s.notBefore[id] })
	require.False(t, backedOff)
}

// A panicking skip job is not a failed commit either (no fetch schedule).
func TestRecoveredSkipIsNotAFailedCommit(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	f := &flight{kind: kindSkip, snap: fetch.Snapshot{ID: 7, Host: "h.test"}}
	out := r.s.recovered(f, "boom", nil, phaseFetch, result{})
	require.False(t, out.commitFailed)
	require.Equal(t, fetch.OutcomeError, out.outcome)
	// A fetch panicking mid-commit still is.
	f = &flight{kind: kindFetch, snap: fetch.Snapshot{ID: 7, Host: "h.test"}}
	require.True(t, r.s.recovered(f, "boom", nil, phaseCommit, result{}).commitFailed)
}
