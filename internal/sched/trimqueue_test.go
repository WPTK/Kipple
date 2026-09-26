package sched

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// withRequeueDelay shortens trimRequeueDelay; call it before newRig so the
// restore runs after the scheduler has stopped.
func withRequeueDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := trimRequeueDelay
	trimRequeueDelay = d
	t.Cleanup(func() { trimRequeueDelay = old })
}

// trimRecorder replaces the trim job: moreFor answers more for the first calls.
type trimRecorder struct {
	mu      sync.Mutex
	calls   []int64
	budgets []store.TrimBudget
	moreFor int
	started chan struct{}
}

func (tr *trimRecorder) fn(_ context.Context, feedID int64, b store.TrimBudget) (int64, bool, error) {
	tr.mu.Lock()
	tr.calls = append(tr.calls, feedID)
	tr.budgets = append(tr.budgets, b)
	more := len(tr.calls) <= tr.moreFor
	tr.mu.Unlock()
	if tr.started != nil {
		select {
		case tr.started <- struct{}{}:
		default:
		}
	}
	return 7, more, nil
}

func (tr *trimRecorder) n() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.calls)
}

// A fetch commit whose trim filled its batch queues a trim job for the feed, so
// the backlog does not wait for the next fetch.
func TestFetchWithTrimPendingQueuesTrimJob(t *testing.T) {
	withRequeueDelay(t, 10*time.Millisecond)
	r := newRig(t, Options{})
	tr := &trimRecorder{}
	r.s.inDispatcher(func() {
		r.s.trimFn = tr.fn
		r.s.commitFetchFn = func(ctx context.Context, res *fetch.Result, perChunk time.Duration) (store.CommitInfo, error) {
			ci, err := r.db.CommitFetchTimeout(ctx, res, perChunk)
			ci.TrimPending = true
			return ci, err
		}
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 2) // the fetch, then the trim job
	waitFor(t, "the trim job", func() bool { return tr.n() == 1 })
	tr.mu.Lock()
	require.Equal(t, []int64{id}, tr.calls)
	require.Equal(t, store.TrimBudget{Total: commitTimeout, PerBatch: commitTimeout}, tr.budgets[0],
		"the job has one commit timeout in all, and each batch its own")
	tr.mu.Unlock()
	evs := r.events("fetch.done")
	require.Equal(t, fetch.OutcomeTrimOnly, evs[1]["outcome"])
	require.Equal(t, fetch.TriggerRetention, evs[1]["trigger"])

	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, tr.n(), "a finished trim queues nothing more")
	require.Equal(t, 1, srv.total(), "and fetches nothing")
}

// A fetch that trimmed within its batch queues nothing.
func TestFetchWithoutTrimPendingQueuesNothing(t *testing.T) {
	withRequeueDelay(t, time.Millisecond)
	r := newRig(t, Options{})
	tr := &trimRecorder{}
	r.s.inDispatcher(func() { r.s.trimFn = tr.fn })
	srv := newSrv(t, serveOK)
	r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, tr.n())
}

// A trim job that runs out of budget with work left queues itself again after a
// delay, until the backlog is gone.
func TestTrimJobRequeuesUntilDone(t *testing.T) {
	withRequeueDelay(t, 20*time.Millisecond)
	r := newRig(t, Options{})
	tr := &trimRecorder{moreFor: 3}
	r.s.inDispatcher(func() { r.s.trimFn = tr.fn })
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	ch, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	rep := waitReply(t, ch, "trim")
	require.EqualValues(t, 7, rep.Trimmed, "the caller is answered by the first job")
	began := time.Now()
	waitFor(t, "four trim jobs", func() bool { return tr.n() == 4 })
	require.GreaterOrEqual(t, time.Since(began), 3*trimRequeueDelay-5*time.Millisecond, "each follow-up waits the delay: no hot loop")
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 4, tr.n(), "the job that finished queues nothing")
	require.Zero(t, srv.total())
}

// A queued follow-up joins a trim job already queued or running for the feed
// rather than adding a second one (single flight per feed).
func TestRequeuedTrimJoinsTheFlightInProgress(t *testing.T) {
	withRequeueDelay(t, time.Millisecond)
	r := newRig(t, Options{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	r.s.inDispatcher(func() {
		r.s.trimFn = func(context.Context, int64, store.TrimBudget) (int64, bool, error) {
			mu.Lock()
			calls++
			c := calls
			mu.Unlock()
			if c == 2 {
				<-release // the follow-up job is running
			}
			return 1, c <= 2, nil
		}
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	_, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	waitFor(t, "the follow-up job", func() bool { mu.Lock(); defer mu.Unlock(); return calls == 2 })
	// a second request while it runs is answered by it
	ch, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	r.barrier()
	require.Equal(t, 1, r.flights())
	close(release)
	waitReply(t, ch, "joined trim")
	waitFor(t, "the third job", func() bool { mu.Lock(); defer mu.Unlock(); return calls == 3 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	require.Equal(t, 3, calls)
	mu.Unlock()
}

// Shutdown stops the chain: no follow-up trim job is queued once Stop was called.
func TestTrimRequeueStopsAtShutdown(t *testing.T) {
	withRequeueDelay(t, 30*time.Millisecond)
	r := newRig(t, Options{})
	tr := &trimRecorder{moreFor: 1 << 30, started: make(chan struct{}, 1)}
	r.s.inDispatcher(func() { r.s.trimFn = tr.fn })
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)
	ch, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	waitReply(t, ch, "trim")
	r.s.Stop()
	<-r.s.Stopped()
	n := tr.n()
	time.Sleep(4 * trimRequeueDelay)
	require.Equal(t, n, tr.n(), "nothing runs after shutdown")
	require.LessOrEqual(t, n, 2)
}

// A trim job stopped by shutdown between batches is not an error: what it
// trimmed is reported and the rest is marked pending (never re-queued, since the
// dispatcher is stopping).
func TestTrimJobShutdownIsNotAnError(t *testing.T) {
	r := newRig(t, Options{})
	r.s.inDispatcher(func() {
		r.s.trimFn = func(ctx context.Context, _ int64, _ store.TrimBudget) (int64, bool, error) {
			r.s.cancelFetch() // shutdown arrives after the first batch
			return 7, true, ctx.Err()
		}
	})
	got := r.s.exec(&flight{snap: fetch.Snapshot{ID: 1}, kind: kindTrim})
	require.False(t, got.cancelled)
	require.Equal(t, fetch.OutcomeTrimOnly, got.outcome)
	require.Empty(t, got.errMsg)
	require.EqualValues(t, 7, got.trimmed)
	require.True(t, got.trimPending)
}
