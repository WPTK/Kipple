package sched

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// Busy is the favicon finder's yield signal. Status, which it used before,
// answers "no runs" whenever the dispatcher does not reply within statusWait,
// so background lookups ran exactly while the dispatcher was loaded. Busy reads
// an atomic kept in step with the runs map and reports busy once stopping.
func TestBusyIsReliableWhenTheDispatcherIsLoaded(t *testing.T) {
	old := statusWait
	statusWait = 30 * time.Millisecond
	t.Cleanup(func() { statusWait = old })

	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	a := r.add(srv.URL+"/a", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })
	require.False(t, r.s.Busy(), "no run yet")

	_, err := r.s.StartImport([]int64{a})
	require.NoError(t, err)
	waitFor(t, "a in flight", func() bool { return srv.count("/a") == 1 })
	require.True(t, r.s.Busy())

	// Wedge the dispatcher: Status gives up and reports no runs; Busy does not.
	unwedge := make(chan struct{})
	wedged := make(chan struct{})
	go r.s.inDispatcher(func() { close(wedged); <-unwedge })
	<-wedged
	runs, _ := r.s.Status()
	require.Empty(t, runs, "the old signal: a loaded dispatcher reads as idle")
	// From here a Busy that consults the dispatcher (as Status does) would wait an
	// hour for its answer, so it can only answer in time by not asking at all.
	statusWait = time.Hour
	// The dispatcher stays wedged until unwedge is closed below, after Busy has
	// answered, so an answer proves Busy did not wait on it: no stopwatch needed.
	// The 30 s timer only turns a Busy that does wait into a failure instead of a hang.
	busy := make(chan bool, 1)
	go func() { busy <- r.s.Busy() }()
	select {
	case got := <-busy:
		require.True(t, got, "a loaded dispatcher is not idle")
	case <-time.After(30 * time.Second):
		close(unwedge)
		t.Fatal("Busy waited on the wedged dispatcher")
	}
	close(unwedge)

	open()
	r.waitEvents("run.done", 1)
	r.barrier()
	require.False(t, r.s.Busy(), "the run ended")

	r.s.Stop()
	require.True(t, r.s.Busy(), "stopping counts as busy")
}

// Two overlapping runs of one kind are both tracked: a newer import that ends
// first must not unregister the older one, which is still listed by Status,
// still reports progress, and keeps Busy true until its own jobs are done.
func TestOverlappingRunsOfOneKindAreAllTracked(t *testing.T) {
	r := newRig(t, Options{})
	rel := map[string]chan struct{}{"/a1": make(chan struct{}), "/a2": make(chan struct{}), "/b": make(chan struct{})}
	var mu sync.Mutex
	released := map[string]bool{}
	release := func(p string) {
		mu.Lock()
		defer mu.Unlock()
		if !released[p] {
			released[p] = true
			close(rel[p])
		}
	}
	t.Cleanup(func() { release("/a1"); release("/a2"); release("/b") })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-rel[p]:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	far := func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() }
	a1 := r.add(srv.URL+"/a1", far)
	a2 := r.add(srv.URL+"/a2", far)
	b := r.add(srv.URL+"/b", far)
	r.setHost(b, "other.test")

	older, err := r.s.StartImport([]int64{a1, a2})
	require.NoError(t, err)
	newer, err := r.s.StartImport([]int64{b})
	require.NoError(t, err)
	require.NotEqual(t, older.RunID, newer.RunID)
	waitFor(t, "all in flight", func() bool { return srv.count("/a1") == 1 && srv.count("/a2") == 1 && srv.count("/b") == 1 })

	ids := func() (out []int64) {
		runs, _ := r.s.Status()
		for _, rs := range runs {
			out = append(out, rs.ID)
		}
		return out
	}
	require.Equal(t, []int64{older.RunID, newer.RunID}, ids(), "both runs are listed")

	release("/b") // the newer run ends first
	r.waitEvents("run.done", 1)
	require.Equal(t, idStr(newer.RunID), r.events("run.done")[0]["run_id"])
	r.barrier()
	require.True(t, r.s.Busy(), "the older run still has jobs")
	require.Equal(t, []int64{older.RunID}, ids(), "the older run is still listed")

	release("/a1")
	waitFor(t, "older run progress", func() bool {
		for _, ev := range r.events("run.progress") {
			if ev["run_id"] == idStr(older.RunID) {
				return true
			}
		}
		return false
	})

	release("/a2")
	r.waitEvents("run.done", 2)
	r.barrier()
	require.False(t, r.s.Busy(), "every run ended")
	require.Empty(t, ids())
}
