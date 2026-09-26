package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WPTK/kipple/internal/httpx"
	"github.com/WPTK/kipple/web"
	"github.com/stretchr/testify/require"
)

// These tests run against whatever is actually embedded in web/dist at
// build time: the committed .gitkeep placeholder on a fresh clone, or a
// real `npm run build` output otherwise. Either way index.html must come
// back with the right cache headers.
func TestIndexServedWithCacheHeaders(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	require.NotEmpty(t, rec.Header().Get("ETag"))
	require.NotEmpty(t, rec.Body.String())
}

func TestSPAFallbackServesIndexForUnknownPath(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/some/client/route", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestETagRevalidation(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	require.Equal(t, http.StatusNotModified, rec2.Code)
}

// A missing asset must NOT get the immutable header: the stdlib file server
// strips Cache-Control/ETag/Last-Modified on error responses precisely so a
// 404 is never cached as if it were the real, hashed file.
func TestMissingAssetIsNotCachedAsImmutable(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Empty(t, rec.Header().Get("Cache-Control"))
}

// An asset that actually exists gets the immutable, long-lived header. This
// only runs once web/dist has a real `npm run build` output; on a fresh
// clone (only the committed .gitkeep placeholder) it skips.
func TestExistingAssetGetsImmutableCacheControl(t *testing.T) {
	dist, err := fs.Sub(web.Dist, "dist")
	require.NoError(t, err)

	var assetPath string
	err = fs.WalkDir(dist, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && assetPath == "" {
			assetPath = p
		}
		return nil
	})
	if err != nil || assetPath == "" {
		t.Skip("web/dist has no built assets yet (run npm run build in web/)")
	}

	h, herr := NewHandler()
	require.NoError(t, herr)

	req := httptest.NewRequest(http.MethodGet, "/"+assetPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
}

// "/" serves the app (index.html, or the status page when dist has no build);
// the status page and assets are unaffected.
func TestRootServesTheApp(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/", nil))
		require.Equal(t, http.StatusOK, rec.Code, method)
		require.Empty(t, rec.Header().Get("Location"), method)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// The page must carry no inline script, or the strict CSP (script-src 'self')
// blanks it. Everything it runs is served by /_status.js.
func TestStatusPageHasNoInlineScript(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	page := rec.Body.String()
	require.Contains(t, page, `<script src="/_status.js"></script>`)
	require.NotRegexp(t, `(?i)<script(s[^>]*)?>s*S`, strings.ReplaceAll(page, `<script src="/_status.js"></script>`, ""))
	require.NotRegexp(t, `(?i)son[a-z]+s*=`, page, "no inline event handlers")

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_status.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript")
	require.Contains(t, rec.Body.String(), "loadFeeds")
	require.NotContains(t, rec.Body.String(), "</script>")
}

// Under httpx.Secure the UI routes get their policy by content: pages the full
// CSP, the script and 404s a policy that still forbids framing.
func TestUIResponsesUnderSecure(t *testing.T) {
	inner, err := NewHandler()
	require.NoError(t, err)
	h := httpx.Secure(inner, httpx.Options{ImgMode: func() string { return "all" }})
	for _, path := range []string{"/", "/_status", "/some/client/route"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		csp := rec.Header().Get("Content-Security-Policy")
		require.Contains(t, csp, "script-src 'self'", path)
		require.Contains(t, csp, "frame-ancestors 'none'", path)
		require.Contains(t, csp, "img-src 'self' data: blob:;", path)
		require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), path)
	}
	for _, path := range []string{"/_status.js", "/assets/missing.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'", path)
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), path)
	}
}

// A revalidation must not replace the cached page policy with a weaker one.
func TestNotModifiedKeepsPolicyOffTheResponse(t *testing.T) {
	inner, err := NewHandler()
	require.NoError(t, err)
	h := httpx.Secure(inner, httpx.Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

// The page CSP depends on imgproxy.mode and a 304 cannot carry a new policy, so
// a mode change must change the ETag of / and /_status.
func TestETagChangesWithImgMode(t *testing.T) {
	mode := "all"
	h, err := NewHandler(WithImgMode(func() string { return mode }))
	require.NoError(t, err)
	for _, path := range []string{"/", "/_status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		before := rec.Header().Get("ETag")
		require.NotEmpty(t, before, path)

		mode = "http_only"
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", before)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, path+": stale ETag must not revalidate")
		require.NotEqual(t, before, rec.Header().Get("ETag"), path)

		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotModified, rec.Code, path)
		mode = "all"
	}
}
