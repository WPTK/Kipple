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
	start := time.Now()
	require.True(t, r.s.Busy(), "a loaded dispatcher is not idle")
	require.Less(t, time.Since(start), 10*time.Millisecond, "Busy never waits on the dispatcher")
	close(unwedge)

	open()
	r.waitEvents("run.done", 1)
	r.barrier()
	require.False(t, r.s.Busy(), "the run ended")

	r.s.Stop()
	require.True(t, r.s.Busy(), "stopping counts as busy")
}
