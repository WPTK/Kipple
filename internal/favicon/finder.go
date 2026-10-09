package favicon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// Schedule (design §4.11).
const (
	RecheckAfter = 7 * 24 * time.Hour // after a success: at most weekly
	firstBackoff = 6 * time.Hour      // after the first failure; doubles, capped at RecheckAfter

	defaultPoll  = time.Minute
	defaultGap   = 2 * time.Second  // between two lookups in one pass
	defaultBatch = 30               // lookups per pass at most
	jobTimeout   = 45 * time.Second // one whole lookup

	// siteSpacing is the least time between two lookups of one site: feeds of
	// the same site due together reuse the first outcome (or wait for it).
	siteSpacing = 10 * time.Minute
)

// NextCheck is when a feed is looked up again: RecheckAfter after a success,
// else 6 h, 12 h, 1 d, 2 d, 4 d, then weekly for failures 1, 2, 3, ...
func NextCheck(now time.Time, ok bool, failures int) time.Time {
	if ok || failures <= 0 {
		return now.Add(RecheckAfter)
	}
	d := firstBackoff
	for i := 1; i < failures && d < RecheckAfter; i++ {
		d *= 2
	}
	return now.Add(min(d, RecheckAfter))
}

// Options configure a Finder. DB and Guard are required.
type Options struct {
	DB *store.DB
	// Guard returns the feed fetcher's guarded transport for a feed's flags
	// (fetch.Client.Transport), so the SSRF check applies. The flags are used
	// only for the feed's own host (ScopedTransport).
	Guard     func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper
	UserAgent func() string // the client default (read per lookup), used when the feed resolves to ""; nil sends none
	Clock     clock.Clock
	Logger    *slog.Logger
	// Busy, when set, is asked before each lookup; true (a refresh-all, import
	// or retention run is active, or the scheduler is stopping:
	// sched.Scheduler.Busy) ends the pass so the scheduler's run has the network
	// and the writer to itself. It must not block.
	Busy func() bool

	Poll  time.Duration // how often to look for due feeds, 1 min
	Gap   time.Duration // pause between lookups in one pass, 2 s
	Batch int           // lookups per pass, 30
}

// Finder is the one background goroutine that fills feed_icons.
type Finder struct {
	opt    Options
	clk    clock.Clock
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	start  sync.Once
	stop   sync.Once

	mu     sync.Mutex
	visits map[string]siteVisit // by store.IconSiteKey, the last siteSpacing only
}

// New builds a Finder; call Start to run it and Stop to end it.
func New(opt Options) *Finder {
	if opt.Clock == nil {
		opt.Clock = clock.Real{}
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Poll <= 0 {
		opt.Poll = defaultPoll
	}
	if opt.Gap <= 0 {
		opt.Gap = defaultGap
	}
	if opt.Batch <= 0 {
		opt.Batch = defaultBatch
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Finder{opt: opt, clk: opt.Clock, log: opt.Logger, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		visits: map[string]siteVisit{}}
}

// Start launches the loop. The first pass waits for the first poll tick, so
// startup is left to the scheduler.
func (f *Finder) Start() {
	f.start.Do(func() { go f.loop() })
}

// Cancel cancels any lookup in progress (nothing is written for it) and ends
// the loop without waiting for it; Stop joins it. Safe at any time.
func (f *Finder) Cancel() { f.cancel() }

// Done is closed once the loop has ended (after Stop, or a Cancel of a started
// finder).
func (f *Finder) Done() <-chan struct{} { return f.done }

// Stop cancels any lookup in progress (nothing is written for it) and waits
// for the loop to end. It is safe to call without Start and more than once.
func (f *Finder) Stop() {
	f.stop.Do(func() {
		f.cancel()
		f.start.Do(func() { close(f.done) }) // never started: nothing to wait for
		<-f.done
	})
}

func (f *Finder) loop() {
	defer close(f.done)
	tick, stopTick := f.clk.Ticker(f.opt.Poll)
	defer stopTick()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-tick:
		}
		f.pass()
	}
}

// pass runs due lookups one at a time, at most Batch of them.
func (f *Finder) pass() {
	// Gap spaces lookups that use the network; one answered from a recent
	// outcome (runOnce) needs no pause before or after it.
	var lastFetch time.Time
	gap := func() error {
		if lastFetch.IsZero() {
			return nil
		}
		d := f.opt.Gap - time.Since(lastFetch)
		if d <= 0 {
			return nil
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-f.ctx.Done():
			return f.ctx.Err()
		case <-t.C:
		}
		if f.opt.Busy != nil && f.opt.Busy() {
			return errBusy
		}
		return nil
	}
	for n := 0; n < f.opt.Batch; n++ {
		if f.ctx.Err() != nil || (f.opt.Busy != nil && f.opt.Busy()) {
			return
		}
		did, fetched, err := f.runOnce(f.ctx, gap)
		if fetched {
			lastFetch = time.Now()
		}
		if err != nil {
			if f.ctx.Err() == nil && !errors.Is(err, errBusy) {
				f.log.Warn("favicon: lookup pass stopped", "err", err)
			}
			return
		}
		if !did {
			return
		}
	}
}

// errBusy ends a pass whose scheduler became busy during the gap.
var errBusy = errors.New("favicon: the scheduler is busy")

// RunOnce looks up the icon of the most overdue feed, if any, and records the
// outcome. did is false when nothing was due. err is a database error or ctx
// ending; a failed lookup is not an error (it is recorded and backed off).
func (f *Finder) RunOnce(ctx context.Context) (did bool, err error) {
	did, _, err = f.runOnce(ctx, nil)
	return did, err
}

// siteVisit is one lookup of a site in the last siteSpacing: its outcome, and
// the network scope it ran under.
type siteVisit struct {
	at    time.Time
	scope string
	icon  *Icon // nil: it failed with lerr
	lerr  error
}

// scopeOf is the network scope a job's lookup runs under: "" for the guarded
// default, else the feed host and its exceptions (ScopedTransport). Two jobs
// with the same scope would make exactly the same requests for the same site.
func scopeOf(j store.IconJob) string {
	if (!j.AllowPrivateNet && !j.AllowInsecureTLS) || j.FeedHost == "" {
		return ""
	}
	return fmt.Sprintf("%s|%t|%t", strings.ToLower(j.FeedHost), j.AllowPrivateNet, j.AllowInsecureTLS)
}

// runOnce is RunOnce, also reporting whether it used the network. A feed whose
// site (store.IconSiteKey) was looked up in the last siteSpacing under the same
// scope takes that outcome without a request; under another scope it waits
// (NextIconJob passes it over), so one site sees at most one lookup per
// siteSpacing however many of its feeds are due (subreddits, channels).
// beforeFetch (optional) runs just before a lookup that uses the network; an
// error from it ends runOnce with nothing written (the feed stays due).
func (f *Finder) runOnce(ctx context.Context, beforeFetch func() error) (did, fetched bool, err error) {
	now := f.clk.Now()
	f.mu.Lock()
	for k, v := range f.visits {
		if now.Sub(v.at) >= siteSpacing || now.Before(v.at) {
			delete(f.visits, k)
		}
	}
	visits := maps.Clone(f.visits)
	f.mu.Unlock()
	job, ok, err := f.opt.DB.NextIconJob(ctx, f.opt.DB.FetchSettings(ctx), now.Unix(), func(j store.IconJob) bool {
		v, hit := visits[store.IconSiteKey(j.SiteURL, j.FeedURL)]
		return hit && v.scope != scopeOf(j)
	})
	if err != nil || !ok {
		return false, false, err
	}
	key, scope := store.IconSiteKey(job.SiteURL, job.FeedURL), scopeOf(job)
	var icon Icon
	var lerr error
	if v, hit := visits[key]; hit && v.scope == scope {
		if v.icon != nil {
			icon = *v.icon
		} else {
			lerr = v.lerr
		}
	} else {
		if beforeFetch != nil {
			if err := beforeFetch(); err != nil {
				return false, false, err
			}
		}
		fetched = true
		ua := job.UserAgent
		if ua == "" && f.opt.UserAgent != nil {
			ua = f.opt.UserAgent()
		}
		lctx, cancel := context.WithTimeout(ctx, jobTimeout)
		icon, lerr = Lookup(lctx, Request{
			SiteURL: job.SiteURL, FeedURL: job.FeedURL,
			Transport: fetch.ScopedTransport(f.opt.Guard, job.FeedHost, job.AllowPrivateNet, job.AllowInsecureTLS, job.DisableHTTP2),
			UserAgent: ua, RetryUA: job.RetryUserAgent,
		})
		cancel()
		if ctx.Err() != nil {
			return false, true, ctx.Err() // shutdown: write nothing, the feed stays due
		}
		v := siteVisit{at: now, scope: scope, lerr: lerr}
		if lerr == nil {
			v.icon = &icon
		}
		if key != "" {
			f.mu.Lock()
			f.visits[key] = v
			f.mu.Unlock()
		}
	}
	did = true
	err = f.record(ctx, job, icon, lerr)
	return did, fetched, err
}

// record saves one lookup's outcome for job.
func (f *Finder) record(ctx context.Context, job store.IconJob, icon Icon, lerr error) error {
	now := f.clk.Now()
	if lerr != nil {
		failures := job.Failures + 1
		f.log.Debug("favicon: no icon", "feed", job.FeedID, "failures", failures, "err", lerr)
		return f.opt.DB.SaveIconCheck(ctx, job, nil, truncate(lerr.Error(), 500), failures,
			now.Unix(), NextCheck(now, false, failures).Unix())
	}
	f.log.Debug("favicon: stored", "feed", job.FeedID, "source", icon.SourceURL, "type", icon.ContentType, "bytes", len(icon.Data))
	return f.opt.DB.SaveIconCheck(ctx, job,
		&store.IconResult{Data: icon.Data, ContentType: icon.ContentType, SourceURL: icon.SourceURL, Hash: icon.Hash},
		"", 0, now.Unix(), NextCheck(now, true, 0).Unix())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
