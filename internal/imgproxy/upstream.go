package imgproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/imgcache"
)

const defaultBrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

const browserAccept = "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"

// cond is the conditional-request state to send upstream.
type cond struct{ inm, ims string }

// profile is the shape of one upstream request. Kipple never sends the article
// URL or its own address as a Referer, and never any cookie.
//
//	ua "kipple",  referer "none": Accept image/*, Kipple's own User-Agent, no Referer (the default)
//	ua "browser", referer "none": Accept image/avif,image/webp,..., a plain desktop Chrome User-Agent, no Referer
//	ua "browser", referer "self": the same, plus Referer set to the image's own origin ("https://host/")
type profile struct{ ua, referer string }

// The retry ladder. The first attempt is the default; when the source answers
// 401, 403 or 429 (with no Retry-After and no Cloudflare challenge) the next
// shapes are tried in this order, each once.
var stdProfiles = []profile{{"kipple", "none"}, {"browser", "none"}, {"browser", "self"}}

// profileOrder puts a remembered winning shape first, then the rest of the ladder.
func profileOrder(hint imgcache.HostHint, hinted bool) []profile {
	if !hinted {
		return stdProfiles
	}
	first := profile{hint.UA, hint.Referer}
	out := []profile{first}
	for _, p := range stdProfiles {
		if p != first {
			out = append(out, p)
		}
	}
	return out
}

// hotlinkSignal reports a response that a different request shape may fix.
func hotlinkSignal(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
	default:
		return false
	}
	// A rate limit or a Cloudflare challenge is not a hotlink rule: do not hammer it.
	return resp.Header.Get("Retry-After") == "" && resp.Header.Get("Cf-Mitigated") == ""
}

// hintStore remembers, per host, the request shape that last worked. It is
// persisted in the cache index when there is one and kept in memory otherwise.
type hintStore struct {
	cache *imgcache.Cache
	mu    sync.Mutex
	mem   map[string]imgcache.HostHint
}

func (s *hintStore) get(host string) (imgcache.HostHint, bool) {
	host = strings.ToLower(host)
	if s.cache.Enabled() {
		return s.cache.HostHint(host)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.mem[host]
	return h, ok
}

func (s *hintStore) set(host string, h imgcache.HostHint) {
	host = strings.ToLower(host)
	if s.cache.Enabled() {
		_ = s.cache.SetHostHint(host, h)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mem) >= 1024 {
		s.mem = nil
	}
	if s.mem == nil {
		s.mem = map[string]imgcache.HostHint{}
	}
	if h.UA == "kipple" && h.Referer == "none" {
		delete(s.mem, host)
		return
	}
	s.mem[host] = h
}

// fetchUpstream asks the source for u through the SSRF-guarded transport and
// walks the hotlink ladder. headerBudget (when positive) bounds the wait for the
// response headers of each attempt. The caller must Close the body and call done.
func (h *Handler) fetchUpstream(ctx context.Context, u *url.URL, flags int, cd cond, headerBudget time.Duration) (*http.Response, func(), error) {
	host := u.Hostname()
	hint, hinted := h.hints.get(host)
	order := profileOrder(hint, hinted)
	for i, p := range order {
		resp, done, err := h.attempt(ctx, u, flags, cd, p, headerBudget)
		if err != nil {
			return nil, nil, err
		}
		if i < len(order)-1 && hotlinkSignal(resp) {
			h.log.Debug("imgproxy: hotlink signal, retrying", "host", host, "status", resp.StatusCode, "next_ua", order[i+1].ua, "next_referer", order[i+1].referer)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			done()
			continue
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
			h.noteWinner(host, p, hint, hinted)
		}
		return resp, done, nil
	}
	return nil, nil, errors.New("imgproxy: no attempt made") // unreachable: stdProfiles is not empty
}

// noteWinner saves the shape that worked, or forgets a hint that the default
// has outgrown.
func (h *Handler) noteWinner(host string, p profile, hint imgcache.HostHint, hinted bool) {
	def := stdProfiles[0]
	switch {
	case p == def && hinted && (hint != imgcache.HostHint{UA: def.ua, Referer: def.referer}):
		h.hints.set(host, imgcache.HostHint{UA: def.ua, Referer: def.referer})
	case p != def && (!hinted || hint != imgcache.HostHint{UA: p.ua, Referer: p.referer}):
		h.hints.set(host, imgcache.HostHint{UA: p.ua, Referer: p.referer})
	}
}

func (h *Handler) attempt(ctx context.Context, u *url.URL, flags int, cd cond, p profile, headerBudget time.Duration) (*http.Response, func(), error) {
	actx, cancel := context.WithCancel(ctx)
	// #nosec G704 -- URL is HMAC-signed by us; the transport dial guard blocks private ranges
	req, err := http.NewRequestWithContext(actx, http.MethodGet, u.String(), nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if p.ua == "browser" {
		req.Header.Set("Accept", browserAccept)
		req.Header.Set("User-Agent", h.opt.BrowserUA)
	} else {
		req.Header.Set("Accept", "image/*")
		req.Header.Set("User-Agent", h.opt.UserAgent)
	}
	if p.referer == "self" {
		req.Header.Set("Referer", u.Scheme+"://"+u.Host+"/")
	}
	if cd.inm != "" {
		req.Header.Set("If-None-Match", cd.inm)
	}
	if cd.ims != "" {
		req.Header.Set("If-Modified-Since", cd.ims)
	}
	// No Client.Timeout: it would also run while the body waits on a slow
	// client (a relay writes to the client between upstream reads). Instead the
	// headers get a deadline and the body a budget that only counts the time
	// spent waiting on the source (budgetBody); together they are Timeout.
	client := &http.Client{
		Transport: h.opt.Transport(flags&FlagPrivateNet != 0, flags&FlagInsecureTLS != 0),
		// No cookie jar. Go adds a Referer on redirects; strip it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errors.New("imgproxy: too many redirects")
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	limit := h.opt.Timeout
	if headerBudget > 0 {
		limit = min(limit, headerBudget)
	}
	var fired atomic.Bool
	t := time.AfterFunc(limit, func() { fired.Store(true); cancel() })
	start := time.Now()
	// #nosec G704 -- same as above: signed URL, SSRF-guarded transport, capped redirects
	resp, err := client.Do(req)
	stopped := t.Stop()
	if err != nil {
		cancel()
		if fired.Load() {
			return nil, nil, fmt.Errorf("%w: %w", errUpstreamTimeout, err)
		}
		return nil, nil, err
	}
	if !stopped {
		// The deadline fired as the headers arrived: the request is already cancelled.
		_ = resp.Body.Close()
		cancel()
		return nil, nil, errUpstreamTimeout
	}
	resp.Body = newBudgetBody(resp.Body, h.opt.Timeout-time.Since(start), cancel)
	return resp, cancel, nil
}

// errUpstreamTimeout is the source taking longer than Timeout (it wraps
// context.DeadlineExceeded, so failures record "timed out").
var errUpstreamTimeout = fmt.Errorf("imgproxy: upstream timed out: %w", context.DeadlineExceeded)

// budgetBody is an upstream body with a time budget that only runs inside
// Read: time the caller spends elsewhere (writing to a slow client) is not
// counted, so a slow reader never times out a fast source, while a source that
// trickles is still cut at Timeout. When the budget runs out mid-read the
// request is cancelled and the read fails with errUpstreamTimeout.
type budgetBody struct {
	rc     io.ReadCloser
	left   time.Duration
	cancel context.CancelFunc
	t      *time.Timer
	fired  atomic.Bool
	out    bool
}

func newBudgetBody(rc io.ReadCloser, left time.Duration, cancel context.CancelFunc) *budgetBody {
	b := &budgetBody{rc: rc, left: left, cancel: cancel}
	b.t = time.AfterFunc(time.Hour, func() { b.fired.Store(true); b.cancel() })
	b.t.Stop()
	return b
}

func (b *budgetBody) Read(p []byte) (int, error) {
	if b.out {
		return 0, errUpstreamTimeout
	}
	if b.left <= 0 {
		b.out = true
		b.cancel()
		return 0, errUpstreamTimeout
	}
	b.t.Reset(b.left)
	start := time.Now()
	n, err := b.rc.Read(p)
	b.t.Stop()
	b.left -= time.Since(start)
	if b.fired.Load() {
		b.out = true
		if err != nil || n == 0 {
			return n, errUpstreamTimeout
		}
	}
	return n, err
}

func (b *budgetBody) Close() error {
	b.t.Stop()
	return b.rc.Close()
}

// hostLimiter caps concurrent upstream fetches per host.
type hostLimiter struct {
	limit int
	mu    sync.Mutex
	m     map[string]*hostSlot
}

type hostSlot struct {
	ch   chan struct{}
	refs int
}

func newHostLimiter(limit int) *hostLimiter {
	return &hostLimiter{limit: limit, m: map[string]*hostSlot{}}
}

// acquire waits for a slot on host until ctx ends or expire fires.
func (l *hostLimiter) acquire(ctx context.Context, host string, expire <-chan time.Time) (func(), bool) {
	host = strings.ToLower(host)
	l.mu.Lock()
	s := l.m[host]
	if s == nil {
		s = &hostSlot{ch: make(chan struct{}, l.limit)}
		l.m[host] = s
	}
	s.refs++
	l.mu.Unlock()
	leave := func() {
		l.mu.Lock()
		s.refs--
		if s.refs == 0 {
			delete(l.m, host)
		}
		l.mu.Unlock()
	}
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch; leave() }, true
	case <-expire:
	case <-ctx.Done():
	}
	leave()
	return nil, false
}

// active is the number of hosts with a fetch running or queued (tests).
func (l *hostLimiter) active() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

// negKindFor classifies an upstream status: server errors and rate limits are
// retried soon, everything else (404, 410, 403, ...) is remembered for a day.
func negKindFor(status int) imgcache.NegKind {
	if status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout {
		return imgcache.NegTransient
	}
	return imgcache.NegPermanent
}

func negKindForErr(error) imgcache.NegKind { return imgcache.NegTransient }

// sink is where a leader's outcome goes in the cache. A nil sink (no cache)
// ignores everything.
type sink struct {
	c     *imgcache.Cache
	ctx   context.Context // the request: a cancelled request records nothing
	key   string
	orig  string
	flags int
	// release ends the leader's flight on key; the fill calls it as soon as
	// the entry (or its failure) is in the index, so followers need not wait
	// for a slow client to finish reading. It is safe to call more than once.
	release func()
}

func (s *sink) fail(kind imgcache.NegKind, status int, reason string) {
	if s == nil || s.ctx.Err() != nil {
		return
	}
	_ = s.c.PutNeg(s.key, s.orig, s.flags, kind, status, reason)
}

func (s *sink) refuse(r *refusal) { s.fail(r.kind, r.status, r.reason) }

func (s *sink) done() {
	if s != nil && s.release != nil {
		s.release()
	}
}

// begin starts the cache write; it returns nil (stream without caching) when the
// cache refuses, for example on a full disk.
func (s *sink) begin(expected int64, log *slog.Logger) *imgcache.Writer {
	if s == nil {
		return nil
	}
	w, err := s.c.Begin(s.key, s.orig, s.flags, expected)
	if err != nil {
		log.Debug("imgproxy: not caching", "err", err)
		return nil
	}
	return w
}

// commit publishes the download. The source's ETag is stored for revalidation
// (the client is never sent it: hits answer with the checksum tag).
func (s *sink) commit(w *imgcache.Writer, contentType string, resp *http.Response, log *slog.Logger) bool {
	err := w.Commit(imgcache.Meta{
		ContentType: contentType, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		FreshFor: imgcache.Freshness(resp.Header.Get("Cache-Control")),
	})
	if err != nil {
		log.Debug("imgproxy: cache commit", "err", err)
		return false
	}
	return true
}
