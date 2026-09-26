package favicon

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
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
	// (fetch.Client.Transport), so the SSRF check and allow_private_net apply.
	Guard     func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper
	UserAgent string // the client default, used when the feed resolves to ""
	Clock     clock.Clock
	Logger    *slog.Logger
	// Busy, when set, is asked before each lookup; true (a refresh-all, import
	// or retention run is active) ends the pass so the scheduler's run has the
	// network and the writer to itself.
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
	return &Finder{opt: opt, clk: opt.Clock, log: opt.Logger, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Start launches the loop. The first pass waits for the first poll tick, so
// startup is left to the scheduler.
func (f *Finder) Start() {
	f.start.Do(func() { go f.loop() })
}

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
	for n := 0; n < f.opt.Batch; n++ {
		if f.ctx.Err() != nil || (f.opt.Busy != nil && f.opt.Busy()) {
			return
		}
		did, err := f.RunOnce(f.ctx)
		if err != nil {
			if f.ctx.Err() == nil {
				f.log.Warn("favicon: lookup pass stopped", "err", err)
			}
			return
		}
		if !did {
			return
		}
		t := time.NewTimer(f.opt.Gap)
		select {
		case <-f.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// RunOnce looks up the icon of the most overdue feed, if any, and records the
// outcome. did is false when nothing was due. err is a database error or ctx
// ending; a failed lookup is not an error (it is recorded and backed off).
func (f *Finder) RunOnce(ctx context.Context) (did bool, err error) {
	now := f.clk.Now()
	job, ok, err := f.opt.DB.NextIconJob(ctx, f.opt.DB.FetchSettings(ctx), now.Unix())
	if err != nil || !ok {
		return false, err
	}
	ua := job.UserAgent
	if ua == "" {
		ua = f.opt.UserAgent
	}
	lctx, cancel := context.WithTimeout(ctx, jobTimeout)
	icon, lerr := Lookup(lctx, Request{
		SiteURL: job.SiteURL, FeedURL: job.FeedURL,
		Transport: f.opt.Guard(job.AllowPrivateNet, job.AllowInsecureTLS, job.DisableHTTP2),
		UserAgent: ua, RetryUA: job.RetryUserAgent,
	})
	cancel()
	if ctx.Err() != nil {
		return false, ctx.Err() // shutdown: write nothing, the feed stays due
	}
	now = f.clk.Now()
	if lerr != nil {
		failures := job.Failures + 1
		f.log.Debug("favicon: no icon", "feed", job.FeedID, "failures", failures, "err", lerr)
		return true, f.opt.DB.SaveIconCheck(ctx, job, nil, truncate(lerr.Error(), 500), failures,
			now.Unix(), NextCheck(now, false, failures).Unix())
	}
	f.log.Debug("favicon: stored", "feed", job.FeedID, "source", icon.SourceURL, "type", icon.ContentType, "bytes", len(icon.Data))
	return true, f.opt.DB.SaveIconCheck(ctx, job,
		&store.IconResult{Data: icon.Data, ContentType: icon.ContentType, SourceURL: icon.SourceURL, Hash: icon.Hash},
		"", 0, now.Unix(), NextCheck(now, true, 0).Unix())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
