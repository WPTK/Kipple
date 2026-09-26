package imgproxy

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/WPTK/kipple/internal/imgcache"
)

const srcTagPrefix = "src=" // the thumbnail's etag column holds the source's checksum

// serveThumb serves the card thumbnail of orig. flags are the fetch flags with
// the thumbnail bit already removed.
//
// A fresh thumbnail is served from disk. Otherwise one leader per image makes
// sure the original is cached (the usual fetch path, single flight and all),
// hands the transcode to the worker pool and waits a few seconds for it. Every
// way of not having a thumbnail (queue full, too slow, too big, undecodable,
// animated, not smaller) answers with the original instead; the outcomes that
// will not change are remembered so the work is not repeated.
func (h *Handler) serveThumb(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig string) {
	c := h.opt.Cache
	ctx := r.Context()
	tkey := imgcache.KeyThumb(flags, orig)
	for round := 0; round < maxFlightRounds; round++ {
		te, ok, err := c.Lookup(ctx, tkey)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("imgproxy: cache lookup", "err", err)
			h.serveCached(w, r, u, flags, orig)
			return
		}
		var stale *imgcache.Entry
		if ok {
			switch {
			case !te.OK && te.NegReason == imgcache.InProgress:
				// A transcode is running (its flight is below), or one died with the process (the leader finds out).
			case !te.OK:
				h.serveCached(w, r, u, flags, orig) // thumbnailing was refused for this image, and not long ago
				return
			case te.Fresh(c.Now()):
				if h.serveHit(w, r, te) {
					return
				}
			default:
				stale = &te
			}
		}
		leader, done, release := c.Flight(tkey)
		if !leader {
			t := time.NewTimer(h.opt.ThumbWait)
			select {
			case <-done:
				t.Stop()
				continue
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				h.serveCached(w, r, u, flags, orig)
				return
			}
		}
		if h.leadThumb(w, r, u, flags, orig, stale, release, done) {
			return
		}
	}
	h.serveCached(w, r, u, flags, orig)
}

// leadThumb is the thumbnail leader's work. It reports true when the request
// has been answered and false when the caller should look the key up again.
// The flight is released on every path, a panic included, unless the job was
// handed to a worker (which then releases it).
func (h *Handler) leadThumb(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig string, stale *imgcache.Entry, release func(), done <-chan struct{}) bool {
	c := h.opt.Cache
	ctx := r.Context()
	tkey := imgcache.KeyThumb(flags, orig)
	okey := imgcache.KeyOrig(flags, orig)
	owned := true
	defer func() {
		if owned {
			release()
		}
	}()
	original := func() bool {
		owned = false
		release()
		h.serveCached(w, r, u, flags, orig)
		return true
	}
	callHook(&testHookThumbLeader)
	// A previous leader may have finished between our lookup and our Flight.
	if e2, ok2 := c.Peek(ctx, tkey); ok2 && (!e2.OK || e2.Fresh(c.Now())) {
		if !e2.OK && e2.NegReason == imgcache.InProgress {
			// We lead, so no transcode of this image is running in this process:
			// the marker was left by one that died with the process (an
			// out-of-memory kill records nothing else). Serve the original until
			// the marker expires; the next attempt backs off longer.
			return original()
		}
		return false
	}
	oe, ok := h.ensureOriginal(w, r, u, flags, orig, okey)
	if !ok {
		return true // answered: the original streamed, or the failure replayed
	}
	tag := srcTag(oe)
	if stale != nil && stale.ETag == tag {
		// The source is the one this thumbnail was made from: it is still good.
		_ = c.Revalidated(tkey, thumbFresh(c, oe), tag, "")
		return false
	}
	if !h.thumbnailable(oe) {
		h.rememberNoThumb(tkey, orig, flags, "the source is served as it is")
		return original()
	}
	job := func() { h.runThumb(tkey, okey, orig, flags, oe, release) }
	if err := h.pool.submit(job); err != nil {
		h.log.Debug("imgproxy: thumbnail queue full, serving the original", "host", u.Host)
		return original()
	}
	owned = false // the worker releases it
	t := time.NewTimer(h.opt.ThumbWait)
	defer t.Stop()
	select {
	case <-done:
		return false
	case <-ctx.Done():
		return true
	case <-t.C:
		h.serveCached(w, r, u, flags, orig) // the worker goes on and the next request finds the thumbnail
		return true
	}
}

// thumbnailable is the cheap pre-check: the type and the size, no decoding.
func (h *Handler) thumbnailable(oe imgcache.Entry) bool {
	switch oe.ContentType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return false
	}
	return oe.Size >= thumbMinSource
}

func (h *Handler) rememberNoThumb(tkey, orig string, flags int, reason string) {
	_ = h.opt.Cache.PutNegVariant(tkey, orig, flags, imgcache.VariantThumb, imgcache.NegPermanent, 0, reason)
}

func srcTag(oe imgcache.Entry) string {
	s := oe.SHA256
	if len(s) > 16 {
		s = s[:16]
	}
	return srcTagPrefix + s
}

// thumbFresh keeps a thumbnail fresh as long as its original is, but at least an hour.
func thumbFresh(c *imgcache.Cache, oe imgcache.Entry) time.Duration {
	return max(oe.FreshUntil.Sub(c.Now()), time.Hour)
}

// ensureOriginal makes sure the original is in the cache and returns its entry.
// A stale original is revalidated through the normal path (a stale copy is
// accepted when the source is failing); a missing one is fetched. The path
// runs into a probe that keeps its answer from the client.
//
// It reports false when there is no cached original afterwards, and then the
// request has been answered: when the path could not cache (low disk, a
// failed commit) the probe forwarded the original it was fetching to the
// client; otherwise its failure (502, 415, or 503 for no fetch slot) is
// replayed as it is. The source is never fetched twice and a slot never
// waited for twice.
func (h *Handler) ensureOriginal(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig, okey string) (imgcache.Entry, bool) {
	c := h.opt.Cache
	ctx := r.Context()
	if e, ok := c.Peek(ctx, okey); ok && e.OK && e.Fresh(c.Now()) {
		return e, true
	}
	r2 := r.Clone(ctx)
	for _, k := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		r2.Header.Del(k)
	}
	p := &probe{real: w}
	func() {
		defer func() {
			// The relay cuts the connection with this panic when the source dies
			// mid-body. Swallowed only while nothing has reached the client.
			if v := recover(); v != nil && (p.fwd || !errors.Is(asError(v), http.ErrAbortHandler)) {
				panic(v)
			}
		}()
		h.serveCached(p, r2, u, flags, orig)
	}()
	if p.fwd {
		return imgcache.Entry{}, false
	}
	if e, ok := c.Peek(ctx, okey); ok && e.OK {
		return e, true
	}
	if ctx.Err() != nil {
		return imgcache.Entry{}, false
	}
	if p.code == 0 || p.code == http.StatusOK {
		fail(w, http.StatusBadGateway) // the body failed after its status: nothing cached, nothing sent
	} else {
		p.replay()
	}
	return imgcache.Entry{}, false
}

func asError(v any) error {
	err, _ := v.(error)
	return err
}

// runThumb is one transcode, on a pool worker. It always releases the flight.
func (h *Handler) runThumb(tkey, okey, orig string, flags int, oe imgcache.Entry, release func()) {
	defer release()
	c := h.opt.Cache
	defer func() {
		if v := recover(); v != nil {
			h.log.Error("imgproxy: thumbnail panic", "panic", v)
			h.rememberNoThumb(tkey, orig, flags, "the decoder failed")
		}
	}()
	f, err := c.OpenFile(okey)
	if err != nil {
		return // evicted meanwhile: the next request starts over
	}
	defer f.Close()
	// The marker goes in before any decode and every outcome below replaces
	// it. If the decode takes the process down (an out-of-memory kill runs no
	// defer), the marker is what the next start finds, and the thumbnail is not
	// retried in a crash loop (serveThumb: 10 minutes, doubling per repeat).
	if err := c.PutInProgress(tkey, orig, flags, imgcache.VariantThumb); err != nil {
		h.log.Debug("imgproxy: thumbnail marker", "err", err)
	}
	backOff := func(reason string) {
		// A full disk or the like: back off (10 minutes, doubling) instead of transcoding on every request.
		if err := c.PutNegVariant(tkey, orig, flags, imgcache.VariantThumb, imgcache.NegTransient, 0, reason); err != nil && !errors.Is(err, imgcache.ErrDisabled) {
			h.log.Debug("imgproxy: thumbnail failure record", "err", err)
		}
	}
	out, ct, err := transcode(f, oe.Size, oe.ContentType, h.lim)
	if err != nil {
		var pe *passError
		reason := "the transcode failed"
		if errors.As(err, &pe) {
			reason = pe.reason
		}
		h.rememberNoThumb(tkey, orig, flags, reason)
		return
	}
	cw, err := c.Begin(tkey, orig, flags, int64(len(out)))
	if err != nil {
		h.log.Debug("imgproxy: thumbnail not cached", "err", err)
		backOff("could not store the thumbnail")
		return
	}
	if _, err := cw.Write(out); err != nil {
		cw.Abort()
		backOff("could not store the thumbnail")
		return
	}
	if err := cw.Commit(imgcache.Meta{
		ContentType: ct, ETag: srcTag(oe), Variant: imgcache.VariantThumb, FreshFor: thumbFresh(c, oe),
	}); err != nil {
		h.log.Debug("imgproxy: thumbnail commit", "err", err)
		backOff("could not store the thumbnail")
	}
}
