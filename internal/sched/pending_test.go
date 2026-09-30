package sched

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// pendingRig builds a one-worker scheduler whose worker is busy on /x, with
// feeds y and z (own hosts) queued behind it, unstarted.
func pendingRig(t *testing.T) (r *rig, srv *feedSrv, release func(), y, z int64) {
	t.Helper()
	r = newRig(t, Options{Workers: 1})
	rel := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(rel) }) }
	t.Cleanup(release)
	started := make(chan struct{}, 1)
	srv = newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if p == "/x" {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-rel:
			case <-req.Context().Done():
				return
			}
		}
		serveOK(p, w, req)
	})
	r.add(srv.URL+"/x", nil)
	r.s.Wake()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("/x never started")
	}
	y = r.add(srv.URL+"/y", nil)
	z = r.add(srv.URL+"/z", nil)
	r.setHost(y, "y.test")
	r.setHost(z, "z.test")
	r.s.Wake()
	r.barrier()
	var pending int
	r.s.inDispatcher(func() { pending = len(r.s.pending) })
	require.Equal(t, 2, pending)
	return
}

func holdHosts(r *rig, hosts ...string) {
	r.s.inDispatcher(func() {
		for _, h := range hosts {
			r.s.hostUntil[h] = r.clk.Now().Add(time.Hour)
		}
	})
}

func waitReply(t *testing.T, ch <-chan Reply, what string) Reply {
	t.Helper()
	select {
	case rep := <-ch:
		return rep
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no reply", what)
		return Reply{}
	}
}

// A trim queued behind a scheduled fetch that is still waiting for a worker,
// on a host that then turns held, must still run and be answered.
func TestPendingFlightWithTrimFollowupIsNotDropped(t *testing.T) {
	r, _, release, y, _ := pendingRig(t)
	holdHosts(r, "y.test")
	ch, err := r.s.Submit(Priority{FeedID: y, Kind: PriorityTrim})
	require.NoError(t, err)
	r.barrier()
	release()
	rep := waitReply(t, ch, "trim")
	require.NoError(t, rep.Err)
	require.Equal(t, fetch.OutcomeTrimOnly, rep.Outcome)
}

// A full refresh for a pending unstarted flight upgrades it in place and is
// answered by a real fetch even though the host is held.
func TestFullRefreshUpgradesPendingFlight(t *testing.T) {
	r, srv, release, _, z := pendingRig(t)
	holdHosts(r, "z.test")
	ch, err := r.s.Submit(Priority{FeedID: z, Full: true})
	require.NoError(t, err)
	r.barrier()
	var queued, full, started bool
	r.s.inDispatcher(func() {
		f := r.s.flights[z]
		queued = f != nil
		full, started = queued && f.snap.Full, queued && f.started
	})
	require.True(t, queued, "z still has its flight")
	require.False(t, started, "z is still waiting for the worker")
	require.True(t, full, "upgraded in place")
	release()
	rep := waitReply(t, ch, "full")
	require.NoError(t, rep.Err)
	require.Equal(t, fetch.OutcomeOK, rep.Outcome)
	require.Equal(t, 1, srv.count("/z"))
}

// A refresh-all must not count a trim job for a feed as having fetched it.
func TestRunDoesNotBorrowTrimFlight(t *testing.T) {
	r, srv, release, y, _ := pendingRig(t)
	ch, err := r.s.Submit(Priority{FeedID: y, Kind: PriorityTrim})
	require.NoError(t, err)
	r.barrier()
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 3, info.Total)
	release()
	require.Equal(t, fetch.OutcomeTrimOnly, waitReply(t, ch, "trim").Outcome)
	r.waitEvents("run.done", 1)
	require.Equal(t, 1, srv.count("/y"), "the run fetched the feed itself after the trim")
	require.Zero(t, r.events("run.done")[0]["errors"])
}
