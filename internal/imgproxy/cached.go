package imgproxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/imgcache"
)

const maxFlightRounds = 3

// serveCached is the path with the cache on: a fresh hit is served from disk with
// no upstream contact; a stale one is revalidated (the stale copy is served
// when the source is slow or failing); a miss is fetched by one leader per
// image while everyone else waits for its file.
func (h *Handler) serveCached(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig string) {
	c := h.opt.Cache
	ctx := r.Context()
	key := imgcache.KeyOrig(flags, orig)
	sk := &sink{c: c, ctx: ctx, key: key, orig: orig, flags: flags}
	for round := 0; round < maxFlightRounds; round++ {
		e, ok, err := c.Lookup(ctx, key)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("imgproxy: cache lookup", "err", err)
			h.serveDirect(w, r, u, flags)
			return
		}
		var stale *imgcache.Entry
		if ok {
			switch {
			case !e.OK:
				serveNeg(w, e)
				return
			case e.Fresh(c.Now()):
				if h.serveHit(w, r, e) {
					return
				}
			default:
				stale = &e
			}
		}
		leader, done, release := c.Flight(key)
		if !leader {
			t := time.NewTimer(h.opt.Wait + h.opt.Timeout)
			select {
			case <-done:
				t.Stop()
				continue // look it up again: the leader's file or failure is in the index
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				busy(w)
				return
			}
		}
		defer release()
		sk.release = release
		// Another leader may have finished between our lookup and our Flight.
		if e2, ok2 := c.Peek(ctx, key); ok2 {
			switch {
			case !e2.OK:
				serveNeg(w, e2)
				return
			case e2.Fresh(c.Now()) && h.serveHit(w, r, e2):
				return
			}
			stale = &e2
		} else {
			stale = nil
		}
		h.fetchAndFill(w, r, u, flags, sk, stale)
		return
	}
	// Followers kept finding nothing (a leader that could not cache): fetch on our own.
	h.serveDirect(w, r, u, flags)
}

// fetchAndFill is the leader's work: fetch (conditionally when there is a stale
// copy), stream to the client and fill the cache.
//
// While a stale copy is being revalidated, anything but a 304 or a good new
// image (an error, a timeout, a 5xx, a 4xx, a body that is not an image) serves
// the stale copy and keeps it: the next revalidation is put off (10 minutes,
// doubling) instead of the good copy being replaced by a failure entry.
func (h *Handler) fetchAndFill(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, sk *sink, stale *imgcache.Entry) {
	ctx := r.Context()
	release, out := h.acquire(ctx, u.Hostname())
	switch out {
	case slotBusy:
		if stale != nil && h.serveHit(w, r, *stale) {
			return
		}
		busy(w)
		return
	case slotGone:
		return
	}
	slot := sync.OnceFunc(release)
	defer slot()

	// At most two exchanges: a revalidation whose 304 finds the stale file
	// evicted meanwhile is followed by one unconditional fetch.
	for attempt := 0; attempt < 2; attempt++ {
		var cd cond
		var budget time.Duration
		sk.stale = stale != nil
		if stale != nil {
			cd = cond{inm: stale.ETag, ims: stale.LastModified}
			budget = h.opt.RevalidateWithin
		}
		resp, done, err := h.fetchUpstream(ctx, u, flags, cd, budget)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Debug("imgproxy: upstream", "host", u.Host, "err", err)
			if stale != nil && h.serveStale(w, r, sk, *stale) {
				return // revalidation failed or timed out: the stale copy beats a broken image
			}
			sk.fail(imgcache.NegTransient, 0, errReason(err))
			fail(w, http.StatusBadGateway)
			return
		}
		if stale != nil && resp.StatusCode == http.StatusNotModified {
			_ = sk.c.Revalidated(sk.key, imgcache.Freshness(resp.Header.Get("Cache-Control")), resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
			_ = resp.Body.Close()
			done()
			if h.serveHit(w, r, *stale) {
				return
			}
			stale = nil // the file went (evicted) between the lookup and now: fetch it whole
			continue
		}
		h.fillFrom(w, r, sk, stale, resp, sync.OnceFunc(func() { _ = resp.Body.Close(); done(); slot() }))
		return
	}
	fail(w, http.StatusBadGateway)
}

// fillFrom vets a fetched response and streams it, or serves the stale copy
// when a revalidation got something that is not an image.
func (h *Handler) fillFrom(w http.ResponseWriter, r *http.Request, sk *sink, stale *imgcache.Entry, resp *http.Response, fin func()) {
	defer fin()
	head, ct, body, ref := h.vet(resp)
	if ref != nil {
		if stale != nil && h.serveStale(w, r, sk, *stale) {
			return
		}
		sk.refuse(ref)
		fail(w, ref.code)
		return
	}
	h.stream(w, resp, sk, head, ct, body, fin)
}

// serveStale serves a stale copy whose revalidation failed and puts the next
// revalidation off. It reports false when the file is gone.
func (h *Handler) serveStale(w http.ResponseWriter, r *http.Request, sk *sink, e imgcache.Entry) bool {
	if !h.serveHit(w, r, e) {
		return false
	}
	if err := sk.c.DeferRevalidation(sk.key); err != nil {
		h.log.Debug("imgproxy: deferring revalidation", "err", err)
	}
	return true
}

func errReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "could not reach the source"
}

// serveNeg replays a remembered failure without contacting the source.
func serveNeg(w http.ResponseWriter, e imgcache.Entry) {
	if e.NegStatus == http.StatusUnsupportedMediaType {
		fail(w, http.StatusUnsupportedMediaType)
		return
	}
	fail(w, http.StatusBadGateway)
}

// serveHit serves a cached image from disk with Range and conditional-request
// support. It reports false when the file has vanished (the entry is dropped and
// the caller refetches); nothing was written then.
func (h *Handler) serveHit(w http.ResponseWriter, r *http.Request, e imgcache.Entry) bool {
	f, err := h.opt.Cache.OpenFile(e.Key)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", e.ContentType)
	if len(e.SHA256) >= 16 {
		hdr.Set("ETag", `"`+e.SHA256[:16]+`"`)
	}
	hdr.Set("Cache-Control", cacheControl)
	setImageSecurityHeaders(hdr)
	var mod time.Time
	if t, err := http.ParseTime(e.LastModified); err == nil {
		mod = t
	}
	http.ServeContent(w, r, "", mod, f)
	return true
}

// setImageSecurityHeaders is the hardening every image response carries, hit or miss.
func setImageSecurityHeaders(hdr http.Header) {
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", "default-src 'none'")
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
}
