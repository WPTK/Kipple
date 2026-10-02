// Package maint is the one maintenance goroutine (design §2.6): an hourly
// passive WAL checkpoint and a nightly purge, PRAGMA optimize and snapshot,
// all under one cancellable context so shutdown interrupts whatever is running
// (design §4.10 step 5). The SQL lives in internal/store; this package decides
// when to run it and keeps every purge in small batches.
package maint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/imgcache"
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
	// DefaultCatchUpDelay holds back a nightly run that was due while the server
	// was down, so it does not overlap the startup fetch burst.
	DefaultCatchUpDelay = 5 * time.Minute

	hourly   = time.Hour
	tickEach = time.Minute
)

// Job is the summary of one maintenance job, logged and handed to OnJob.
type Job struct {
	Name     string // checkpoint, purge_stubs, purge_ledger, purge_sessions, purge_devices, auto_read, imgcache_sweep, optimize, snapshot
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
	// ImgCache, if set, gets its idle-expiry sweep and index VACUUM in the nightly
	// job (job name imgcache_sweep). Optional.
	ImgCache *imgcache.Cache

	BatchSize int           // default DefaultBatchSize
	Pause     time.Duration // between batches; default DefaultPause
	NightlyAt time.Duration // time of day in the tz setting; default DefaultNightlyAt
	// CatchUpDelay is how long after Start a run that was already due before
	// Start waits; default DefaultCatchUpDelay, negative for none.
	CatchUpDelay time.Duration

	// OnAutoRead, if set, is called after each committed batch of the auto-read step with the ids
	// marked read, with no lock held (the server publishes items.state and counts). It can also be
	// set after New with SetOnAutoRead.
	OnAutoRead func(store.StateResult)
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
	if o.CatchUpDelay == 0 {
		o.CatchUpDelay = DefaultCatchUpDelay
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
	// The zone is read here too: the baseline depends on it, so reading it in the
	// goroutine let a tz change made right after Start pick a different baseline.
	start := m.clk.Now()
	go m.run(ctx, m.done, tick, stopTick, start, store.Zone(ctx, m.o.DB.Reader()))
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

// baseline is the instant of the run "already covered" for a Maint that has never
// run: start itself when the nightly time is already behind it (the run is
// tomorrow's), else the same time yesterday (the run is today's). Being an
// instant, it reads as the right local date in whatever zone is asked.
func baseline(start time.Time, at time.Duration, loc *time.Location) time.Time {
	if nightlyPassed(start, at, loc) {
		return start
	}
	return start.AddDate(0, 0, -1)
}

// lastRun is the instant of the last nightly run: the recorded one, else (a
// database from before the instant was stored) the nightly time on the recorded
// local date, else the baseline.
func (m *Maint) lastRun(ctx context.Context, start time.Time, loc *time.Location) time.Time {
	q := m.o.DB.Reader()
	if t, ok := store.NightlyAt(ctx, q); ok {
		return t
	}
	if d := store.NightlyDate(ctx, q); d != "" {
		if day, err := time.ParseInLocation(dateFmt, d, loc); err == nil {
			y, mo, dd := day.Date()
			return time.Date(y, mo, dd, int(m.o.NightlyAt/time.Hour), int(m.o.NightlyAt%time.Hour/time.Minute), 0, 0, loc)
		}
	}
	return baseline(start, m.o.NightlyAt, loc)
}

// zone resolves the time zone (design 2.6; the `tz` setting: store.ZoneName). An unknown name keeps prev
// (the zone in use) and is warned about once per distinct bad value; it is
// never treated as a zone change.
func (m *Maint) zone(ctx context.Context, prev *time.Location, badTZ *string) *time.Location {
	name := store.ZoneName(ctx, m.o.DB.Reader())
	loc, err := store.LoadZone(name)
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

func (m *Maint) run(ctx context.Context, done chan struct{}, tick <-chan time.Time, stopTick func(), start time.Time, loc *time.Location) {
	defer close(done)
	defer stopTick()
	var badTZ string
	// last is the instant of the last nightly run. The job runs when the local
	// date now is later than the date of that instant IN THE ZONE NOW IN USE and the
	// time of day has passed, so it runs once per local date, and a zone change
	// (even to one further behind, where the old zone's date would read as
	// "tomorrow") neither repeats a date nor skips one. It survives restarts.
	last := m.lastRun(ctx, start, loc)
	// catchUp: a run was already due when the process started (it was down over a
	// run time). Only that one waits (CatchUpDelay); a run that falls due later,
	// a zone change included, does not.
	catchUp := m.o.CatchUpDelay > 0 && start.In(loc).Format(dateFmt) > last.In(loc).Format(dateFmt) && nightlyPassed(start, m.o.NightlyAt, loc)
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
		today := now.In(loc).Format(dateFmt)
		if today > last.In(loc).Format(dateFmt) && nightlyPassed(now, m.o.NightlyAt, loc) {
			// A run that was already due before this process started waits a few
			// minutes so it does not overlap the startup fetch burst.
			if catchUp && now.Sub(start) < m.o.CatchUpDelay {
				continue
			}
			catchUp = false
			last = now
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
	m.purge(ctx, "purge_devices", func() (int64, error) { return db.PurgeDevices(ctx, unix, m.o.BatchSize) })
	if ctx.Err() != nil {
		return
	}
	m.autoRead(ctx, now)
	if ctx.Err() != nil {
		return
	}
	if ic := m.o.ImgCache; ic != nil {
		began := time.Now()
		r, err := ic.Sweep(ctx, true)
		m.finish(Job{Name: "imgcache_sweep", Rows: r.Rows(), Batches: 1, Err: err}, began)
		if ctx.Err() != nil {
			return
		}
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

// SetOnAutoRead installs the callback of Options.OnAutoRead (main wires it once the API exists,
// which is after the maintenance goroutine has started).
func (m *Maint) SetOnAutoRead(fn func(store.StateResult)) {
	m.mu.Lock()
	m.o.OnAutoRead = fn
	m.mu.Unlock()
}

func (m *Maint) onAutoRead() func(store.StateResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.o.OnAutoRead
}

// autoRead is the nightly auto-read step (design 5.4a): it marks read the unread, unstarred,
// unmuted articles whose crawl time crossed each feed's threshold since the last run, the window
// (lastRun - N days, now - N days]. A run that was missed while the server was down is covered
// because the window starts at the last run that completed. A step that fails or is interrupted
// does not advance that, but each feed keeps its own high-water mark (sys.auto_read_feed_marks, see
// store.RunAutoRead): feeds that already finished their window are not repeated, so a manual
// mark-unread in one of them still sticks, and the feeds that did not finish are. With no
// recorded run (first night after the upgrade) the window is empty and only the instant is
// recorded, so enabling the feature never marks history behind the reader's back: the explicit
// "catch up" (POST /api/library/auto-read/run) does that, after a preview.
func (m *Maint) autoRead(ctx context.Context, now time.Time) {
	began := time.Now()
	db := m.o.DB
	since := now
	// A failed settings read must end the step without recording a run: read as "never ran" it
	// would give an empty window that recording the run then closes for good.
	last, ok, err := store.AutoReadLastRun(ctx, db.Reader())
	if err != nil {
		m.finish(Job{Name: "auto_read", Err: fmt.Errorf("read last run: %w", err)}, began)
		return
	}
	if ok && last.Before(now) {
		since = last
	}
	res, err := db.RunAutoRead(ctx, store.AutoReadOptions{
		Now: now, Since: since, Pause: m.o.Pause, PerFeedMarks: true, OnBatch: func(r store.StateResult) {
			if fn := m.onAutoRead(); fn != nil {
				fn(r)
			}
		},
	})
	if err == nil {
		err = db.RecordAutoReadRun(ctx, now)
	}
	if err == nil && (res.Items > 0 || res.Ledger > 0) {
		m.log.Info("maint: auto-read", "items", res.Items, "ledger", res.Ledger, "feeds", res.Feeds, "since", since.UTC().Format(time.RFC3339))
	}
	m.finish(Job{Name: "auto_read", Rows: int64(res.Items + res.Ledger), Batches: res.Batches, Err: err}, began)
}
