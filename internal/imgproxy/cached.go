package imgproxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
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
	defer release()

	var cd cond
	var budget time.Duration
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
		if stale != nil && h.serveHit(w, r, *stale) {
			return // revalidation failed or timed out: the stale copy beats a broken image
		}
		sk.fail(imgcache.NegTransient, 0, errReason(err))
		fail(w, http.StatusBadGateway)
		return
	}
	defer done()
	defer resp.Body.Close()

	if stale != nil {
		switch {
		case resp.StatusCode == http.StatusNotModified:
			_ = sk.c.Revalidated(sk.key, imgcache.Freshness(resp.Header.Get("Cache-Control")), resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
			if h.serveHit(w, r, *stale) {
				return
			}
			fail(w, http.StatusBadGateway)
			return
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			if h.serveHit(w, r, *stale) {
				return
			}
		}
	}
	h.relay(w, resp, sk)
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
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Content-Security-Policy", "default-src 'none'")
	hdr.Set("Cross-Origin-Resource-Policy", "same-origin")
	var mod time.Time
	if t, err := http.ParseTime(e.LastModified); err == nil {
		mod = t
	}
	http.ServeContent(w, r, "", mod, f)
	return true
}
