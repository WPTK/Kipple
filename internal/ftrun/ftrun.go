// Package ftrun is the one place a full-text extraction of a stored item runs
// (design §4.3, §7.5). The ingest pool (internal/sched) and the on-demand
// endpoint (internal/api) both go through a shared Runner, so that
//
//   - one item is fetched once at a time: a caller that finds the item already
//     being extracted joins that run and reads its outcome;
//   - the per-article-host limit covers both paths together;
//   - the outcome is saved inside the run, before it is published to joiners,
//     so nobody sees a result that is not yet stored (and it is written once);
//   - a panic in the parser (a hostile page) becomes a stored permanent error
//     and a logged stack instead of a crashed process.
package ftrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/store"
)

// Extractor fetches and extracts one article page. *extract.Extractor
// implements it; tests substitute their own.
type Extractor interface {
	Extract(ctx context.Context, t extract.Target) (extract.Result, error)
}

const (
	defaultPerHost     = 2
	defaultSaveTimeout = 10 * time.Second
)

// ErrAborted is returned to a background caller whose context ended (shutdown)
// before the extraction finished. Nothing was stored.
var ErrAborted = errors.New("ftrun: extraction aborted")

// Options configures New.
type Options struct {
	DB        *store.DB
	Extractor Extractor
	PerHost   int // concurrent extractions per article host, default 2
	Log       *slog.Logger
}

// Runner runs extractions. It is safe for concurrent use.
type Runner struct {
	db      *store.DB
	ext     Extractor
	log     *slog.Logger
	perHost int

	mu     sync.Mutex
	calls  map[int64]*call
	busy   map[string]int
	wake   chan struct{} // closed and replaced whenever a host slot frees
	saveTO time.Duration
}

// New builds a Runner.
func New(opt Options) *Runner {
	if opt.PerHost <= 0 {
		opt.PerHost = defaultPerHost
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Runner{
		db: opt.DB, ext: opt.Extractor, log: opt.Log, perHost: opt.PerHost,
		calls: map[int64]*call{}, busy: map[string]int{}, wake: make(chan struct{}), saveTO: defaultSaveTimeout,
	}
}

// Request is one extraction of a stored item.
type Request struct {
	Item store.FulltextItem
	// Now is the time stored as the attempt time (unix seconds).
	Now int64
	// Timeout is the budget of the fetch itself, counted from when the host
	// slot is free.
	Timeout time.Duration
	// Background marks an ingest extraction. It is dropped, not stored, when
	// ctx ends, and its save is guarded: it writes only while the item still
	// has Item.URL and full text is still on for it. Without it (the on-demand
	// endpoint) the extraction is detached from ctx, which only bounds how long
	// the caller waits, and its save is unconditional.
	Background bool
}

// Outcome is what one run stored.
type Outcome struct {
	Save store.FulltextSave
	// Written is whether a row was written (a guarded save can be refused).
	Written bool
	// Joined is true when this caller waited on a run started by someone else.
	Joined bool
}

type call struct {
	done chan struct{}
	out  Outcome
	err  error
}

// Run extracts req.Item and stores the outcome, or joins the run already in
// flight for the item. The returned error is a failed save (or ErrAborted, or
// ctx's error for a joiner that gave up waiting); an extraction failure is an
// Outcome whose Save carries the error.
func (r *Runner) Run(ctx context.Context, req Request) (Outcome, error) {
	id := req.Item.ID
	r.mu.Lock()
	if c, ok := r.calls[id]; ok {
		r.mu.Unlock()
		select {
		case <-c.done:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		out := c.out
		out.Joined = true
		return out, c.err
	}
	c := &call{done: make(chan struct{})}
	r.calls[id] = c
	r.mu.Unlock()

	defer func() {
		// Also runs when lead panics past its own recover: waiters are released
		// and the id is not stuck.
		r.mu.Lock()
		delete(r.calls, id)
		r.mu.Unlock()
		close(c.done)
	}()
	c.out, c.err = r.lead(ctx, req)
	return c.out, c.err
}

func (r *Runner) lead(ctx context.Context, req Request) (Outcome, error) {
	it := req.Item
	work := ctx
	if !req.Background {
		work = context.WithoutCancel(ctx)
	}
	host := HostKey(it.URL)
	if err := r.acquire(work, host); err != nil {
		return Outcome{}, ErrAborted // only a background run's ctx can end here
	}
	defer r.release(host)

	ictx, cancel := context.WithTimeout(work, req.Timeout)
	defer cancel()
	res, err := r.safeExtract(ictx, extract.Target{
		URL: it.URL, UserAgent: it.UserAgent, RetryUserAgent: it.RetryUserAgent,
		AllowPrivate: it.AllowPrivateNet, InsecureTLS: it.AllowInsecureTLS, NoHTTP2: it.NoHTTP2,
	})
	save := store.FulltextSave{HTML: res.HTML, Text: res.Text, WordCount: res.WordCount, ImageURL: res.ImageURL, SourceURL: res.SourceURL}
	if err != nil {
		if req.Background && ctx.Err() != nil {
			return Outcome{}, ErrAborted // shutdown, not the page's fault
		}
		var ee *extract.Error
		var pe *panicError
		switch {
		case errors.As(err, &ee):
			save = store.FulltextSave{Error: ee.Msg, ErrorTransient: ee.Transient}
		case errors.As(err, &pe):
			r.log.Error("ftrun: extraction panicked", "item", it.ID, "url", it.URL, "panic", pe.val, "stack", string(pe.stack))
			save = store.FulltextSave{Error: "the page could not be processed"}
		case ictx.Err() != nil:
			save = store.FulltextSave{Error: "extraction timed out", ErrorTransient: true}
		default:
			r.log.Error("ftrun: extraction failed", "item", it.ID, "err", err)
			save = store.FulltextSave{Error: "extraction failed", ErrorTransient: true}
		}
	}

	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), r.saveTO)
	defer scancel()
	out := Outcome{Save: save}
	if req.Background {
		out.Written, err = r.db.SaveFulltextIfURL(sctx, it.ID, it.URL, req.Now, save)
	} else {
		err = r.db.SaveFulltext(sctx, it.ID, req.Now, save)
		out.Written = err == nil
	}
	return out, err
}

type panicError struct {
	val   any
	stack []byte
}

func (e *panicError) Error() string { return fmt.Sprintf("extraction panicked: %v", e.val) }

// safeExtract is Extract with a panic turned into an error.
func (r *Runner) safeExtract(ctx context.Context, t extract.Target) (res extract.Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			res, err = extract.Result{}, &panicError{val: v, stack: debug.Stack()}
		}
	}()
	return r.ext.Extract(ctx, t)
}

// HostKey is the per-article-host limit key of an article URL. The ingest
// queue in internal/sched groups by the same key, so the two limits cannot drift.
func HostKey(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "?"
}

// acquire waits for one of the host's slots.
func (r *Runner) acquire(ctx context.Context, host string) error {
	for {
		r.mu.Lock()
		if r.busy[host] < r.perHost {
			r.busy[host]++
			r.mu.Unlock()
			return nil
		}
		ch := r.wake
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Runner) release(host string) {
	r.mu.Lock()
	if r.busy[host]--; r.busy[host] <= 0 {
		delete(r.busy, host)
	}
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
}
