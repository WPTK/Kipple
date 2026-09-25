package sched

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
)

// Inline extraction limits (design §4.3, §7.5). Every one is overridable in
// Options; these are the defaults.
const (
	defaultFTMaxItems    = 20
	defaultFTItemTimeout = 10 * time.Second
	defaultFTTotal       = 60 * time.Second
	defaultFTConcurrency = 3
	defaultFTPerHost     = 2
)

// Extractor fetches and extracts one article page. *extract.Extractor
// implements it; tests substitute their own.
type Extractor interface {
	Extract(ctx context.Context, t extract.Target) (extract.Result, error)
}

// hostLimiter caps concurrent article extractions per article host across all
// workers, so a burst of feeds pointing at one site cannot hammer it.
type hostLimiter struct {
	mu    sync.Mutex
	limit int
	m     map[string]*hostSem
}

type hostSem struct {
	ch   chan struct{}
	refs int
}

func newHostLimiter(limit int) *hostLimiter {
	return &hostLimiter{limit: limit, m: map[string]*hostSem{}}
}

// acquire blocks until a slot for host is free or ctx ends. The returned
// release must be called once when ok.
func (l *hostLimiter) acquire(ctx context.Context, host string) (release func(), ok bool) {
	l.mu.Lock()
	hs := l.m[host]
	if hs == nil {
		hs = &hostSem{ch: make(chan struct{}, l.limit)}
		l.m[host] = hs
	}
	hs.refs++
	l.mu.Unlock()
	drop := func() {
		l.mu.Lock()
		if hs.refs--; hs.refs == 0 {
			delete(l.m, host)
		}
		l.mu.Unlock()
	}
	select {
	case hs.ch <- struct{}{}:
		return func() { <-hs.ch; drop() }, true
	case <-ctx.Done():
		drop()
		return nil, false
	}
}

// extractInline extracts the article pages of a feed's new items before the
// commit, so no transaction is open across the network (design §4.3). It only
// ever adds to res.Fulltext and res.Notes and never fails the fetch: an item
// that cannot be extracted is stored with its error, an item the run had no
// budget for is left for the on-demand endpoint, and both are noted in the
// fetch_log row.
//
// An ingest attempt happens once per item, when the item is new. A failure is
// stored as an error row, which the on-demand endpoint reports without
// refetching; only an explicit refresh retries it. So a broken page is never
// hit again by polling.
func (s *Scheduler) extractInline(ctx context.Context, res *fetch.Result) {
	if !res.Snap.Fulltext || res.Outcome != fetch.OutcomeOK || res.Feed == nil || len(res.Feed.Items) == 0 {
		return
	}
	if res.Snap.RekeyPending {
		// Unmatched items are inserted read after a re-key; leave them to the
		// on-demand path rather than fetching pages nobody is waiting on.
		res.Notes = append(res.Notes, "fulltext: skipped (re-key pending)")
		return
	}
	uids := make([]string, len(res.Feed.Items))
	for i, it := range res.Feed.Items {
		uids[i] = it.UID
	}
	known, err := s.db.KnownUIDs(ctx, res.Snap.ID, uids)
	if err != nil {
		s.log.Warn("sched: fulltext known uids", "feed", res.Snap.ID, "err", err)
		res.Notes = append(res.Notes, "fulltext: skipped (could not check existing items)")
		return
	}

	var cand []fetch.Item
	seen := map[string]bool{}
	for _, it := range res.Feed.Items {
		if known[it.UID] || seen[it.UID] || it.URL == "" {
			continue
		}
		seen[it.UID] = true
		cand = append(cand, it)
	}
	if len(cand) == 0 {
		return
	}
	// Newest first; an item without a date is stamped with crawl time, so it is
	// the newest. Ties keep document order.
	sort.SliceStable(cand, func(a, b int) bool {
		pa, pb := cand[a].Published, cand[b].Published
		switch {
		case pa == nil && pb == nil:
			return false
		case pa == nil:
			return true
		case pb == nil:
			return false
		}
		return pa.After(*pb)
	})
	deferred := 0
	if len(cand) > s.opt.FulltextMaxItems {
		deferred = len(cand) - s.opt.FulltextMaxItems
		cand = cand[:s.opt.FulltextMaxItems]
	}

	ctx, cancel := context.WithTimeout(ctx, s.opt.FulltextTotal)
	defer cancel()
	type out struct {
		uid string
		r   fetch.FulltextResult
		ok  bool // an outcome to record; false = not attempted or cut off
	}
	outs := make([]out, len(cand))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(s.opt.FulltextConcurrency, len(cand)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				it := cand[i]
				r, ok := s.extractOne(ctx, res.Snap, it)
				outs[i] = out{uid: it.UID, r: r, ok: ok}
			}
		}()
	}
feed:
	for i := range cand {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	res.Fulltext = map[string]fetch.FulltextResult{}
	okN, tried := 0, 0
	for _, o := range outs {
		if !o.ok {
			deferred++
			continue
		}
		tried++
		if o.r.Error == "" {
			okN++
		}
		res.Fulltext[o.uid] = o.r
	}
	res.Notes = append(res.Notes, fmt.Sprintf("fulltext: %d/%d", okN, tried))
	if deferred > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("fulltext_deferred: %d", deferred))
	}
}

// extractOne extracts one item under the per-item timeout and the per-host
// cap. ok is false when the item was cut off by the run budget or shutdown
// rather than tried, so it is deferred instead of recorded as a failure.
func (s *Scheduler) extractOne(ctx context.Context, snap fetch.Snapshot, it fetch.Item) (fetch.FulltextResult, bool) {
	host := "?"
	if u, err := url.Parse(it.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	release, ok := s.ftHosts.acquire(ctx, host)
	if !ok {
		return fetch.FulltextResult{}, false
	}
	defer release()
	ictx, cancel := context.WithTimeout(ctx, s.opt.FulltextItemTimeout)
	defer cancel()
	r, err := s.ext.Extract(ictx, extract.Target{
		URL: it.URL, UserAgent: snap.UserAgent,
		AllowPrivate: snap.AllowPrivateNet, InsecureTLS: snap.AllowInsecureTLS, NoHTTP2: snap.DisableHTTP2,
	})
	if err != nil {
		if ctx.Err() != nil {
			return fetch.FulltextResult{}, false // run budget or shutdown, not the page's fault
		}
		var ee *extract.Error
		msg := "extraction failed"
		if errors.As(err, &ee) {
			msg = ee.Msg
		} else if ictx.Err() != nil {
			msg = "extraction timed out"
		}
		return fetch.FulltextResult{Error: msg}, true
	}
	return fetch.FulltextResult{HTML: r.HTML, Text: r.Text, WordCount: r.WordCount, ImageURL: r.ImageURL, SourceURL: r.SourceURL}, true
}
