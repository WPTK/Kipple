package imgproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	client := &http.Client{
		Transport: h.opt.Transport(flags&FlagPrivateNet != 0, flags&FlagInsecureTLS != 0),
		Timeout:   h.opt.Timeout, // covers reading the body too
		// No cookie jar. Go adds a Referer on redirects; strip it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errors.New("imgproxy: too many redirects")
			}
			req.Header.Del("Referer")
			return nil
		},
	}
	var t *time.Timer
	if headerBudget > 0 {
		t = time.AfterFunc(headerBudget, cancel)
	}
	// #nosec G704 -- same as above: signed URL, SSRF-guarded transport, capped redirects
	resp, err := client.Do(req)
	if t != nil {
		t.Stop()
	}
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
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
}

func (s *sink) fail(kind imgcache.NegKind, status int, reason string) {
	if s == nil || s.ctx.Err() != nil {
		return
	}
	_ = s.c.PutNeg(s.key, s.orig, s.flags, kind, status, reason)
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

func (s *sink) commit(w *imgcache.Writer, contentType string, resp *http.Response, log *slog.Logger) {
	err := w.Commit(imgcache.Meta{
		ContentType: contentType, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"),
		FreshFor: imgcache.Freshness(resp.Header.Get("Cache-Control")),
	})
	if err != nil {
		log.Debug("imgproxy: cache commit", "err", err)
	}
}
