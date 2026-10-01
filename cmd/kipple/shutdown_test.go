package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/favicon"
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
	var cut atomic.Bool
	var maintStartNs atomic.Int64 // written by the hung maintenance goroutine
	start := time.Now()
	err := runShutdown(&budget, shutdownSteps{
		stopWork:  func() {},
		drainHTTP: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		cutHTTP:   func() { cut.Store(true) },
		stopped:   make(chan struct{}),                                          // the scheduler never drains
		stopMaint: func() { maintStartNs.Store(time.Now().UnixNano()); <-hang }, // maintenance never stops
	}, quiet)
	stages := time.Since(start)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, cut.Load(), "a drain that ran out cuts the connections")
	require.Less(t, stages, shutdownTotal-storeCloseReserve+80*time.Millisecond, "the stages leave the store's reserve")
	require.NotZero(t, maintStartNs.Load())
	require.GreaterOrEqual(t, time.Since(time.Unix(0, maintStartNs.Load())), maintFloor-10*time.Millisecond, "maintenance got its floor, not a zero wait")

	closeWithin(&budget, quiet, "closing the UI API", storeCloseReserve, func() error { <-hang; return nil })
	closeWithin(&budget, quiet, "closing image cache", storeCloseReserve, func() error { <-hang; return nil })
	storeStart := time.Now()
	closeWithin(&budget, quiet, "closing store", 0, func() error { <-hang; return nil })
	require.GreaterOrEqual(t, time.Since(storeStart), storeCloseReserve-30*time.Millisecond, "the store's close still had its reserve")
	require.Less(t, time.Since(start), shutdownTotal+80*time.Millisecond, "the whole shutdown fits the budget")

	// A close that finishes in time has its error logged.
	var logged atomic.Bool
	closeWithin(&shutdownBudget{}, slog.New(slog.NewTextHandler(writerFunc(func(p []byte) { logged.Store(true) }), nil)), "x", 0,
		func() error { return errors.New("boom") })
	require.True(t, logged.Load())
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
	oldH, oldS, oldLog := newWebHandler, startBackground, slog.Default()
	t.Cleanup(func() { newWebHandler, startBackground = oldH, oldS; slog.SetDefault(oldLog) })
	started := false
	startBackground = func(*sched.Scheduler, *maint.Maint, *favicon.Finder) { started = true }
	newWebHandler = func(func() string) (http.Handler, error) { return nil, errors.New("no dist") }

	serveEnv(t, "127.0.0.1:0")
	err := runServe()
	require.ErrorContains(t, err, "web: no dist")
	require.False(t, started, "nothing was started, so nothing is left running")
}

// serveEnv sets the environment for a runServe test: a fresh data directory,
// addr, no account, TZ=UTC, and a fake time.Local (fakeLocalZone, starting as
// UTC) that runServe sets instead of the real one (issue #165). Call it before
// starting anything.
func serveEnv(t *testing.T, addr string) *fakeZone {
	t.Helper()
	z := fakeLocalZone(t, time.UTC)
	t.Setenv("KIPPLE_DATA", filepath.Join(t.TempDir(), "data"))
	t.Setenv("KIPPLE_ADDR", addr)
	t.Setenv("KIPPLE_LOG_LEVEL", "error")
	t.Setenv("KIPPLE_USERNAME", "")
	t.Setenv("KIPPLE_PASSWORD", "")
	t.Setenv("KIPPLE_API_PASSWORD", "")
	t.Setenv("KIPPLE_PUBLIC_URL", "")
	t.Setenv("KIPPLE_TRUSTED_PROXY_IPS", "")
	t.Setenv("KIPPLE_SCHED_TICK", "")
	t.Setenv("TZ", "UTC")
	return z
}

// runServe started and stopped again and again on one data directory, each time
// while a client holds an idle keep-alive connection to it, leaves nothing
// running when it returns (no serve, scheduler, maintenance or connection
// goroutine) and sets the zone only on the first start: a goroutine left
// behind, or a repeated write of time.Local, is what raced in issue #165.
// Deterministic: no -race needed.
func TestRunServeStartStopLeavesNothingRunning(t *testing.T) {
	oldH, oldSig, oldListen, oldLog := newWebHandler, stopSignals, listenTCP, slog.Default()
	t.Cleanup(func() { newWebHandler, stopSignals, listenTCP = oldH, oldSig, oldListen; slog.SetDefault(oldLog) })
	newWebHandler = func(func() string) (http.Handler, error) { return http.NotFoundHandler(), nil }
	z := serveEnv(t, "127.0.0.1:0")
	t.Setenv("TZ", "America/New_York") // not the fake's UTC: the first start applies it

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stopSignals = func() (context.Context, context.CancelFunc) { return ctx, cancel }
		lns := make(chan net.Listener, 1)
		listenTCP = func(string) (net.Listener, error) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				lns <- ln
			}
			return ln, err
		}
		done := make(chan error, 1)
		go func() { done <- runServe() }()
		var ln net.Listener
		select {
		case ln = <-lns:
		case err := <-done:
			t.Fatalf("runServe returned before listening: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("runServe did not listen")
		}

		// One request on a keep-alive connection, which then idles in the pool.
		tr := &http.Transport{Proxy: nil}
		resp, err := (&http.Client{Transport: tr}).Get("http://" + ln.Addr().String() + "/healthz")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)

		cancel() // the stop signal
		select {
		case err := <-done:
			require.NoError(t, err, "a signalled stop is a clean exit")
		case <-time.After(30 * time.Second):
			t.Fatal("runServe did not stop")
		}
		require.Equal(t, "America/New_York", z.loc.String(), "TZ is applied")
		require.Equal(t, 1, z.writes, "only the first start wrote time.Local: the zone was unchanged after it")
		// Shutdown closed the server's side of the idle connection; the client's
		// side goes too, and then nothing at all may be left running.
		tr.CloseIdleConnections()
		requireQuiet(t)
	}
}

// The favicon finder starts with the scheduler and, on any return from
// runServe after it started (here the listener fails), has stopped before the
// store closed: the deferred Stop and the budgeted stopAll both join it.
func TestRunServeStopsTheFaviconFinder(t *testing.T) {
	oldH, oldS, oldLog := newWebHandler, startBackground, slog.Default()
	t.Cleanup(func() { newWebHandler, startBackground = oldH, oldS; slog.SetDefault(oldLog) })
	newWebHandler = func(func() string) (http.Handler, error) { return http.NotFoundHandler(), nil }
	var finder *favicon.Finder
	startBackground = func(s *sched.Scheduler, m *maint.Maint, icons *favicon.Finder) {
		oldS(s, m, icons)
		finder = icons
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	serveEnv(t, taken.Addr().String())
	err = runServe()
	require.Error(t, err, "the address is taken")
	require.NotNil(t, finder, "it was started")
	select {
	case <-finder.Done():
	default:
		t.Fatal("the favicon finder is still running after runServe returned")
	}
}
