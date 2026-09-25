// Package maint is the one maintenance goroutine (design §2.6): an hourly
// passive WAL checkpoint and a nightly purge, PRAGMA optimize and snapshot,
// all under one cancellable context so shutdown interrupts whatever is running
// (design §4.10 step 5). The SQL lives in internal/store; this package decides
// when to run it and keeps every purge in small batches.
package maint

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/store"
)

// Defaults.
const (
	// DefaultBatchSize is the rows deleted per write transaction. Stubs are
	// about article-sized, so 1000 is a few MB and a few tens of ms.
	DefaultBatchSize = 1000
	// DefaultPause is the yield between batches: the commit gate and the
	// writer are free during it, so a queued fetch commit or edit-tag runs.
	DefaultPause = 25 * time.Millisecond
	// DefaultNightlyAt is the time of day of the nightly job (04:10 in the
	// `tz` setting, design §2.6), as an offset from midnight.
	DefaultNightlyAt = 4*time.Hour + 10*time.Minute

	hourly   = time.Hour
	tickEach = time.Minute
)

// Job is the summary of one maintenance job, logged and handed to OnJob.
type Job struct {
	Name     string // checkpoint, purge_stubs, purge_ledger, purge_sessions, optimize, snapshot
	Rows     int64  // rows purged (frames checkpointed for the checkpoint)
	Batches  int
	Duration time.Duration // wall clock
	Err      error
}

// Options configures New. Only DB is required.
type Options struct {
	DB     *store.DB
	Clock  clock.Clock // defaults to the store's clock
	Logger *slog.Logger

	BatchSize int           // default DefaultBatchSize
	Pause     time.Duration // between batches; default DefaultPause
	NightlyAt time.Duration // time of day in the tz setting; default DefaultNightlyAt

	// OnJob, if set, is called after every job (tests).
	OnJob func(Job)
	// AfterBatch, if set, is called after each purge batch with no lock held
	// (tests).
	AfterBatch func(job string, rows int64)
}

// Maint runs the maintenance jobs.
type Maint struct {
	o   Options
	clk clock.Clock
	log *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New returns a stopped Maint.
func New(o Options) *Maint {
	if o.BatchSize <= 0 {
		o.BatchSize = DefaultBatchSize
	}
	if o.Pause <= 0 {
		o.Pause = DefaultPause
	}
	if o.NightlyAt <= 0 {
		o.NightlyAt = DefaultNightlyAt
	}
	m := &Maint{o: o, clk: o.Clock, log: o.Logger}
	if m.clk == nil {
		m.clk = o.DB.Clock()
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	return m
}

// Start launches the goroutine. It is a no-op if already started.
func (m *Maint) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.done = cancel, make(chan struct{})
	// The ticker and the start time are taken here, not in the goroutine, so a
	// clock advanced right after Start can never be missed.
	tick, stopTick := m.clk.Ticker(tickEach)
	go m.run(ctx, m.done, tick, stopTick, m.clk.Now())
}

// Stop cancels the maintenance context (interrupting a running purge or
// VACUUM INTO) and waits for the goroutine to exit. Safe to call twice.
func (m *Maint) Stop() {
	m.mu.Lock()
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// nextNightly is the first occurrence of at (offset from midnight in loc)
// strictly after now.
func nextNightly(now time.Time, at time.Duration, loc *time.Location) time.Time {
	now = now.In(loc)
	y, mo, d := now.Date()
	t := time.Date(y, mo, d, int(at/time.Hour), int(at%time.Hour/time.Minute), 0, 0, loc)
	if !t.After(now) {
		t = time.Date(y, mo, d+1, int(at/time.Hour), int(at%time.Hour/time.Minute), 0, 0, loc)
	}
	return t
}

// location is the `tz` setting (design §2.6): the one time zone source for the
// nightly job and statistics. It is read at every due-check, so a change takes
// effect at the next tick. The process TZ only affects log timestamps.
func (m *Maint) location(ctx context.Context) *time.Location {
	return store.LoadLocation(ctx, m.o.DB.Reader())
}

func (m *Maint) run(ctx context.Context, done chan struct{}, tick <-chan time.Time, stopTick func(), start time.Time) {
	defer close(done)
	defer stopTick()
	loc := m.location(ctx)
	lastCheckpoint, next := start, nextNightly(start, m.o.NightlyAt, loc)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
		now := m.clk.Now()
		if now.Sub(lastCheckpoint) >= hourly {
			lastCheckpoint = now
			m.checkpoint(ctx)
		}
		// A changed tz moves the pending run to its next 04:10 in the new zone.
		if cur := m.location(ctx); cur.String() != loc.String() {
			loc = cur
			next = nextNightly(now, m.o.NightlyAt, loc)
		}
		if !now.Before(next) {
			next = nextNightly(now, m.o.NightlyAt, loc)
			m.nightly(ctx, now, loc)
		}
	}
}

func (m *Maint) finish(j Job, began time.Time) {
	j.Duration = time.Since(began)
	if j.Err != nil && ctxErr(j.Err) {
		m.log.Info("maint: job interrupted", "job", j.Name, "rows", j.Rows, "batches", j.Batches)
	} else if j.Err != nil {
		m.log.Error("maint: job failed", "job", j.Name, "rows", j.Rows, "batches", j.Batches, "duration", j.Duration.String(), "err", j.Err)
	} else {
		m.log.Info("maint: job done", "job", j.Name, "rows", j.Rows, "batches", j.Batches, "duration", j.Duration.String())
	}
	if m.o.OnJob != nil {
		m.o.OnJob(j)
	}
}

func ctxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (m *Maint) checkpoint(ctx context.Context) {
	began := time.Now()
	frames, err := m.o.DB.CheckpointPassive(ctx)
	m.finish(Job{Name: "checkpoint", Rows: frames, Batches: 1, Err: err}, began)
}

// nightly runs the design §2.6 nightly sequence. A failed step is logged and
// does not stop the later ones; a cancelled context stops everything.
func (m *Maint) nightly(ctx context.Context, now time.Time, loc *time.Location) {
	unix := now.Unix()
	db := m.o.DB
	m.purge(ctx, "purge_stubs", func() (int64, error) { return db.PurgeStubs(ctx, unix, m.o.BatchSize) })
	m.purge(ctx, "purge_ledger", func() (int64, error) { return db.PurgeLedger(ctx, unix, m.o.BatchSize) })
	m.purge(ctx, "purge_sessions", func() (int64, error) { return db.PurgeSessions(ctx, unix, m.o.BatchSize) })
	if ctx.Err() != nil {
		return
	}
	began := time.Now()
	m.finish(Job{Name: "optimize", Batches: 1, Err: db.Optimize(ctx)}, began)
	if ctx.Err() != nil {
		return
	}
	// FTS integrity-check runs on Sundays, against the snapshot file (design §2.4).
	began = time.Now()
	_, err := db.WriteSnapshot(ctx, unix, now.In(loc).Weekday() == time.Sunday)
	m.finish(Job{Name: "snapshot", Batches: 1, Err: err}, began)
}

// purge repeats one bounded batch until a batch comes back short, pausing
// between batches so other writers get in. It stops at once on cancellation.
func (m *Maint) purge(ctx context.Context, name string, batch func() (int64, error)) {
	if ctx.Err() != nil {
		return
	}
	began := time.Now()
	j := Job{Name: name}
	for {
		n, err := batch()
		if err != nil {
			j.Err = err
			break
		}
		j.Rows += n
		j.Batches++
		if m.o.AfterBatch != nil {
			m.o.AfterBatch(name, n)
		}
		if n < int64(m.o.BatchSize) {
			break
		}
		select {
		case <-ctx.Done():
			j.Err = ctx.Err()
		case <-time.After(m.o.Pause):
		}
		if j.Err != nil {
			break
		}
	}
	m.finish(j, began)
}
