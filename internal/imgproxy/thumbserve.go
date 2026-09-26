package imgproxy

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/WPTK/kipple/internal/imgcache"
)

const srcTagPrefix = "src=" // the thumbnail's etag column holds the source's checksum

// discard is a ResponseWriter that throws the body away: a thumbnail request
// runs the ordinary fetch-and-cache path for the original through it.
type discard struct{ h http.Header }

func (d *discard) Header() http.Header {
	if d.h == nil {
		d.h = http.Header{}
	}
	return d.h
}
func (d *discard) Write(p []byte) (int, error) { return len(p), nil }
func (d *discard) WriteHeader(int)             {}

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
	okey := imgcache.KeyOrig(flags, orig)
	original := func() { h.serveCached(w, r, u, flags, orig) }
	for round := 0; round < maxFlightRounds; round++ {
		te, ok, err := c.Lookup(ctx, tkey)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("imgproxy: cache lookup", "err", err)
			original()
			return
		}
		var stale *imgcache.Entry
		if ok {
			switch {
			case !te.OK:
				original() // thumbnailing was refused for this image, and not long ago
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
				original()
				return
			}
		}
		// A previous leader may have finished between our lookup and our Flight.
		if e2, ok2 := c.Peek(ctx, tkey); ok2 && (!e2.OK || e2.Fresh(c.Now())) {
			release()
			continue
		}
		oe, ok := h.ensureOriginal(r, u, flags, orig, okey)
		if !ok {
			release()
			original()
			return
		}
		tag := srcTag(oe)
		if stale != nil && stale.ETag == tag {
			// The source is the one this thumbnail was made from: it is still good.
			_ = c.Revalidated(tkey, thumbFresh(c, oe), tag, "")
			release()
			continue
		}
		if !h.thumbnailable(oe) {
			h.rememberNoThumb(tkey, orig, flags, "the source is served as it is")
			release()
			original()
			return
		}
		job := func() { h.runThumb(tkey, okey, orig, flags, oe, release) }
		if err := h.pool.submit(job); err != nil {
			release()
			h.log.Debug("imgproxy: thumbnail queue full, serving the original", "host", u.Host)
			original()
			return
		}
		t := time.NewTimer(h.opt.ThumbWait)
		select {
		case <-done:
			t.Stop()
			continue
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			original() // the worker goes on and the next request finds the thumbnail
			return
		}
	}
	original()
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
// accepted when the source is failing); a missing one is fetched. It reports
// false when there is no cached original afterwards (a failure, an oversize
// image, a full disk): the caller then answers with the ordinary path.
func (h *Handler) ensureOriginal(r *http.Request, u *url.URL, flags int, orig, okey string) (imgcache.Entry, bool) {
	c := h.opt.Cache
	ctx := r.Context()
	if e, ok := c.Peek(ctx, okey); ok && e.OK && e.Fresh(c.Now()) {
		return e, true
	}
	r2 := r.Clone(ctx)
	for _, k := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		r2.Header.Del(k)
	}
	func() {
		defer func() {
			// relay cuts the connection with this panic when the source dies mid-body.
			if v := recover(); v != nil && !errors.Is(asError(v), http.ErrAbortHandler) {
				panic(v)
			}
		}()
		h.serveCached(&discard{}, r2, u, flags, orig)
	}()
	e, ok := c.Peek(ctx, okey)
	return e, ok && e.OK
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
	out, ct, err := transcode(f, oe.Size, oe.ContentType, h.lim)
	_ = f.Close()
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
		if !errors.Is(err, imgcache.ErrDisabled) {
			// A full disk or the like: back off (10 minutes, doubling) instead of transcoding on every request.
			_ = c.PutNegVariant(tkey, orig, flags, imgcache.VariantThumb, imgcache.NegTransient, 0, "could not store the thumbnail")
		}
		return
	}
	if _, err := cw.Write(out); err != nil {
		cw.Abort()
		return
	}
	if err := cw.Commit(imgcache.Meta{
		ContentType: ct, ETag: srcTag(oe), Variant: imgcache.VariantThumb, FreshFor: thumbFresh(c, oe),
	}); err != nil {
		h.log.Debug("imgproxy: thumbnail commit", "err", err)
	}
}
