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

// dateFmt is the layout of the persisted local date. It sorts as text.
const dateFmt = "2006-01-02"

// nightlyPassed reports whether the nightly time of day has been reached on the
// local calendar date of now in loc (DST-safe: it builds the instant).
func nightlyPassed(now time.Time, at time.Duration, loc *time.Location) bool {
	now = now.In(loc)
	y, mo, d := now.Date()
	return !now.Before(time.Date(y, mo, d, int(at/time.Hour), int(at%time.Hour/time.Minute), 0, 0, loc))
}

// baseline is the "already covered" date for a Maint that has never run: the
// local date of start when the nightly time is already behind it (the run is
// tomorrow's), else the day before (the run is today's).
func baseline(start time.Time, at time.Duration, loc *time.Location) string {
	l := start.In(loc)
	if nightlyPassed(start, at, loc) {
		return l.Format(dateFmt)
	}
	return l.AddDate(0, 0, -1).Format(dateFmt)
}

// zone resolves the `tz` setting (design 2.6). An unknown name keeps prev (the
// zone in use) and is warned about once per distinct bad value; it is never
// treated as a zone change.
func (m *Maint) zone(ctx context.Context, prev *time.Location, badTZ *string) *time.Location {
	name := store.TZName(ctx, m.o.DB.Reader())
	loc, err := time.LoadLocation(name)
	if err != nil {
		if *badTZ != name {
			*badTZ = name
			m.log.Warn("maint: unknown tz setting, keeping the previous zone", "tz", name, "keeping", prev.String(), "err", err)
		}
		return prev
	}
	*badTZ = ""
	return loc
}

func (m *Maint) run(ctx context.Context, done chan struct{}, tick <-chan time.Time, stopTick func(), start time.Time) {
	defer close(done)
	defer stopTick()
	var badTZ string
	loc := store.LoadLocation(ctx, m.o.DB.Reader()) // an unknown name is warned about on the first tick
	// last is the local date (in the zone it ran under) of the last nightly run.
	// The nightly job runs when the local date is later than last and the time
	// of day has passed, so it runs once per local date, and a zone change can
	// neither repeat a date nor skip one. It survives restarts.
	last := baseline(start, m.o.NightlyAt, loc)
	if p := store.NightlyDate(ctx, m.o.DB.Reader()); p != "" {
		last = p // an older date means downtime over a run time: catch up on the first tick
	}
	lastCheckpoint := start
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
		loc = m.zone(ctx, loc, &badTZ)
		if today := now.In(loc).Format(dateFmt); today > last && nightlyPassed(now, m.o.NightlyAt, loc) {
			last = today
			// Recorded before the run: a crash mid-run is not retried in a loop.
			if err := m.o.DB.RecordNightlyDate(ctx, today, now.Unix()); err != nil && ctx.Err() == nil {
				m.log.Error("maint: record nightly date", "err", err)
			}
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
