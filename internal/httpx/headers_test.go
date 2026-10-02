package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, opt Options, h http.HandlerFunc, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = "10.20.30.10:1234"
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	Secure(h, opt).ServeHTTP(rec, r)
	return rec
}

func typed(ct, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write([]byte(body))
	}
}

func TestBaseHeadersOnEveryClass(t *testing.T) {
	for _, ct := range []string{"text/html; charset=utf-8", "application/json", "text/plain", "text/javascript", "image/png", ""} {
		rec := serve(t, Options{}, typed(ct, "x"))
		require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"), ct)
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), ct)
		require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), ct)
		require.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'", ct)
		require.Empty(t, rec.Header().Get("Strict-Transport-Security"), "plain http: "+ct)
	}
}

func TestHTMLGetsThePageCSPModeDependent(t *testing.T) {
	mode := "http_only"
	opt := Options{ImgMode: func() string { return mode }}
	rec := serve(t, opt, typed("text/html; charset=utf-8", "<p>"))
	csp := rec.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "default-src 'none'")
	require.Contains(t, csp, "script-src 'self';", "no inline script, no eval")
	require.NotContains(t, csp, "script-src 'self' 'unsafe")
	require.Contains(t, csp, "img-src 'self' data: blob: https:;")
	require.Contains(t, csp, "frame-src https://www.youtube-nocookie.com https://player.vimeo.com")
	require.Contains(t, csp, "base-uri 'none'")
	require.Contains(t, csp, "object-src 'none'")
	require.Contains(t, csp, "frame-ancestors 'none'")
	require.NotContains(t, csp, "upgrade-insecure-requests", "plain-http LAN visits must keep working")
	require.Contains(t, rec.Header().Get("Permissions-Policy"), "camera=()")
	require.Contains(t, rec.Header().Get("Permissions-Policy"), `fullscreen=(self "https://www.youtube-nocookie.com"`)
	pp := rec.Header().Get("Permissions-Policy")
	embeds := `self "https://www.youtube-nocookie.com" "https://player.vimeo.com"`
	for _, f := range []string{"autoplay", "fullscreen", "picture-in-picture"} {
		require.Contains(t, pp, f+"=("+embeds+")", f+" must reach the tap-to-load embeds")
	}
	require.NotContains(t, pp, "autoplay=()")
	require.Equal(t, "same-origin", rec.Header().Get("Cross-Origin-Opener-Policy"))

	mode = "all" // read per response: a settings change applies at once
	csp = serve(t, opt, typed("text/html", "<p>")).Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "img-src 'self' data: blob:;")
	require.NotContains(t, csp, "blob: https:")

	require.Contains(t, serve(t, Options{}, typed("text/html", "")).Header().Get("Content-Security-Policy"), "img-src 'self' data: blob:;", "nil ImgMode is the strict policy")
}

func TestDataAndAssetPolicies(t *testing.T) {
	for _, ct := range []string{"application/json", "text/plain; charset=utf-8", "application/xml", "text/xml", "image/svg+xml", ""} {
		rec := serve(t, Options{}, typed(ct, "x"))
		require.Equal(t, "default-src 'none'; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"), ct)
		require.Empty(t, rec.Header().Get("Permissions-Policy"), ct)
	}
	for _, ct := range []string{"text/javascript; charset=utf-8", "text/css", "image/png", "font/woff2", "application/javascript"} {
		rec := serve(t, Options{}, typed(ct, "x"))
		require.Equal(t, "frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"), ct)
	}
}

func TestHandlerCSPIsKept(t *testing.T) {
	rec := serve(t, Options{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'")
		_, _ = w.Write([]byte("x"))
	})
	require.Equal(t, "default-src 'none'; sandbox; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"))
}

func TestNotModifiedGetsNoContentPolicy(t *testing.T) {
	rec := serve(t, Options{}, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotModified) })
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

func TestHSTSAndUpgradeOnlyWhenEffectivelyHTTPS(t *testing.T) {
	proxy := netip.MustParsePrefix("192.0.2.5/32")
	opt := Options{TrustedProxies: []netip.Prefix{proxy}}
	fromProxy := func(proto string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "192.0.2.5:4444"
			r.Header.Set("X-Forwarded-Proto", proto)
		}
	}
	rec := serve(t, opt, typed("text/html", "x"), fromProxy("https"))
	require.Equal(t, "max-age=31536000", rec.Header().Get("Strict-Transport-Security"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "upgrade-insecure-requests")

	rec = serve(t, opt, typed("text/html", "x"), fromProxy("http"))
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))

	// an untrusted peer cannot claim https
	rec = serve(t, opt, typed("text/html", "x"), func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") })
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	require.NotContains(t, rec.Header().Get("Content-Security-Policy"), "upgrade-insecure-requests")
}

func TestCORPOnAPIAndImages(t *testing.T) {
	for path, want := range map[string]string{"/api/items": "same-origin", "/img/a/0/b": "same-origin", "/": "", "/assets/a.js": "", "/healthz": ""} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Secure(typed("application/json", "{}"), Options{}).ServeHTTP(rec, r)
		require.Equal(t, want, rec.Header().Get("Cross-Origin-Resource-Policy"), path)
	}
}

type flushRec struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRec) Flush() { f.flushed = true }

func TestStreamingStillFlushesAndControllerUnwraps(t *testing.T) {
	rec := &flushRec{ResponseRecorder: httptest.NewRecorder()}
	h := Secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		require.NoError(t, http.NewResponseController(w).Flush())
	}), Options{})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	require.True(t, rec.flushed)
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"))
}

func TestPageCSPHasNoUnsafeScript(t *testing.T) {
	for _, m := range []string{"all", "http_only"} {
		for _, sec := range []bool{true, false} {
			csp := PageCSP(m, sec)
			require.False(t, strings.Contains(csp, "unsafe-eval"))
			require.Equal(t, 1, strings.Count(csp, "'unsafe-inline'"), "only style-src")
		}
	}
}

func TestAPIHandshakeHeaderOnAPIPathsOnly(t *testing.T) {
	get := func(path string) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Secure(typed("application/json", "{}"), Options{}).ServeHTTP(rec, r)
		return rec.Header().Get("X-Kipple-API")
	}
	require.Equal(t, APIVersion, get("/api/bootstrap"))
	require.Empty(t, get("/"))
	require.Empty(t, get("/api/greader.php/reader/api/0/token"), "the Reader API is a different contract")
}

// The Go and TypeScript halves of the handshake must agree: web/src/lib/offlineState.ts says which contract
// the app was built for.
func TestAPIVersionMatchesTheWebApp(t *testing.T) {
	src, err := os.ReadFile("../../web/src/lib/offlineState.ts")
	require.NoError(t, err)
	m := regexp.MustCompile(`export const API_VERSION = (\d+);`).FindSubmatch(src)
	require.NotNil(t, m, "API_VERSION not found in offlineState.ts")
	require.Equal(t, APIVersion, string(m[1]))
}
