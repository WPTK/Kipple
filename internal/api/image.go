package api

import (
	"context"
	"net/http"

	"github.com/WPTK/kipple/internal/imgproxy"
	"github.com/WPTK/kipple/internal/sanitize"
	"github.com/WPTK/kipple/internal/store"
)

// imageSecret returns the account secret that keys image proxy signatures. It
// is cached once read; a failure is not cached.
func (s *Server) imageSecret(ctx context.Context) ([]byte, bool) {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.imgSecret != nil {
		return s.imgSecret, true
	}
	acct, ok, err := s.db.Account(ctx)
	if err != nil || !ok || acct.Secret == "" {
		if err != nil {
			s.log.Error("api: image secret", "err", err)
		}
		return nil, false
	}
	s.imgSecret = []byte(acct.Secret)
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
	if s.imgH == nil {
		s.imgH = imgproxy.New(imgproxy.Options{
			Secret: secret, UserAgent: s.outgoingUA(), Logger: s.log,
			Transport: func(allowPrivate, insecure bool) http.RoundTripper { return s.opt.Guard(allowPrivate, insecure, false) },
		})
	}
	return s.imgH, true
}

func (s *Server) outgoingUA() string {
	ua := "Mozilla/5.0 (compatible; Kipple/" + s.opt.Version
	if s.opt.PublicURL != "" {
		ua += "; +" + s.opt.PublicURL
	}
	return ua + ")"
}

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
func (s *Server) imageRewriters(ctx context.Context, feedIDs []int64) func(feedID int64) func(string) string {
	secret, ok := s.imageSecret(ctx)
	if !ok {
		return nil
	}
	flags, err := s.db.FeedImageFlags(ctx, feedIDs)
	if err != nil {
		s.log.Error("api: image flags", "err", err)
		return nil
	}
	all := s.db.StringSetting(ctx, "imgproxy.mode", "http_only") == "all"
	return func(feedID int64) func(string) string {
		return imgproxy.Rewriter{Secret: secret, Flags: flags[feedID], All: all}.Rewrite
	}
}

// proxyCards rewrites each card's lead image through the proxy.
func (s *Server) proxyCards(ctx context.Context, cards []store.Card) {
	if len(cards) == 0 {
		return
	}
	ids := make([]int64, 0, len(cards))
	for _, c := range cards {
		if c.Image != nil {
			ids = append(ids, c.FeedID)
		}
	}
	rw := s.imageRewriters(ctx, ids)
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

// proxyDetail rewrites an item's lead image and content HTML.
func (s *Server) proxyDetail(ctx context.Context, det *store.ItemDetail) {
	rw := s.imageRewriters(ctx, []int64{det.FeedID})
	if rw == nil {
		return
	}
	fn := rw(det.FeedID)
	if det.Image != nil {
		img := fn(*det.Image)
		det.Image = &img
	}
	det.ContentHTML = sanitize.RewriteImages(det.ContentHTML, fn)
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

func (s *Server) refreshImgMode(ctx context.Context) string {
	m := s.db.StringSetting(ctx, "imgproxy.mode", "http_only")
	s.imgMode.Store(&m)
	return m
}
