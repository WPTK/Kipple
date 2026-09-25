package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
)

func TestFtFlightsClearsEntryOnPanic(t *testing.T) {
	var f ftFlights
	require.Panics(t, func() {
		_, _ = f.do(1, func() (extract.Result, error) { panic("boom") })
	})
	f.mu.Lock()
	_, stuck := f.m[1]
	f.mu.Unlock()
	require.False(t, stuck)
	_, err := f.do(1, func() (extract.Result, error) { return extract.Result{}, nil })
	require.NoError(t, err)
}

// A caller waiting on a flight whose leader panics must be released with an
// error, not left blocked forever.
func TestFtFlightsReleasesWaitersOnPanic(t *testing.T) {
	var f ftFlights
	started, release := make(chan struct{}), make(chan struct{})
	leader := make(chan any, 1)
	go func() {
		defer func() { leader <- recover() }()
		_, _ = f.do(7, func() (extract.Result, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started
	waiter := make(chan error, 1)
	go func() {
		_, err := f.do(7, func() (extract.Result, error) {
			t.Error("waiter must join the flight, not start its own")
			return extract.Result{}, nil
		})
		waiter <- err
	}()
	// let the waiter reach <-c.done (it holds no lock there); the entry exists throughout
	time.Sleep(50 * time.Millisecond)
	close(release)
	require.NotNil(t, <-leader, "the leader still panics")
	select {
	case err := <-waiter:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("waiter is stuck behind a panicked flight")
	}
}
