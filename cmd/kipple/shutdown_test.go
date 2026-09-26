package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/maint"
	"github.com/WPTK/kipple/internal/sched"
)

func shrinkBudget(t *testing.T) {
	t.Helper()
	oldT, oldR, oldH, oldS, oldC := shutdownTotal, closeReserve, httpDrainMax, schedDrainMax, closeMax
	oldSR, oldMF := storeCloseReserve, maintFloor
	t.Cleanup(func() {
		shutdownTotal, closeReserve, httpDrainMax, schedDrainMax, closeMax = oldT, oldR, oldH, oldS, oldC
		storeCloseReserve, maintFloor = oldSR, oldMF
	})
	// Scaled from 25 s / 5 s / 10 s / 15 s / 3 s / 1 s: the stage maxima alone add
	// up to more than the total, as in production.
	shutdownTotal, closeReserve, httpDrainMax, schedDrainMax = 1000*time.Millisecond, 200*time.Millisecond, 400*time.Millisecond, 600*time.Millisecond
	storeCloseReserve, maintFloor = 120*time.Millisecond, 40*time.Millisecond
}

// Every stage hanging still ends inside the one budget: the stages by the
// deadline less the close reserve, the deferred closes by the deadline. Even
// then maintenance gets its floor to stop, and the hanging UI API and image
// cache closes leave the store's close its own reserve.
func TestShutdownFitsOneBudgetWhenEveryStageHangs(t *testing.T) {
	shrinkBudget(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	var budget shutdownBudget
	cut := false
	var maintStart time.Time
	start := time.Now()
	err := runShutdown(&budget, shutdownSteps{
		stopWork:  func() {},
		drainHTTP: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		cutHTTP:   func() { cut = true },
		stopped:   make(chan struct{}), // the scheduler never drains
		stopMaint: func() { maintStart = time.Now(); <-hang }, // maintenance never stops
	}, quiet)
	stages := time.Since(start)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, cut, "a drain that ran out cuts the connections")
	require.Less(t, stages, shutdownTotal-storeCloseReserve+80*time.Millisecond, "the stages leave the store's reserve")
	require.False(t, maintStart.IsZero())
	require.GreaterOrEqual(t, time.Since(maintStart), maintFloor-10*time.Millisecond, "maintenance got its floor, not a zero wait")

	closeWithin(&budget, quiet, "closing the UI API", storeCloseReserve, func() error { <-hang; return nil })
	closeWithin(&budget, quiet, "closing image cache", storeCloseReserve, func() error { <-hang; return nil })
	storeStart := time.Now()
	closeWithin(&budget, quiet, "closing store", 0, func() error { <-hang; return nil })
	require.GreaterOrEqual(t, time.Since(storeStart), storeCloseReserve-30*time.Millisecond, "the store's close still had its reserve")
	require.Less(t, time.Since(start), shutdownTotal+80*time.Millisecond, "the whole shutdown fits the budget")

	// A close that finishes in time has its error logged.
	var logged bool
	closeWithin(&shutdownBudget{}, slog.New(slog.NewTextHandler(writerFunc(func(p []byte) { logged = true }), nil)), "x", 0,
		func() error { return errors.New("boom") })
	require.True(t, logged)
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func TestShutdownBudgetLeft(t *testing.T) {
	var b shutdownBudget
	require.Equal(t, 7*time.Second, b.left(time.Second, 7*time.Second), "not started: the stage maximum")
	shutdownTotalWas := shutdownTotal
	t.Cleanup(func() { shutdownTotal = shutdownTotalWas })
	shutdownTotal = 25 * time.Second
	b.start()
	require.LessOrEqual(t, b.left(5*time.Second, 30*time.Second), 20*time.Second)
	require.Equal(t, 10*time.Second, b.left(5*time.Second, 10*time.Second))
	require.Zero(t, b.left(26*time.Second, time.Second), "never negative")
}

// A setup failure after the store is open returns with no scheduler or
// maintenance running: they start only once every handler is built.
func TestRunServeStartsNothingWhenSetupFails(t *testing.T) {
	oldH, oldS, oldLocal, oldLog := newWebHandler, startBackground, time.Local, slog.Default()
	t.Cleanup(func() { newWebHandler, startBackground, time.Local = oldH, oldS, oldLocal; slog.SetDefault(oldLog) })
	started := false
	startBackground = func(*sched.Scheduler, *maint.Maint) { started = true }
	newWebHandler = func(func() string) (http.Handler, error) { return nil, errors.New("no dist") }

	t.Setenv("KIPPLE_DATA", filepath.Join(t.TempDir(), "data"))
	t.Setenv("KIPPLE_ADDR", "127.0.0.1:0")
	t.Setenv("KIPPLE_LOG_LEVEL", "error")
	t.Setenv("KIPPLE_USERNAME", "")
	t.Setenv("KIPPLE_PASSWORD", "")
	t.Setenv("KIPPLE_API_PASSWORD", "")
	t.Setenv("KIPPLE_PUBLIC_URL", "")
	t.Setenv("KIPPLE_TRUSTED_PROXY_IPS", "")
	t.Setenv("KIPPLE_SCHED_TICK", "")
	t.Setenv("TZ", "UTC")
	err := runServe()
	require.ErrorContains(t, err, "web: no dist")
	require.False(t, started, "nothing was started, so nothing is left running")
}
