package main

import (
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

// time.Local is a plain global that every time.Now reads. runServe sets it at
// start-up, and in one test binary runServe runs many times while goroutines of
// earlier tests (an HTTP keep-alive read loop pools its connection with
// time.Now) may still be running: that raced in issue #165. Waiting for those
// goroutines to exit is not enough for the race detector (a goroutine's exit
// does not order its last time.Now before the write), so:
//
//   - no test writes the real time.Local: tests that need a zone, or run code
//     that sets one, go through fakeLocalZone, which swaps the localZone and
//     writeLocalZone seams and restores them with t.Cleanup;
//   - tests that start an HTTP server or client call quiesceHTTP, which closes
//     idle connections and requires every HTTP goroutine gone before the test
//     ends, so none outlives its test.

// quietWait bounds the wait for goroutines that are already on their way out
// (a closed connection's read loop exits asynchronously).
const quietWait = 5 * time.Second

// strayGoroutine reports a goroutine (one runtime.Stack block) that a test may
// have left behind: one running or created by net/http (a server's conn,
// a transport's read or write loop) or created by Kipple code (runServe's serve
// goroutine, the scheduler, maintenance, a shutdown stage).
func strayGoroutine(stack string) bool {
	return strings.Contains(stack, "net/http.") ||
		strings.Contains(stack, "\ncreated by github.com/WPTK/kipple/")
}

// httpClientGoroutine reports a transport's connection goroutine (its read or
// write loop), the kind that keeps calling time.Now while a connection idles.
func httpClientGoroutine(stack string) bool {
	return strings.Contains(stack, "net/http.(*persistConn)")
}

// goroutines is every goroutine's stack but the caller's, one block each.
func goroutines() []string {
	buf := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	blocks := strings.Split(strings.TrimSpace(string(buf)), "\n\n")
	return blocks[1:] // the first block is the caller's own goroutine
}

// waitGoroutines waits up to quietWait until no goroutine matches, and returns
// the stacks still matching when it gives up (nil when none is left).
func waitGoroutines(match func(string) bool) []string {
	deadline := time.Now().Add(quietWait)
	for {
		var left []string
		for _, g := range goroutines() {
			if match(g) {
				left = append(left, g)
			}
		}
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// requireNoGoroutines fails the test if a goroutine matching match is still
// running after quietWait, printing their stacks.
func requireNoGoroutines(t *testing.T, match func(string) bool, why string) {
	t.Helper()
	if left := waitGoroutines(match); len(left) > 0 {
		t.Fatalf("%s: %d goroutine(s) still running:\n\n%s", why, len(left), strings.Join(left, "\n\n"))
	}
}

// requireQuiet fails the test if any stray goroutine (strayGoroutine) is
// still running after quietWait.
func requireQuiet(t *testing.T) {
	t.Helper()
	requireNoGoroutines(t, strayGoroutine, "goroutines left behind")
}

// quiesceHTTP makes the end of the test close the idle connections of
// http.DefaultTransport and of every transport given, then wait until no HTTP
// or Kipple goroutine is left. Call it before starting the servers and clients
// so that its cleanup runs after theirs (cleanups run last-registered first).
func quiesceHTTP(t *testing.T, transports ...*http.Transport) {
	t.Helper()
	t.Cleanup(func() {
		if tr, ok := http.DefaultTransport.(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
		for _, tr := range transports {
			tr.CloseIdleConnections()
		}
		requireQuiet(t)
	})
}

// fakeZone stands in for time.Local during a test (fakeLocalZone).
type fakeZone struct {
	loc    *time.Location // what localZone reports
	writes int            // how many times setLocal wrote it
}

// fakeLocalZone points the localZone and writeLocalZone seams at a fake that
// starts as loc, for the rest of the test, and puts the real ones back when it
// ends: the code under test then never writes the real time.Local. It first
// requires that no stray goroutine is left, so an earlier test that leaked one
// fails here with its stack. Call it before the test starts anything (runServe
// reads the seams). Never use it in a parallel test.
func fakeLocalZone(t *testing.T, loc *time.Location) *fakeZone {
	t.Helper()
	requireQuiet(t)
	z := &fakeZone{loc: loc}
	oldGet, oldSet := localZone, writeLocalZone
	t.Cleanup(func() { localZone, writeLocalZone = oldGet, oldSet })
	localZone = func() *time.Location { return z.loc }
	writeLocalZone = func(l *time.Location) { z.loc = l; z.writes++ }
	return z
}
