// Package web serves the embedded single-page app: hashed files under
// /assets/ with a long-lived immutable cache header, and index.html for
// every other path with an ETag and Cache-Control: no-cache so the browser
// always revalidates it (per docs/research/go-libraries.md §12).
package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/WPTK/kipple/web"
)

// placeholderIndex is served in place of the real SPA when web/dist has not
// been built (a fresh clone with no `npm run build` yet, e.g. `go test`).
const placeholderIndex = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Kipple</title></head>
<body>
<p>Kipple's frontend has not been built. Run "npm run build" in web/, or
use the Docker image, which builds it as part of the image.</p>
</body>
</html>
`

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
		return []byte(placeholderIndex), time.Time{}
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
