// Package web serves the embedded single-page app: hashed files under
// /assets/ with a long-lived immutable cache header, and index.html for
// every other path with an ETag and Cache-Control: no-cache so the browser
// always revalidates it (per docs/research/go-libraries.md §12).
package web

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/WPTK/kipple/web"
)

// statusPage is the one-file status page (login, feed health, refresh, live
// events). It is served in place of the SPA until web/dist has a real build
// (a fresh clone, `go test`, or phase 1), and always at /_status.
//
//go:embed status.html
var statusPage []byte

// statusScript is the status page's script, served at /_status.js so the page
// carries no inline script and the strict CSP (script-src 'self') holds.
//
//go:embed status.js
var statusScript []byte

// placeholderApp is true until the real single-page app exists: "/" then
// redirects to /_status instead of serving web/dist/index.html (which is only
// the Vite scaffold). Flip it to false when phase 2 lands. Every other path,
// including /assets/, is unaffected. Tests may set it before NewHandler.
var placeholderApp = true

// NewHandler returns an http.Handler serving the embedded frontend.
func NewHandler() (http.Handler, error) {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}

	index, modTime := readIndex(dist)
	etag := etagOf(index)

	mux := http.NewServeMux()
	mux.Handle("GET /assets/", immutable(http.FileServerFS(dist)))
	mux.HandleFunc("GET /_status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etagOf(statusPage))
		http.ServeContent(w, r, "status.html", time.Time{}, bytes.NewReader(statusPage))
	})
	mux.HandleFunc("GET /_status.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etagOf(statusScript))
		http.ServeContent(w, r, "status.js", time.Time{}, bytes.NewReader(statusScript))
	})
	if placeholderApp {
		// "/{$}" matches the root only (GET also covers HEAD).
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/_status", http.StatusFound)
		})
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(index))
	})
	return mux, nil
}

// readIndex returns the built index.html, or the placeholder page (with a
// zero mod time, so ETag alone drives revalidation) if dist has no real
// build yet.
func readIndex(dist fs.FS) ([]byte, time.Time) {
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return statusPage, time.Time{}
	}
	return b, time.Now()
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func immutable(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}
