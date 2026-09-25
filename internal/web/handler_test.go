package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestUIResponsesForbidFraming(t *testing.T) {
	h, err := NewHandler()
	require.NoError(t, err)
	for _, path := range []string{"/", "/_status", "/some/client/route", "/assets/missing.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, "frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"), path)
		require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), path)
	}
}
