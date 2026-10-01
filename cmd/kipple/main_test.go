package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestApplyTZ(t *testing.T) {
	setLocalForTest(t, time.Local)

	require.NoError(t, applyTZ("America/New_York"))
	noon := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 8, noon.In(time.Local).Hour(), "EDT is UTC-4 (tzdata is embedded)")
	require.Equal(t, 7, time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC).In(time.Local).Hour(), "EST is UTC-5")

	require.NoError(t, applyTZ("America/Chicago"))
	require.Equal(t, 7, noon.In(time.Local).Hour())

	before := time.Local
	require.Error(t, applyTZ("Not/AZone"))
	require.Same(t, before, time.Local, "a bad zone leaves time.Local alone")

	// The same zone again is not a write (issue #165): a repeated start leaves the
	// global alone, even though LoadLocation returns a new *Location every time.
	require.NoError(t, applyTZ("America/Chicago"))
	require.Same(t, before, time.Local, "an unchanged zone leaves time.Local alone")
	other, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	require.NotSame(t, before, other, "the check above would pass without the name comparison otherwise")
	setLocal(other)
	require.Same(t, before, time.Local)
	setLocal(time.UTC)
	require.Same(t, time.UTC, time.Local, "a different zone is still applied")
}

func TestSuperviseServeExitCodes(t *testing.T) {
	quietLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	noStop := func() error { return nil }

	// SIGTERM: stopAll clean, listener reports nil (ErrServerClosed mapped) -> exit 0
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	stopped := false
	cancel()
	err := superviseServe(ctx, serveErr, func() error { stopped = true; serveErr <- nil; return nil }, nil, quietLog)
	require.NoError(t, err)
	require.True(t, stopped)

	// SIGTERM with an unclean shutdown (grace period expired): still a normal exit,
	// and serveErr is still consumed
	serveErr = make(chan error, 1)
	err = superviseServe(ctx, serveErr, func() error {
		serveErr <- nil
		return context.DeadlineExceeded
	}, nil, quietLog)
	require.NoError(t, err)
	require.Empty(t, serveErr, "serveErr was read")

	// listener never reports: bounded wait, not a hang
	old := serveDrainWait
	serveDrainWait = 20 * time.Millisecond
	t.Cleanup(func() { serveDrainWait = old })
	require.NoError(t, superviseServe(ctx, make(chan error), noStop, nil, quietLog))

	// a listener that failed on its own is returned, after stopAll ran
	serveErr = make(chan error, 1)
	serveErr <- errors.New("listen tcp :7080: address already in use")
	stopped = false
	err = superviseServe(context.Background(), serveErr, func() error { stopped = true; return nil }, nil, quietLog)
	require.ErrorContains(t, err, "address already in use")
	require.True(t, stopped)

	// a real serve error that arrives after the signal is still surfaced
	serveErr = make(chan error, 1)
	err = superviseServe(ctx, serveErr, func() error { serveErr <- errors.New("serve failed"); return nil }, nil, quietLog)
	require.ErrorContains(t, err, "serve failed")
}
