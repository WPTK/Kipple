// Package sched is the fetch scheduler (design §4.1-4.3, §4.9, §4.10): one
// dispatcher goroutine that owns all scheduling state, and a pool of workers
// that fetch and commit. The dispatcher never blocks; workers reach it only
// through channels.
package sched

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// Errors returned to API callers.
var (
	ErrStopped  = errors.New("sched: scheduler is shutting down")
	ErrNotFound = errors.New("sched: no such feed")
	ErrDisabled = errors.New("sched: feed is disabled")
)

// Run kinds (design §4.1). At most one run per kind is active.
const (
	RunManual    = "manual"
	RunImport    = "import"
	RunRetention = "retention"
)

// Options configure the scheduler. Zero values take the design defaults.
type Options struct {
	Workers int           // KIPPLE_FETCH_WORKERS, 8
	PerHost int           // KIPPLE_FETCH_PER_HOST, 2
	Tick    time.Duration // KIPPLE_SCHED_TICK, 30 s
	Rand    fetch.Rand    // jitter source, default math/rand/v2
}

// RunInfo answers a refresh-all, import or retention request.
type RunInfo struct {
	RunID  int64
	Kind   string
	Total  int
	Joined bool
}

// Reply is the result of a priority job.
type Reply struct {
	FeedID   int64
	Outcome  string
	NewItems int
	Updated  int
	Trimmed  int64
	ErrClass string
	ErrMsg   string
	Err      error // ErrNotFound, ErrDisabled, ErrStopped
}

// PriorityKind selects what a priority job does.
type PriorityKind int

// Priority job kinds.
const (
	PriorityRefresh PriorityKind = iota // fetch this feed now
	PriorityTrim                        // retention transaction only
)

// Priority describes one per-feed job (health-view refresh, UI subscribe,
// rekey, a retention PATCH).
type Priority struct {
	FeedID  int64
	Kind    PriorityKind
	Full    bool   // drop validators and the body-hash short circuit
	Trigger string // fetch_log trigger; default feed_manual (refresh) / retention (trim)
}

type jobKind int

const (
	kindFetch jobKind = iota
	kindSkip
	kindTrim
)

// flight is one queued or running job. Fields other than snap and kind are
// owned by the dispatcher; a worker only reads snap and kind.
type flight struct {
	snap    fetch.Snapshot
	kind    jobKind
	started bool
	runs    []*Run
	replies []chan Reply
}

type result struct {
	exit      bool
	feedID    int64
	host      string
	trigger   string
	outcome   string
	status    int
	errClass  string
	errMsg    string
	newIDs    []int64
	updated   int
	trimmed   int64
	newItems  int
	retry     time.Duration
	nextFetch time.Time
	cancelled bool
}

type runReq struct {
	kind    string
	feedIDs []int64 // import only
	all     bool    // retention: every feed instead of only those inheriting the default
	reply   chan runReply
}

type runReply struct {
	info RunInfo
	err  error
}

type priorityReq struct {
	p     Priority
	reply chan Reply
}

// Scheduler is the fetch dispatcher plus its workers.
type Scheduler struct {
	db     *store.DB
	client *fetch.Client
	hub    *events.Hub
	clk    clock.Clock
	log    *slog.Logger
	opt    Options

	jobs       chan *flight
	doneCh     chan result
	manualCh   chan runReq
	priorityCh chan priorityReq
	wake       chan struct{}
	stopCh     chan struct{}
	shutdownCh chan struct{}
	stopped    chan struct{}
	syncCh     chan func()

	fetchCtx    context.Context
	cancelFetch context.CancelFunc
	stopOnce    sync.Once
	startOnce   sync.Once

	// dispatcher-owned
	flights   map[int64]*flight
	perHost   map[string]int
	hostUntil map[string]time.Time
	pending   []*flight
	runs      map[string]*Run
	running   int
	live      int
	stopping  bool
	lastRunID int64
}

// New builds a scheduler. Call Start to run it and Stop to shut it down.
func New(db *store.DB, client *fetch.Client, hub *events.Hub, clk clock.Clock, log *slog.Logger, opt Options) *Scheduler {
	if opt.Workers <= 0 {
		opt.Workers = 8
	}
	if opt.PerHost <= 0 {
		opt.PerHost = 2
	}
	if opt.Tick <= 0 {
		opt.Tick = 30 * time.Second
	}
	if opt.Rand == nil {
		opt.Rand = rand.Float64
	}
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		db: db, client: client, hub: hub, clk: clk, log: log, opt: opt,
		jobs:       make(chan *flight, opt.Workers),
		doneCh:     make(chan result, opt.Workers),
		manualCh:   make(chan runReq, 8),
		priorityCh: make(chan priorityReq, 32),
		wake:       make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		shutdownCh: make(chan struct{}),
		stopped:    make(chan struct{}),
		syncCh:     make(chan func()),
		fetchCtx:   ctx, cancelFetch: cancel,
		flights:   map[int64]*flight{},
		perHost:   map[string]int{},
		hostUntil: map[string]time.Time{},
		runs:      map[string]*Run{},
	}
}

// Start launches the dispatcher and the workers. It is idempotent.
func (s *Scheduler) Start() {
	s.startOnce.Do(func() {
		tickC, stopTick := s.clk.Ticker(s.opt.Tick)
		s.live = s.opt.Workers
		for i := 0; i < s.opt.Workers; i++ {
			go s.worker()
		}
		go s.dispatch(tickC, stopTick)
	})
}

// Wake asks the dispatcher to run a tick now (non-blocking).
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Shutdown is closed as soon as Stop is called: handlers waiting on the
// scheduler select on it and return 503.
func (s *Scheduler) Shutdown() <-chan struct{} { return s.shutdownCh }

// Stopped is closed once the dispatcher has drained every worker.
func (s *Scheduler) Stopped() <-chan struct{} { return s.stopped }

// Stop begins shutdown (design §4.10 step 1): refuse new work, cancel
// in-flight HTTP, close the job queue. Workers finish the job in hand; a
// completed fetch still commits, an aborted one writes nothing. Wait on
// Stopped() for the drain.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.shutdownCh)
		close(s.stopCh)
	})
}

// RefreshAll starts a manual run over every enabled feed, or joins the active
// manual run (design §4.9).
func (s *Scheduler) RefreshAll() (RunInfo, error) { return s.startRun(runReq{kind: RunManual}) }

// StartImport starts an import run over the given (newly inserted) feeds.
func (s *Scheduler) StartImport(feedIDs []int64) (RunInfo, error) {
	return s.startRun(runReq{kind: RunImport, feedIDs: feedIDs})
}

// ApplyRetention starts a retention run of trim_only jobs: over the feeds that
// inherit retention.default, or over every feed when all is set.
func (s *Scheduler) ApplyRetention(all bool) (RunInfo, error) {
	return s.startRun(runReq{kind: RunRetention, all: all})
}

func (s *Scheduler) startRun(req runReq) (RunInfo, error) {
	req.reply = make(chan runReply, 1)
	select {
	case <-s.shutdownCh:
		return RunInfo{}, ErrStopped
	default:
	}
	select {
	case s.manualCh <- req:
	case <-s.shutdownCh:
		return RunInfo{}, ErrStopped
	}
	select {
	case r := <-req.reply:
		return r.info, r.err
	case <-s.shutdownCh:
		return RunInfo{}, ErrStopped
	}
}

// Submit queues a per-feed priority job and returns its buffered reply channel
// (the caller waits on it, a timer and Shutdown()). A late reply is dropped.
func (s *Scheduler) Submit(p Priority) (<-chan Reply, error) {
	req := priorityReq{p: p, reply: make(chan Reply, 1)}
	select {
	case <-s.shutdownCh:
		return nil, ErrStopped
	default:
	}
	select {
	case s.priorityCh <- req:
		return req.reply, nil
	case <-s.shutdownCh:
		return nil, ErrStopped
	}
}

// inDispatcher runs fn on the dispatcher goroutine after any tick that has
// already been delivered, and waits for it. Tests use it as a barrier and to
// read dispatcher-owned state without racing.
func (s *Scheduler) inDispatcher(fn func()) {
	done := make(chan struct{})
	select {
	case s.syncCh <- func() { fn(); close(done) }:
		<-done
	case <-s.stopped:
	}
}
