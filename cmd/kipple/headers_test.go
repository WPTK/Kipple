package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/greader"
	"github.com/WPTK/kipple/internal/store"
	kweb "github.com/WPTK/kipple/internal/web"
)

// The handler chain serve installs puts the clickjacking and content policies
// on every kind of response: the SPA, a UI API path, the Reader API and the
// image proxy (which keeps its own CSP but still gets framing protection).
func TestRootHandlerSendsSecurityHeaders(t *testing.T) {
	dir := newData(t, 0)
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	reader := greader.New(greader.Options{DB: db, Logger: quietLog})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		_, _ = w.Write([]byte("png"))
	})
	webHandler, err := kweb.NewHandler()
	require.NoError(t, err)
	mux.Handle("/", webHandler)
	h := rootHandler(reader.Front, mux, func() string { return "all" }, nil, quietLog, nil)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}
	for _, path := range []string{"/", "/api/status", "/api/greader.php/reader/api/0/user-info", "/img/abc"} {
		rec := get(path)
		hd := rec.Header()
		require.Equal(t, "DENY", hd.Get("X-Frame-Options"), path)
		require.Equal(t, "nosniff", hd.Get("X-Content-Type-Options"), path)
		require.Equal(t, "no-referrer", hd.Get("Referrer-Policy"), path)
		require.NotEmpty(t, hd.Get("Content-Security-Policy"), path)
		if path != "/img/abc" {
			require.Contains(t, hd.Get("Content-Security-Policy"), "frame-ancestors 'none'", path)
		}
	}
	page := get("/")
	require.True(t, strings.HasPrefix(page.Header().Get("Content-Type"), "text/html"), "the SPA is a page")
	require.Contains(t, page.Header().Get("Content-Security-Policy"), "script-src 'self'")
	require.Contains(t, page.Header().Get("Permissions-Policy"), "camera=()")
	require.Equal(t, "default-src 'none'; sandbox", get("/img/abc").Header().Get("Content-Security-Policy"), "the image proxy keeps its own policy")
	require.Equal(t, "same-origin", get("/api/status").Header().Get("Cross-Origin-Resource-Policy"))
}
