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
	"regexp"
	"strings"
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

// Option configures NewHandler.
type Option func(*handlerOpts)

type handlerOpts struct{ imgMode func() string }

// WithImgMode supplies the current imgproxy.mode. The page CSP (img-src)
// depends on it and a 304 cannot carry a new policy, so the mode is folded into
// the ETag of every HTML page: a mode change invalidates cached copies.
func WithImgMode(f func() string) Option { return func(o *handlerOpts) { o.imgMode = f } }

// buildMeta finds the build id the Vite build writes into index.html (<meta name="kipple-build" content="...">).
var buildMeta = regexp.MustCompile(`<meta\s+name="kipple-build"\s+content="([A-Za-z0-9._-]{1,64})"`)

// BuildID is the id of the web build embedded in this binary, read once from the embedded index.html: the same
// value the bundle carries as __KIPPLE_BUILD__, so a page can tell whether the server it talks to was rebuilt
// since it loaded. "" when the build carries none (a fresh clone with the placeholder page, or a dev build).
func BuildID() string { return buildIDOf(web.Dist) }

func buildIDOf(fsys fs.FS) string {
	dist, err := fs.Sub(fsys, "dist")
	if err != nil {
		return ""
	}
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return ""
	}
	if m := buildMeta.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// NewHandler returns an http.Handler serving the embedded frontend.
func NewHandler(opts ...Option) (http.Handler, error) {
	var o handlerOpts
	for _, f := range opts {
		f(&o)
	}
	modeTag := func() string {
		if o.imgMode == nil {
			return "all"
		}
		return o.imgMode()
	}
	// pageETag is the content hash plus the policy input, so it changes when
	// either does.
	pageETag := func(base string) string { return base[:len(base)-1] + "." + modeTag() + `"` }

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}
	return newHandler(dist, pageETag), nil
}

// newHandler serves dist: /assets/*, the root files of the build (manifest, service worker, icons), and
// index.html for every other path.
func newHandler(dist fs.FS, pageETag func(string) string) http.Handler {
	index, modTime := readIndex(dist)
	etag := etagOf(index)

	mux := http.NewServeMux()
	mux.Handle("GET /assets/", immutable(http.FileServerFS(dist)))
	mux.HandleFunc("GET /_status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", pageETag(etagOf(statusPage)))
		http.ServeContent(w, r, "status.html", time.Time{}, bytes.NewReader(statusPage))
	})
	mux.HandleFunc("GET /_status.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etagOf(statusScript))
		http.ServeContent(w, r, "status.js", time.Time{}, bytes.NewReader(statusScript))
	})
	for _, name := range rootFiles(dist) {
		body, err := fs.ReadFile(dist, name)
		if err != nil {
			continue
		}
		mux.Handle("GET /"+name, rootFile(name, body))
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", pageETag(etag))
		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(index))
	})
	return mux
}

// rootFiles lists the regular files at the top of dist other than index.html and dotfiles: what the
// build copies from web/public (manifest, service worker, icons, robots.txt). Each is served at "/<name>",
// so a name can never reach outside dist and needs no path cleaning.
func rootFiles(dist fs.FS) []string {
	ents, err := fs.ReadDir(dist, ".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && n != "index.html" && validRootName(n) {
			out = append(out, n)
		}
	}
	return out
}

// validRootName keeps a name that is safe as a mux pattern and cannot shadow a route of the server: plain
// characters only, no dotfiles, nothing starting with an underscore (the /_status pages).
func validRootName(n string) bool {
	if n == "" || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// rootFile serves one root file. All of them revalidate on every use (no-cache plus an ETag of the
// content), which is what a service worker script needs so an update is found within a day at most, and
// is cheap for the small rest. sw.js may control the whole origin; its type is fixed so a browser never
// refuses it for a wrong MIME.
func rootFile(name string, body []byte) http.Handler {
	etag := etagOf(body)
	ctype := ""
	switch {
	case name == "sw.js":
		ctype = "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".webmanifest"):
		ctype = "application/manifest+json"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", etag)
		if ctype != "" {
			h.Set("Content-Type", ctype)
		}
		if name == "sw.js" {
			h.Set("Service-Worker-Allowed", "/")
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
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
