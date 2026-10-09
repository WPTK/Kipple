// Package httpx holds HTTP middleware shared by every route of the server.
package httpx

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/WPTK/kipple/internal/auth"
)

// Options configures Secure.
type Options struct {
	// ImgMode returns the current imgproxy.mode ("all" or "http_only"). It must be
	// cheap (an atomic read): it runs on every HTML response. Nil means "all", the
	// strictest policy.
	ImgMode func() string
	// TrustedProxies returns the peers whose X-Forwarded-Proto is believed when
	// deciding whether the effective scheme is https (HSTS, upgrade-insecure-requests).
	// It must be cheap (an atomic read). Nil trusts none.
	TrustedProxies func() []netip.Prefix
}

const (
	youtubeHost = "https://www.youtube-nocookie.com"
	vimeoHost   = "https://player.vimeo.com"

	// cspData covers everything that is not a page: JSON, plain text, XML.
	cspData = "default-src 'none'; frame-ancestors 'none'"
	// cspFrameOnly covers static assets (scripts, styles, fonts, images, manifest):
	// a restrictive default-src there would only constrain a worker that inherits it.
	cspFrameOnly = "frame-ancestors 'none'"

	permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), bluetooth=(), serial=(), hid=(), " +
		"browsing-topics=(), interest-cohort=(), " +
		`autoplay=(self "` + youtubeHost + `" "` + vimeoHost + `"), ` +
		`fullscreen=(self "` + youtubeHost + `" "` + vimeoHost + `"), ` +
		`picture-in-picture=(self "` + youtubeHost + `" "` + vimeoHost + `")`
)

// PageCSP is the policy for HTML (the SPA and /_status). imgMode is the
// imgproxy.mode: with "http_only", https images load straight from their host,
// so img-src has to allow them; with "all" every image is same-origin. secure
// adds upgrade-insecure-requests, which must not be sent over plain http (a LAN
// visit would have its own subresources upgraded to https and fail).
func PageCSP(imgMode string, secure bool) string {
	img := "img-src 'self' data: blob:"
	if imgMode == "http_only" {
		img += " https:"
	}
	parts := []string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		img,
		"font-src 'self'",
		"connect-src 'self'",
		"media-src 'self' https:",
		"frame-src " + youtubeHost + " " + vimeoHost,
		"manifest-src 'self'",
		"worker-src 'self'",
		"form-action 'self'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"object-src 'none'",
	}
	if secure {
		parts = append(parts, "upgrade-insecure-requests")
	}
	return strings.Join(parts, "; ")
}

// APIVersion is the version of the web API contract (/api/*, not the Reader API). It is bumped when a
// change would break a web app built for the previous one; the app reloads itself when the server's is newer.
const APIVersion = "1"

// Secure wraps h so every response carries the security headers of
// docs/research/backend-additions-round2.md section 5.1. The policy that depends
// on the content is chosen when the response starts, from its Content-Type, so
// it needs no list of routes: HTML gets the full page policy, other text and
// JSON get default-src 'none', and assets get frame-ancestors only. A handler
// that sets its own Content-Security-Policy (the image proxy) keeps it.
func Secure(h http.Handler, opt Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		secure := auth.EffectiveScheme(r, trusted(opt)) == "https"
		if secure {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		// The Reader API is meant for other origins (it answers CORS, and a client shows its iconUrl
		// images), so it gets no same-origin resource policy; the web app's /api and /img do.
		webAPI := strings.HasPrefix(r.URL.Path, "/api/") && !readerPath(r.URL.Path)
		if webAPI || strings.HasPrefix(r.URL.Path, "/img/") {
			hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		}
		if webAPI {
			hd.Set("X-Kipple-API", APIVersion) // the handshake: the web app compares it with the one it was built for
		}
		h.ServeHTTP(&secureWriter{ResponseWriter: w, opt: opt, secure: secure}, r)
	})
}

// readerMount is the Reader API's mount point (greader.apiPrefix).
const readerMount = "/api/greader.php"

// readerPath reports whether p is under the Reader API mount: the mount itself or a path below it,
// after collapsing runs of '/' as the Reader front handler does (so /api//greader.php/x is the
// Reader API too), and never a sibling such as /api/greader.phpx.
func readerPath(p string) bool {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p == readerMount || strings.HasPrefix(p, readerMount+"/")
}

// secureWriter adds the content-dependent headers on the first WriteHeader.
type secureWriter struct {
	http.ResponseWriter
	opt         Options
	secure      bool
	wroteHeader bool
}

func (s *secureWriter) WriteHeader(code int) {
	if !s.wroteHeader && (code < 100 || code > 199 || code == http.StatusSwitchingProtocols) {
		s.wroteHeader = true
		s.decorate(code)
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *secureWriter) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps Server-Sent Events streaming through the wrapper.
func (s *secureWriter) Flush() {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer (SSE deadlines).
func (s *secureWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *secureWriter) decorate(code int) {
	h := s.Header()
	if code == http.StatusNotModified {
		// A 304 refreshes the stored headers of the cached response. Sending a
		// weaker policy here would replace the page policy in the browser cache.
		return
	}
	// A response that names no cache policy is not for a shared cache or a stored copy: every route
	// that wants caching (assets, icons, images) says so itself.
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "private, no-store")
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/html") {
		mode := "all"
		if s.opt.ImgMode != nil {
			mode = s.opt.ImgMode()
		}
		if h.Get("Content-Security-Policy") == "" {
			h.Set("Content-Security-Policy", PageCSP(mode, s.secure))
		}
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		return
	}
	if h.Get("Content-Security-Policy") != "" {
		return
	}
	script := strings.HasPrefix(ct, "text/css") || strings.HasPrefix(ct, "text/javascript")
	switch {
	case script:
		h.Set("Content-Security-Policy", cspFrameOnly)
	case ct == "", strings.HasPrefix(ct, "text/"), strings.Contains(ct, "json"), strings.Contains(ct, "xml"):
		// An unknown type fails closed: the body may be sniffed as anything else.
		h.Set("Content-Security-Policy", cspData)
	default:
		h.Set("Content-Security-Policy", cspFrameOnly)
	}
}

// trusted is opt.TrustedProxies(), or none.
func trusted(opt Options) []netip.Prefix {
	if opt.TrustedProxies == nil {
		return nil
	}
	return opt.TrustedProxies()
}
