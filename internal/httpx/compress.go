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
	// maxCompressors bounds the responses being compressed at once. A gzip writer holds most of a megabyte
	// while in use and stays in use for as long as its client takes to read the response, so a crowd
	// of slow readers (or anyone fetching the static assets without signing in) could otherwise pin
	// memory without limit. Past the bound a response goes out uncompressed.
	maxCompressors = 16
)

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(nil, compressLevel) // a valid constant level never errors
	return w
}}

// compressSlots holds one token per response being compressed (maxCompressors; tests replace it).
var compressSlots = make(chan struct{}, maxCompressors)

// Compress wraps h so a response is sent gzip-compressed (compress/gzip from the standard library)
// when the client accepts gzip and the response is worth it, decided once here for every route:
//
//   - only a 200 whose Content-Type is text (not an event stream), JSON, JavaScript or XML: never an
//     image, an archive, a font or anything already compressed, and never a response that set its
//     own Content-Encoding;
//   - only from compressMin bytes on (the decision waits for that much, or for a declared
//     Content-Length);
//   - only while fewer than maxCompressors responses are being compressed; past that the response
//     is sent uncompressed rather than waiting;
//   - never for HEAD or a Range request, so byte ranges and Content-Length keep meaning the
//     identity bytes. A HEAD therefore describes the uncompressed response (its Content-Length,
//     no Content-Encoding), which is what a GET without Accept-Encoding gets: RFC 9110 lets a HEAD
//     omit what it cannot know cheaply, and computing the compressed length would mean compressing;
//   - every response whose 200 would be compressible carries Vary: Accept-Encoding, compressed or
//     not, a HEAD, a 206 and an error included, and so does every 304 (it carries no Content-Type
//     to judge by, and must repeat the Vary of the 200 it revalidates);
//   - a compressed response drops Content-Length and Accept-Ranges and has its ETag made weak (the
//     bytes differ; a conditional request with the weak tag still matches, as If-None-Match
//     compares weakly). A 304 cannot tell whether its 200 would have been compressed (that also
//     depends on the body's size and on a free compressor), so its ETag takes the form the client
//     holds: weak when the request's If-None-Match names the weak form of it and the client accepts
//     gzip, so a cache matches the 304 to the stored response it revalidates (RFC 9111 4.3.4).
//
// Server-Sent Events pass straight through (text/event-stream), and Flush and Unwrap reach the real
// writer. No response that reflects request input carries a secret (sign-in tokens, the backup
// download token), so compression does not open a BREACH-style length oracle.
func Compress(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := r.Method == http.MethodHead || r.Header.Get("Range") != ""
		cw := &compressWriter{ResponseWriter: w, accept: !identity && acceptsGzip(r.Header.Get("Accept-Encoding")),
			ifNoneMatch: r.Header.Get("If-None-Match")}
		defer cw.finish()
		h.ServeHTTP(cw, r)
	})
}

type compressWriter struct {
	http.ResponseWriter
	accept      bool
	ifNoneMatch string // the request's, for the ETag of a 304
	code        int
	started     bool // the handler sent its status
	buffering   bool // eligible; waiting for compressMin bytes before deciding
	buf         []byte
	gz          *gzip.Writer
	slot        bool // holds a compressSlots token
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
	eligible := h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type"))
	if eligible || code == http.StatusNotModified {
		addVary(h, "Accept-Encoding")
	}
	if code == http.StatusNotModified && c.accept {
		if et := h.Get("ETag"); strings.HasPrefix(et, `"`) && listsTag(c.ifNoneMatch, "W/"+et) {
			h.Set("ETag", "W/"+et)
		}
	}
	if code != http.StatusOK || !eligible || !c.accept {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	if cl := h.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			if n < compressMin || !c.startGzip() {
				c.ResponseWriter.WriteHeader(code)
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
	case len(c.buf)+len(p) < compressMin:
		c.buf = append(c.buf, p...)
		return len(p), nil
	}
	// Enough to decide: what was buffered, then p itself, with no copy of p.
	if !c.startGzip() {
		c.sendBuffered()
		return c.ResponseWriter.Write(p)
	}
	if len(c.buf) > 0 {
		if _, err := c.gz.Write(c.buf); err != nil {
			return 0, err
		}
		c.buf = nil
	}
	return c.gz.Write(p)
}

// listsTag reports whether an If-None-Match value names tag exactly.
func listsTag(inm, tag string) bool {
	for _, t := range strings.Split(inm, ",") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

// startGzip commits to a compressed response, or reports false (nothing written) when
// maxCompressors responses are already being compressed.
func (c *compressWriter) startGzip() bool {
	select {
	case compressSlots <- struct{}{}:
		c.slot = true
	default:
		return false
	}
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
	return true
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
	if c.slot {
		<-compressSlots
		c.slot = false
	}
}

// addVary adds token to Vary unless it is already listed.
func addVary(h http.Header, token string) {
	for _, v := range h.Values("Vary") {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t == "*" || strings.EqualFold(t, token) {
				return
			}
		}
	}
	h.Add("Vary", token)
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
