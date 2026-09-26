package sched

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// waitingJob builds a job for feed id as if it had been queued earlier with
// queuedHost; the caller puts it on the pending list inside the dispatcher.
// (Assertions stay outside inDispatcher: a failing require there would end the
// dispatcher goroutine and hang the test.)
func waitingJob(t *testing.T, r *rig, id int64, queuedHost string, replies ...chan Reply) *flight {
	t.Helper()
	snap, ok, err := r.db.FeedSnapshot(context.Background(), r.db.FetchSettings(context.Background()), id)
	require.NoError(t, err)
	require.True(t, ok)
	snap.Host, snap.Trigger = queuedHost, fetch.TriggerScheduled
	return &flight{snap: snap, kind: kindFetch, waited: true, replies: replies}
}

func (s *Scheduler) queueForTest(f *flight) {
	s.flights[f.snap.ID] = f
	s.pending = append(s.pending, f)
}

// A reloaded job that still cannot start (the reload moved it to a host whose
// slots are taken) is reloaded again before it finally starts, so a feed
// disabled meanwhile is not fetched.
func TestReloadedJobThatCannotStartIsReloadedAgain(t *testing.T) {
	r := newRig(t, Options{Workers: 1, PerHost: 1})
	y := r.add("http://y.test/feed", nil)
	r.setHost(y, "busy.test") // a URL edit moved it to a host whose slot is taken
	reply := make(chan Reply, 1)
	f := waitingJob(t, r, y, "y.test", reply)
	var pending int
	var host string
	var waited bool
	r.s.inDispatcher(func() {
		r.s.perHost["busy.test"] = 1
		r.s.queueForTest(f)
		r.s.drainPending()
		pending, host, waited = len(r.s.pending), f.snap.Host, f.waited
	})
	require.Equal(t, 1, pending, "still waiting for the host slot")
	require.Equal(t, "busy.test", host, "reloaded")
	require.True(t, waited, "it will be reloaded again before it starts")

	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", y)
	var running int
	r.s.inDispatcher(func() {
		delete(r.s.perHost, "busy.test")
		r.s.drainPending()
		pending, running = len(r.s.pending), r.s.running
	})
	require.Zero(t, pending)
	require.Zero(t, running, "the disabled feed was not started")
	require.ErrorIs(t, waitReply(t, reply, "dropped job").Err, ErrDisabled)
}

// The Retry-After decision is redone after the reload: a job queued for a free
// host whose feed now lives on a held host is dropped (a plain scheduled fetch)
// or turned into a skip (someone waits on it), not fetched against the hold.
func TestReloadRedoesHostHoldDecision(t *testing.T) {
	r := newRig(t, Options{Workers: 2, PerHost: 1})
	y := r.add("http://y.test/feed", nil)
	z := r.add("http://z.test/feed", nil)
	r.setHost(y, "held.test")
	r.setHost(z, "held.test")
	until := r.clk.Now().Add(time.Hour)
	reply := make(chan Reply, 1)
	fy := waitingJob(t, r, y, "y.test")
	fz := waitingJob(t, r, z, "z.test", reply)
	var pending, running int
	var yStill bool
	var zKind jobKind
	var zUntil time.Time
	r.s.inDispatcher(func() {
		r.s.hostUntil["held.test"] = until
		r.s.queueForTest(fy)
		r.s.queueForTest(fz)
		r.s.drainPending()
		_, yStill = r.s.flights[y]
		pending, running, zKind, zUntil = len(r.s.pending), r.s.running, fz.kind, fz.snap.HostUntil
	})
	require.Zero(t, pending)
	require.False(t, yStill, "the plain scheduled fetch is dropped; the feed stays due")
	require.Equal(t, kindSkip, zKind, "the job someone waits on becomes a skip")
	require.Equal(t, until, zUntil)
	require.Equal(t, 1, running, "only the skip started")
	require.Equal(t, fetch.OutcomeSkipped, waitReply(t, reply, "skip").Outcome)
}

// A job turned into a skip for its queued host becomes a fetch again when the
// reload moves it to a host that is not held.
func TestReloadedSkipForFreeHostFetches(t *testing.T) {
	r := newRig(t, Options{Workers: 1, PerHost: 1})
	y := r.add("http://y.test/feed", nil)
	r.setHost(y, "free.test")
	f := waitingJob(t, r, y, "old.test", make(chan Reply, 1))
	var started bool
	var kind jobKind
	var until time.Time
	r.s.inDispatcher(func() {
		r.s.hostUntil["old.test"] = r.clk.Now().Add(time.Hour)
		f.kind, f.snap.HostUntil = kindSkip, r.s.hostUntil["old.test"]
		r.s.flights[y] = f
		// tryStart directly: drainPending would keep the skip for old.test.
		started = r.s.tryStart(f)
		kind, until = f.kind, f.snap.HostUntil
	})
	require.True(t, started)
	require.Equal(t, kindFetch, kind)
	require.True(t, until.IsZero())
}
