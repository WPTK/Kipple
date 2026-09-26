package api

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/WPTK/kipple/internal/imgproxy"
	"github.com/WPTK/kipple/internal/sanitize"
	"github.com/WPTK/kipple/internal/store"
)

// imageSecretTTL bounds how stale the cached image secret may be. A list of 50
// items would otherwise read the account row 50 times; a rotation is still
// noticed within this long.
const imageSecretTTL = time.Second

// imageSecret returns the account secret that keys image proxy signatures. It is
// cached for imageSecretTTL and re-read after that, and the proxy handler is
// rebuilt when it changed: `kipple password` rotates the secret from another
// process while the server runs, and old signed image URLs must stop verifying
// within a second. A failed read falls back to the last good value.
func (s *Server) imageSecret(ctx context.Context) ([]byte, bool) {
	s.imgMu.Lock()
	if s.imgSecret != nil && s.now().Sub(s.imgSecretAt) < imageSecretTTL {
		defer s.imgMu.Unlock()
		return s.imgSecret, true
	}
	s.imgMu.Unlock()
	secret, ok, err := s.db.AccountSecret(ctx)
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if err != nil {
		s.log.Error("api: image secret", "err", err)
		return s.imgSecret, s.imgSecret != nil
	}
	if !ok || secret == "" {
		return nil, false
	}
	if s.imgSecret == nil || string(s.imgSecret) != secret {
		s.imgSecret = []byte(secret)
		if old := s.imgH; old != nil { // keyed by the old secret; in-flight requests finish, queued thumbnails drain
			go old.Close()
		}
		s.imgH = nil
	}
	s.imgSecretAt = s.now()
	return s.imgSecret, true
}

// imageHandler builds the proxy handler on first use (it needs the secret).
func (s *Server) imageHandler(ctx context.Context) (*imgproxy.Handler, bool) {
	secret, ok := s.imageSecret(ctx)
	if !ok {
		return nil, false
	}
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.imgH == nil || !bytes.Equal(s.imgHSecret, secret) {
		s.imgHSecret = secret
		if s.imgH != nil {
			go s.imgH.Close() // keyed by the old secret; its queued thumbnails finish first
		}
		s.imgH = imgproxy.New(imgproxy.Options{
			Secret: secret, UserAgent: s.outgoingUA(), Logger: s.log, Cache: s.opt.ImgCache,
			Transport: func(allowPrivate, insecure bool) http.RoundTripper { return s.opt.Guard(allowPrivate, insecure, false) },
		})
	}
	return s.imgH, true
}

// outgoingUA is Kipple's own User-Agent, the one string shared with the fetcher.
func (s *Server) outgoingUA() string { return s.opt.UserAgent }

// image is GET /img/{sig}/{flags}/{u}; authed supplies the session check.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	h, ok := s.imageHandler(r.Context())
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	h.ServeHTTP(w, r)
}

// imageRewriters returns, per feed id, the serve-time image URL rewriter
// (design §7.4), or nil when the proxy cannot sign (no account secret yet).
func (s *Server) imageRewriters(ctx context.Context, feedIDs []int64, thumb bool) func(feedID int64) func(string) string {
	secret, ok := s.imageSecret(ctx)
	if !ok {
		return nil
	}
	flags, err := s.db.FeedImageFlags(ctx, feedIDs)
	if err != nil {
		s.log.Error("api: image flags", "err", err)
		return nil
	}
	all := s.db.StringSetting(ctx, "imgproxy.mode", store.DefaultImgMode) == "all"
	// Thumbnails are made from the cached original, so they need the cache.
	thumb = thumb && s.opt.ImgCache.Enabled()
	return func(feedID int64) func(string) string {
		return imgproxy.Rewriter{Secret: secret, Flags: flags[feedID], All: all, Thumb: thumb}.Rewrite
	}
}

// stripLinks reports whether serve-time link tracking removal is on
// (links.strip_tracking, default on).
func (s *Server) stripLinks(ctx context.Context) bool {
	return s.db.BoolSetting(ctx, "links.strip_tracking", true)
}

// proxyCards rewrites each card's lead image through the proxy (its thumbnail
// variant; article bodies and the open article's lead image keep the original) and strips
// tracking parameters from its link. Stored URLs are never touched.
func (s *Server) proxyCards(ctx context.Context, cards []store.Card) {
	if len(cards) == 0 {
		return
	}
	if s.stripLinks(ctx) {
		for i := range cards {
			cards[i].URL = sanitize.StripTracking(cards[i].URL)
		}
	}
	ids := make([]int64, 0, len(cards))
	seen := map[int64]bool{}
	for _, c := range cards {
		if c.Image != nil && !seen[c.FeedID] {
			seen[c.FeedID] = true
			ids = append(ids, c.FeedID)
		}
	}
	rw := s.imageRewriters(ctx, ids, true) // list cards use the 800 px thumbnail
	if rw == nil {
		return
	}
	for i := range cards {
		if cards[i].Image != nil {
			img := rw(cards[i].FeedID)(*cards[i].Image)
			cards[i].Image = &img
		}
	}
}

// serveOptions builds the sanitize.ServeHTML options for one feed's article.
func (s *Server) serveOptions(ctx context.Context, feedID int64) sanitize.ServeOptions {
	opt := sanitize.ServeOptions{StripTracking: s.stripLinks(ctx)}
	if secret, ok := s.imageSecret(ctx); ok {
		if flags, err := s.db.FeedImageFlags(ctx, []int64{feedID}); err != nil {
			s.log.Error("api: image flags", "err", err)
		} else {
			all := s.db.StringSetting(ctx, "imgproxy.mode", store.DefaultImgMode) == "all"
			opt.Image = imgproxy.Rewriter{Secret: secret, Flags: flags[feedID], All: all}.Rewrite
			// Embed thumbnails always go through the proxy, whatever the mode:
			// the point of click-to-load is that nothing contacts YouTube first.
			opt.Thumb = imgproxy.Rewriter{Secret: secret, Flags: flags[feedID], All: true}.Rewrite
		}
	}
	return opt
}

// proxyDetail applies the serve-time transform (design 7.8) to an item's lead
// image, link and content HTML.
func (s *Server) proxyDetail(ctx context.Context, det *store.ItemDetail) {
	applyServeOptions(det, s.serveOptions(ctx, det.FeedID))
}

// applyServeOptions is proxyDetail with the options already looked up.
func applyServeOptions(det *store.ItemDetail, opt sanitize.ServeOptions) {
	if det.Image != nil && opt.Image != nil {
		img := opt.Image(*det.Image)
		det.Image = &img
	}
	if opt.StripTracking {
		det.URL = sanitize.StripTracking(det.URL)
	}
	det.ContentHTML = sanitize.ServeHTML(det.ContentHTML, opt)
}

// ImgMode is the current imgproxy.mode ("all" or "http_only"), served from an
// atomic cache so the CSP middleware can read it on every HTML response. The
// cache fills on first use and is refreshed whenever the setting is patched.
func (s *Server) ImgMode() string {
	if p := s.imgMode.Load(); p != nil {
		return *p
	}
	return s.refreshImgMode(context.Background())
}

// refreshImgMode reads the setting and caches it. The read and the store happen
// under one lock, so the last store always comes from the last read: a
// first-use fill that read the old value cannot land after a PATCH's refresh
// (which runs after its write committed) and pin a stale img-src.
func (s *Server) refreshImgMode(ctx context.Context) string {
	s.imgModeMu.Lock()
	defer s.imgModeMu.Unlock()
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	m, err := s.db.StringSettingErr(lctx, "imgproxy.mode", store.DefaultImgMode)
	if err != nil {
		// Not cached: a failed read must not pin the default until the next PATCH.
		s.log.Error("api: imgproxy mode", "err", err)
		return m
	}
	if s.imgModeRead != nil {
		s.imgModeRead(m)
	}
	s.imgMode.Store(&m)
	return m
}
