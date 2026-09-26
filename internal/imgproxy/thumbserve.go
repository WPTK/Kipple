package imgproxy

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/imgcache"
)

const srcTagPrefix = "src=" // the thumbnail's etag column holds the source's checksum

// noCache is the Cache-Control of an original served at a thumbnail URL for a
// passing reason (load, a running or crashed transcode, low disk): the browser
// must ask again, and will get the thumbnail once there is one. Answering it
// with the original's "immutable" would pin the original under the thumbnail
// URL for 30 days.
const noCache = "no-cache"

// refusalCC is the Cache-Control of an original served at a thumbnail URL
// because thumbnailing it was refused: good for as long as the refusal is
// remembered (it is looked at again after that), never immutable.
func refusalCC(left time.Duration) string {
	return "private, max-age=" + strconv.FormatInt(int64(max(left, 0)/time.Second), 10)
}

// ccWriter replaces the Cache-Control of a successful response (errors keep
// their no-store).
type ccWriter struct {
	http.ResponseWriter
	cc    string
	wrote bool
}

func (c *ccWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		if code < 400 {
			c.Header().Set("Cache-Control", c.cc)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *ccWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer.
func (c *ccWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// serveOriginal answers a thumbnail request with the original, cached by the
// browser as cc says.
func (h *Handler) serveOriginal(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig, cc string) {
	h.serveCached(&ccWriter{ResponseWriter: w, cc: cc}, r, u, flags, orig)
}

// serveThumb serves the card thumbnail of orig. flags are the fetch flags with
// the thumbnail bit already removed.
//
// A fresh thumbnail is served from disk. Otherwise one leader per image makes
// sure the original is cached (the usual fetch path, single flight and all),
// hands the transcode to the worker pool and waits a few seconds for it. Every
// way of not having a thumbnail (queue full, too slow, too big, undecodable,
// animated, not smaller) answers with the original instead; the outcomes that
// will not change are remembered so the work is not repeated. Only a real
// thumbnail is sent as immutable.
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
			h.serveOriginal(w, r, u, flags, orig, noCache)
			return
		}
		var stale *imgcache.Entry
		if ok {
			switch {
			case !te.OK && te.NegReason == imgcache.InProgress:
				// A transcode is running (its flight is below), or one died with the process (the leader finds out).
			case !te.OK:
				// Thumbnailing was refused for this image, and not long ago.
				h.serveOriginal(w, r, u, flags, orig, refusalCC(te.FreshUntil.Sub(c.Now())))
				return
			case te.Fresh(c.Now()):
				// The original ages with its thumbnail: evicted first, it would
				// leave a stale thumbnail with nothing to revalidate it against.
				c.Touch(imgcache.KeyOrig(flags, orig))
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
				h.serveOriginal(w, r, u, flags, orig, noCache)
				return
			}
		}
		if h.leadThumb(w, r, u, flags, orig, stale, release, done) {
			return
		}
	}
	h.serveOriginal(w, r, u, flags, orig, noCache)
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
	original := func(cc string) bool {
		owned = false
		release()
		h.serveOriginal(w, r, u, flags, orig, cc)
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
			return original(noCache)
		}
		return false
	}
	// A stale thumbnail beats a failure when its original cannot be had (the
	// original was evicted and the source is failing): it is served and its
	// revalidation put off (10 minutes, doubling), as a stale original would
	// be. Without a fetch slot (503) it is served but not put off: that is
	// load, not the source. The back-off is recorded before the answer goes
	// out; if the file turns out to be gone, OpenFile drops the row anyway.
	fallback := func(code int) bool {
		if stale == nil {
			return false
		}
		if code != http.StatusServiceUnavailable {
			if err := c.DeferRevalidation(tkey); err != nil {
				h.log.Debug("imgproxy: deferring thumbnail revalidation", "err", err)
			}
		}
		return h.serveHit(w, r, *stale) // false: its file vanished too, and the failure is replayed
	}
	// The probe's own answer (a forwarded original, or a replayed failure) is
	// not the thumbnail either.
	oe, ok := h.ensureOriginal(&ccWriter{ResponseWriter: w, cc: noCache}, r, u, flags, orig, okey, fallback)
	if !ok {
		return true // answered: the original streamed, the failure replayed, or the stale thumbnail served
	}
	tag := srcTag(oe)
	if stale != nil && stale.ETag == tag {
		// The source is the one this thumbnail was made from: it is still good.
		if err := c.Revalidated(tkey, thumbFresh(c, oe), tag, ""); err != nil {
			h.log.Debug("imgproxy: thumbnail revalidation not recorded", "err", err)
		}
		return false
	}
	if !h.thumbnailable(oe) {
		h.rememberNoThumb(tkey, orig, flags, "the source is served as it is")
		return original(refusalCC(negPermanentTTL))
	}
	var unmarked atomic.Bool
	job := func() { h.runThumb(tkey, okey, orig, flags, oe, release, &unmarked) }
	if err := h.pool.submit(job); err != nil {
		h.log.Debug("imgproxy: thumbnail queue full, serving the original", "host", u.Host)
		return original(noCache)
	}
	owned = false // the worker releases it
	t := time.NewTimer(h.opt.ThumbWait)
	defer t.Stop()
	select {
	case <-done:
		if unmarked.Load() {
			// The worker could not write its in-progress marker (a full disk, a
			// failing index), so it did not decode: serve the original now.
			h.serveOriginal(w, r, u, flags, orig, noCache)
			return true
		}
		return false
	case <-ctx.Done():
		return true
	case <-t.C:
		h.serveOriginal(w, r, u, flags, orig, noCache) // the worker goes on and the next request finds the thumbnail
		return true
	}
}

// negPermanentTTL is how long a refusal is remembered (imgcache: NegPermanent).
const negPermanentTTL = 24 * time.Hour

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
// replayed as it is, unless fallback (when not nil) answers the request
// instead: it gets the status that would be replayed and reports whether it
// answered. The source is never fetched twice and a slot never waited for
// twice.
func (h *Handler) ensureOriginal(w http.ResponseWriter, r *http.Request, u *url.URL, flags int, orig, okey string, fallback func(code int) bool) (imgcache.Entry, bool) {
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
	code := p.code
	if code == 0 || code == http.StatusOK {
		code = http.StatusBadGateway
	}
	if fallback != nil && fallback(code) {
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
// When the in-progress marker cannot be written it does not decode at all and
// sets unmarked, and the waiting leader serves the original.
func (h *Handler) runThumb(tkey, okey, orig string, flags int, oe imgcache.Entry, release func(), unmarked *atomic.Bool) {
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
	// Without it that protection is gone, so there is no decode.
	if err := c.PutInProgress(tkey, orig, flags, imgcache.VariantThumb); err != nil {
		if now := time.Now().Unix(); now-h.markWarn.Load() >= 60 {
			h.markWarn.Store(now)
			h.log.Warn("imgproxy: cannot record a thumbnail in progress; serving originals", "err", err)
		}
		unmarked.Store(true)
		return
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
