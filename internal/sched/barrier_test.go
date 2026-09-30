package sched

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Submit returns once its request is in the buffered priorityCh, and the
// dispatcher's select picks at random among ready cases. The dispatcher is
// wedged while a Submit and a barrier queue up behind it, so both are ready at
// once when it is released: the barrier must still return only after the
// Submit was handled. With a single syncCh round trip as the barrier this
// failed about half the iterations (TestFullRefreshUpgradesPendingFlight's
// flake: its check ran before the upgrade it checked for).
func TestBarrierWaitsForQueuedSubmit(t *testing.T) {
	r := newRig(t, Options{})
	for i := range 50 {
		wedged, unwedge := make(chan struct{}), make(chan struct{})
		go r.s.inDispatcher(func() { close(wedged); <-unwedge })
		select {
		case <-wedged:
		case <-time.After(30 * time.Second):
			t.Fatal("the dispatcher never ran the wedge")
		}
		reply, err := r.s.Submit(Priority{FeedID: 1 << 40, Kind: PriorityTrim}) // no such feed: answered at once
		require.NoError(t, err)
		done := make(chan struct{})
		go func() { defer close(done); r.barrier() }()
		time.Sleep(time.Millisecond) // let the barrier's round trip queue up beside the Submit
		close(unwedge)
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("barrier did not return")
		}
		select {
		case rep := <-reply:
			require.ErrorIs(t, rep.Err, ErrNotFound)
		default:
			t.Fatalf("iteration %d: the barrier returned before the queued Submit was handled", i)
		}
	}
}

// A failed assertion (runtime.Goexit) or a panic inside inDispatcher's fn is
// re-raised on the caller, and the dispatcher keeps running: it must never turn
// into a wait that only go test's -timeout ends.
func TestInDispatcherReraisesGoexitAndPanic(t *testing.T) {
	r := newRig(t, Options{})

	exited := make(chan bool, 1)
	go func() {
		normal := false
		defer func() { exited <- !normal && recover() == nil }()
		r.s.inDispatcher(func() { runtime.Goexit() })
		normal = true
	}()
	select {
	case ok := <-exited:
		require.True(t, ok, "the caller's goroutine exits too")
	case <-time.After(30 * time.Second):
		t.Fatal("inDispatcher hung after fn called runtime.Goexit")
	}

	var got any
	func() {
		defer func() { got = recover() }()
		r.s.inDispatcher(func() { panic("boom") })
	}()
	require.Equal(t, "boom", got, "the panic reaches the caller")

	var alive bool
	r.s.inDispatcher(func() { alive = true })
	require.True(t, alive, "the dispatcher still answers")
}
