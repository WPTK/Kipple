package api

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Close must not return while a catch-up run it did not refuse is still being
// started: that run's goroutine would outlive Close and touch a closed database.
// Either the run is refused or Close waits for it.
func TestCloseWaitsForAutoReadBeingStarted(t *testing.T) {
	h := newHarness(t)
	admitted := make(chan struct{})
	release := make(chan struct{})
	h.srv.autoReadAdmitted = func() {
		close(admitted)
		<-release
	}
	started := make(chan error, 1)
	go func() {
		_, err := h.srv.startAutoRead(autoReadReq{}, 0)
		started <- err
	}()
	<-admitted

	closed := make(chan struct{})
	go func() {
		h.srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
		close(release)
		t.Fatal("Close returned while an admitted auto-read run had not started yet")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-started)
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned after the run was released")
	}
	require.Nil(t, h.srv.autoReadStatus(), "the run finished before Close returned")

	_, err := h.srv.startAutoRead(autoReadReq{}, 0)
	require.ErrorIs(t, err, errAutoReadBusy, "no run starts after Close")
}
