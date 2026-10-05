package httpx

import (
	"compress/gzip"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const (
	// compressMin is the smallest body worth compressing: below about one TCP segment gzip saves
	// nothing a client would notice and its header costs bytes.
	compressMin = 1400
	// compressLevel is gzip.BestSpeed: on the Reader API and web app responses it keeps most of the
	// saving of the default level at a fraction of the CPU (docs/performance.md, "Response compression").
	compressLevel = gzip.BestSpeed
)

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, compressLevel) // a valid constant level never errors
	return w
}}

// Compress wraps h so a response is sent gzip-compressed (compress/gzip from the standard library)
// when the client accepts gzip and the response is worth it, decided once here for every route:
//
//   - only a 200 whose Content-Type is text (not an event stream), JSON, JavaScript or XML: never an
//     image, an archive, a font or anything already compressed, and never a response that set its
//     own Content-Encoding;
//   - only from compressMin bytes on (the decision waits for that much, or for a declared
//     Content-Length);
//   - never for HEAD or a Range request, so byte ranges and Content-Length keep meaning the
//     identity bytes;
//   - every compressible response carries Vary: Accept-Encoding, compressed or not, and a
//     compressed one drops Content-Length and Accept-Ranges and has its ETag made weak (the bytes
//     differ; a conditional request with the weak tag still matches, as If-None-Match compares
//     weakly).
//
// Server-Sent Events pass straight through (text/event-stream), and Flush and Unwrap reach the real
// writer. No response that reflects request input carries a secret (sign-in tokens, the backup
// download token), so compression does not open a BREACH-style length oracle.
func Compress(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" {
			h.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w, accept: acceptsGzip(r.Header.Get("Accept-Encoding"))}
		defer cw.finish()
		h.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	accept    bool
	code      int
	started   bool // the handler sent its status
	buffering bool // eligible; waiting for compressMin bytes before deciding
	buf       []byte
	gz        *gzip.Writer
}

func (c *compressWriter) WriteHeader(code int) {
	if c.started {
		return // a second WriteHeader is a no-op, as on the real writer (which logs it)
	}
	if code < 200 && code != http.StatusSwitchingProtocols {
		c.ResponseWriter.WriteHeader(code) // informational: not the response's status
		return
	}
	c.started, c.code = true, code
	h := c.Header()
	if code != http.StatusOK || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	h.Add("Vary", "Accept-Encoding")
	if !c.accept {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			if n < compressMin {
				c.ResponseWriter.WriteHeader(code)
			} else {
				c.startGzip()
			}
			return
		}
	}
	c.buffering = true
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	switch {
	case c.gz != nil:
		return c.gz.Write(p)
	case !c.buffering:
		return c.ResponseWriter.Write(p)
	}
	c.buf = append(c.buf, p...)
	if len(c.buf) >= compressMin {
		c.startGzip()
		if _, err := c.gz.Write(c.buf); err != nil {
			return 0, err
		}
		c.buf = nil
	}
	return len(p), nil
}

// startGzip commits to a compressed response.
func (c *compressWriter) startGzip() {
	c.buffering = false
	h := c.Header()
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	h.Set("Content-Encoding", "gzip")
	if et := h.Get("ETag"); strings.HasPrefix(et, `"`) {
		h.Set("ETag", "W/"+et)
	}
	c.ResponseWriter.WriteHeader(c.code)
	c.gz = gzipPool.Get().(*gzip.Writer)
	c.gz.Reset(c.ResponseWriter)
}

// sendBuffered gives up on compression: the status and what was buffered go out as they are.
func (c *compressWriter) sendBuffered() {
	c.buffering = false
	c.ResponseWriter.WriteHeader(c.code)
	if len(c.buf) > 0 {
		_, _ = c.ResponseWriter.Write(c.buf)
		c.buf = nil
	}
}

// Flush sends what the handler wrote so far. A response still below compressMin goes out
// uncompressed: the handler wants these bytes now.
func (c *compressWriter) Flush() {
	if !c.started {
		c.WriteHeader(http.StatusOK)
	}
	if c.buffering {
		c.sendBuffered()
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer (SSE deadlines).
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// finish ends the response after the handler returned.
func (c *compressWriter) finish() {
	if c.buffering {
		c.sendBuffered()
	}
	if c.gz != nil {
		_ = c.gz.Close()
		c.gz.Reset(nil)
		gzipPool.Put(c.gz)
		c.gz = nil
	}
}

// compressible reports whether a Content-Type is worth compressing: text other than an event stream,
// JSON, JavaScript and XML (not SVG or any other image).
func compressible(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"):
		return true
	case strings.HasPrefix(mt, "image/"):
		return false
	case mt == "application/json", mt == "application/javascript", mt == "application/xml",
		strings.HasSuffix(mt, "+json"), strings.HasSuffix(mt, "+xml"):
		return true
	}
	return false
}

// acceptsGzip reads Accept-Encoding (RFC 9110 12.5.3): gzip (or x-gzip) with a non-zero weight, or,
// when gzip is not named, "*" with a non-zero weight.
func acceptsGzip(header string) bool {
	gz, star := -1.0, -1.0
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "gzip", "x-gzip":
			gz = max(gz, q)
		case "*":
			star = q
		}
	}
	if gz >= 0 {
		return gz > 0
	}
	return star > 0
}
