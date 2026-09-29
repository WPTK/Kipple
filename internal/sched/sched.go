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
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/ftrun"
	"github.com/WPTK/kipple/internal/store"
)

// Errors returned to API callers.
var (
	ErrStopped  = errors.New("sched: scheduler is shutting down")
	ErrNotFound = errors.New("sched: no such feed")
	ErrDisabled = errors.New("sched: feed is disabled")
)

// Run kinds (design §4.1). A second manual run joins the active one; import and
// retention runs of the same kind can be active side by side.
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

	CommitTimeout time.Duration // per-commit deadline, 10 s

	// Inline full-text extraction (design §4.3).
	Extractor           ftrun.Extractor // used to build Runner when Runner is nil (tests); default: the guarded extract.Extractor
	Runner              *ftrun.Runner   // shared with the on-demand endpoint; default: built from Extractor and FulltextPerHost
	FulltextMaxItems    int             // new items queued per fetch, 20; the rest are left to on-demand
	FulltextItemTimeout time.Duration   // per article, 10 s
	FulltextPerHost     int             // concurrent articles per article host, 2
	FulltextGlobal      int             // extraction pool size = concurrent articles overall, 4 (memory guard)
	FulltextQueue       int             // items waiting for extraction, 500; beyond it items are left to on-demand
	// The same two limits while fetch.fulltext_all is on, when every feed feeds the pool: 50 and 2000
	// (never below the plain limits). Concurrency stays FulltextGlobal and FulltextPerHost.
	FulltextMaxItemsAll int
	FulltextQueueAll    int
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
	// waited is set once the job had to wait in pending: its snapshot is then
	// reloaded just before it starts (tryStart), so a change made meanwhile (URL,
	// settings, disabled, deleted) is not ignored.
	waited  bool
	runs    []*Run
	replies []chan Reply
	// personRefresh is set when a person's own refresh (not a subscribe-time fetch, say) joined this job
	// while it was already in flight; effectiveTrigger reports that for the fetch.done event.
	personRefresh bool
	// followups are priority requests that arrived while this job was running
	// and that it does not satisfy (a full refetch, a trim, a re-key). They are
	// replayed, each as a fresh job on a fresh snapshot, once this one is done.
	followups []priorityReq
	// runFollows are runs' jobs for this feed that this job cannot stand in for.
	runFollows []runFollow
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
	mutedIDs  []int64 // new items a filter muted (left out of the fetch.done ids)
	muted     int
	updated   int
	trimmed   int64
	newItems  int
	migrated  bool // the commit rewrote feeds.url (redirect migration)
	gone      bool // a 410 disabled the feed
	retry     time.Duration
	nextFetch time.Time
	cancelled bool

	commitFailed bool // the fetch completed but its commit did not
	// trimPending: the job's retention trim stopped with a full batch (a fetch
	// commit's single batch, or a trim job whose budget ran out), so the feed may
	// still be over its cap. The dispatcher queues a trim job for it.
	trimPending bool
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

	runner *ftrun.Runner
	ftq    *ftQueue // bounded background extraction queue, drained by the pool
	ftWG   sync.WaitGroup

	failCommit    func(feedID int64) error                                                                       // test hook: replaces the fetch commit
	commitFetchFn func(ctx context.Context, res *fetch.Result, perChunk time.Duration) (store.CommitInfo, error) // test hook
	fetchFn       func(ctx context.Context, snap fetch.Snapshot, now time.Time) *fetch.Result                    // test hook: replaces client.Fetch
	trimFn        func(ctx context.Context, feedID int64, b store.TrimBudget) (int64, bool, error)               // test hook: replaces the trim job
	afterCommit   func(feedID int64)                                                                             // test hook: runs once a fetch commit succeeded

	fetchCtx    context.Context
	cancelFetch context.CancelFunc
	stopOnce    sync.Once
	startOnce   sync.Once

	// dispatcher-owned
	flights   map[int64]*flight
	perHost   map[string]int
	hostUntil map[string]time.Time
	notBefore map[int64]time.Time // feeds whose commit failed: not redispatched before this
	// commitFails counts consecutive commit failures per feed (in memory only:
	// a failed write leaves the persisted counter untouched). Reset by a commit
	// that succeeds; drives the notBefore backoff.
	commitFails map[int64]int
	pending     []*flight
	// replays are the follow-ups (priority requests, run jobs) of flights dropped
	// by tryStart, replayed once drainPending has finished rebuilding pending.
	replays []*flight
	// runs are the active runs by id: every overlapping import or retention run is
	// its own entry (only a manual run is joined rather than started twice).
	runs map[int64]*Run
	// activeRuns mirrors len(runs) for Busy, which must not wait on the
	// dispatcher. Written only by the dispatcher (setRun, endRun).
	activeRuns atomic.Int32
	running    int
	live       int
	stopping   bool
	lastRunID  int64
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
	if opt.CommitTimeout <= 0 {
		opt.CommitTimeout = commitTimeout
	}
	if opt.FulltextMaxItems <= 0 {
		opt.FulltextMaxItems = defaultFTMaxItems
	}
	if opt.FulltextItemTimeout <= 0 {
		opt.FulltextItemTimeout = defaultFTItemTimeout
	}
	if opt.FulltextQueue <= 0 {
		opt.FulltextQueue = defaultFTQueue
	}
	if opt.FulltextMaxItemsAll <= 0 {
		opt.FulltextMaxItemsAll = defaultFTMaxItemsAll
	}
	opt.FulltextMaxItemsAll = max(opt.FulltextMaxItemsAll, opt.FulltextMaxItems)
	if opt.FulltextQueueAll <= 0 {
		opt.FulltextQueueAll = defaultFTQueueAll
	}
	opt.FulltextQueueAll = max(opt.FulltextQueueAll, opt.FulltextQueue)
	if opt.FulltextPerHost <= 0 {
		opt.FulltextPerHost = defaultFTPerHost
	}
	if opt.FulltextGlobal <= 0 {
		opt.FulltextGlobal = defaultFTGlobal
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
	if opt.Runner == nil {
		// Only when the caller supplies no shared Runner (tests): main builds one
		// Runner, with its own extractor, for the scheduler and the API.
		if opt.Extractor == nil {
			opt.Extractor = extract.New(extract.Options{
				Transport: client.Transport, UserAgent: client.DefaultUserAgent(), Timeout: opt.FulltextItemTimeout,
			})
		}
		opt.Runner = ftrun.New(ftrun.Options{DB: db, Extractor: opt.Extractor, PerHost: opt.FulltextPerHost, Log: log})
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		runner: opt.Runner, ftq: newFTQueue(opt.FulltextQueueAll, opt.FulltextPerHost),
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
		flights:     map[int64]*flight{},
		perHost:     map[string]int{},
		hostUntil:   map[string]time.Time{},
		notBefore:   map[int64]time.Time{},
		commitFails: map[int64]int{},
		runs:        map[int64]*Run{},
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
		s.startFulltext()
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

// Busy reports whether a run (refresh-all, import, retention) is active or the
// scheduler is stopping, for background work that yields to them (the favicon
// finder). It never waits on the dispatcher, so a loaded dispatcher cannot make
// it answer "idle" (Status does, after statusWait); a state it cannot know, the
// scheduler stopping, counts as busy.
func (s *Scheduler) Busy() bool {
	select {
	case <-s.shutdownCh:
		return true
	default:
	}
	return s.activeRuns.Load() > 0
}

// RunStatus is one active run as GET /api/status reports it.
type RunStatus struct {
	ID       int64  `json:"id,string"`
	Kind     string `json:"kind"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	NewItems int    `json:"new_items"`
	Errors   int    `json:"errors"`
}

// statusWait bounds how long Status waits for the dispatcher. A variable so a
// test can shorten it.
var statusWait = 2 * time.Second

// Status snapshots the active runs and the number of fetches in flight. It
// reads dispatcher-owned state on the dispatcher goroutine, but never blocks
// past shutdown or statusWait: an HTTP handler stuck here would hold up
// srv.Shutdown and SIGTERM. When the dispatcher cannot answer (stopped, stopping
// or busy) it returns empty.
func (s *Scheduler) Status() (runs []RunStatus, inflight int) {
	type snap struct {
		runs     []RunStatus
		inflight int
	}
	runs = []RunStatus{}
	out := make(chan snap, 1) // buffered: the dispatcher never blocks on a caller that gave up
	fn := func() {
		var sn snap
		for _, r := range s.runs {
			sn.runs = append(sn.runs, RunStatus{ID: r.ID, Kind: r.Kind, Done: r.Done, Total: r.Total, NewItems: r.NewItems, Errors: r.Errors})
		}
		for _, f := range s.flights {
			if f.started {
				sn.inflight++
			}
		}
		out <- sn
	}
	timer := time.NewTimer(statusWait)
	defer timer.Stop()
	select {
	case s.syncCh <- fn:
	case <-s.shutdownCh:
		return runs, 0
	case <-s.stopped:
		return runs, 0
	case <-timer.C:
		return runs, 0
	}
	select {
	case sn := <-out:
		runs = append(runs, sn.runs...)
		inflight = sn.inflight
	case <-s.shutdownCh:
		return runs, 0
	case <-timer.C:
		return runs, 0
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, inflight
}

// HostHolds snapshots the per-host politeness deadlines still in the future
// (host -> until). Like Status it reads dispatcher state on the dispatcher
// goroutine and gives up (returning an empty map) at shutdown or statusWait.
func (s *Scheduler) HostHolds() map[string]time.Time {
	out := make(chan map[string]time.Time, 1)
	fn := func() {
		now := s.clk.Now()
		m := map[string]time.Time{}
		for h, t := range s.hostUntil {
			if t.After(now) {
				m[h] = t
			}
		}
		out <- m
	}
	timer := time.NewTimer(statusWait)
	defer timer.Stop()
	select {
	case s.syncCh <- fn:
	case <-s.shutdownCh:
		return map[string]time.Time{}
	case <-s.stopped:
		return map[string]time.Time{}
	case <-timer.C:
		return map[string]time.Time{}
	}
	select {
	case m := <-out:
		return m
	case <-s.shutdownCh:
	case <-timer.C:
	}
	return map[string]time.Time{}
}
