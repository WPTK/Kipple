package sched

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// blockingSrv holds /a until release is closed; every other path serves the feed.
func blockingSrv(t *testing.T) (*feedSrv, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	rel := func() { once.Do(func() { close(release) }) }
	t.Cleanup(rel)
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if p == "/a" {
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		serveOK(p, w, req)
	})
	return srv, rel
}

// A job waiting in the queue picks up changes made to its feed meanwhile: a
// feed disabled while its job waited is not fetched and its caller hears so.
func TestQueuedJobDroppedWhenFeedDisabled(t *testing.T) {
	r := newRig(t, Options{Workers: 1})
	srv, release := blockingSrv(t)
	r.add(srv.URL+"/a", nil)
	r.s.Wake()
	waitFor(t, "a in flight", func() bool { return srv.count("/a") == 1 })

	b := r.add(srv.URL+"/b", nil)
	ch, err := r.s.Submit(Priority{FeedID: b})
	require.NoError(t, err)
	r.barrier()
	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", b)

	release()
	select {
	case rep := <-ch:
		require.ErrorIs(t, rep.Err, ErrDisabled)
	case <-time.After(10 * time.Second):
		t.Fatal("no reply for the dropped job")
	}
	r.waitEvents("fetch.done", 1)
	require.Zero(t, srv.count("/b"), "the disabled feed was not fetched")
	require.Zero(t, r.flights())
	var running int
	r.s.inDispatcher(func() { running = r.s.running })
	require.Zero(t, running)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log"), "only a's fetch was logged")
}

// A URL edit while the job waited: the job fetches the new URL, and its result is
// committed (not dropped as stale).
func TestQueuedJobUsesCurrentURL(t *testing.T) {
	r := newRig(t, Options{Workers: 1})
	srv, release := blockingSrv(t)
	r.add(srv.URL+"/a", nil)
	r.s.Wake()
	waitFor(t, "a in flight", func() bool { return srv.count("/a") == 1 })

	b := r.add(srv.URL+"/b", nil)
	ch, err := r.s.Submit(Priority{FeedID: b})
	require.NoError(t, err)
	r.barrier()
	r.sql("UPDATE feeds SET url = ?, url_key = ? WHERE id = ?", srv.URL+"/b2", srv.URL[len("http://"):]+"/b2", b)

	release()
	select {
	case rep := <-ch:
		require.NoError(t, rep.Err)
		require.Equal(t, fetch.OutcomeOK, rep.Outcome)
		require.Equal(t, 2, rep.NewItems, "committed, not dropped as stale")
	case <-time.After(10 * time.Second):
		t.Fatal("no reply")
	}
	require.Zero(t, srv.count("/b"))
	require.Equal(t, 1, srv.count("/b2"))
}

// A run whose queued job is dropped still finishes, counting the feed as an error.
func TestQueuedRunJobDroppedSettlesRun(t *testing.T) {
	r := newRig(t, Options{Workers: 1})
	srv, release := blockingSrv(t)
	r.add(srv.URL+"/a", nil)
	b := r.add(srv.URL+"/b", nil)
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 2, info.Total)
	waitFor(t, "a in flight", func() bool { return srv.count("/a") == 1 })
	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", b)
	release()
	r.waitEvents("run.done", 1)
	done := r.events("run.done")[0]
	require.EqualValues(t, 1, done["errors"])
	require.Zero(t, srv.count("/b"))
}
