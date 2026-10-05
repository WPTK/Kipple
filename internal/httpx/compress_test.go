package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var bigJSON = `{"items":[` + strings.Repeat(`{"title":"a title","content":"<p>some article text</p>"},`, 200) + `{}]}`

func serveTyped(ct, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", ct)
		_, _ = io.WriteString(w, body)
	})
}

func doCompress(t *testing.T, h http.Handler, method, ae string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/x", nil)
	if ae != "" {
		r.Header.Set("Accept-Encoding", ae)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	Compress(h).ServeHTTP(rec, r)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(out)
}

func TestCompressJSON(t *testing.T) {
	rec := doCompress(t, serveTyped("application/json; charset=utf-8", bigJSON), http.MethodGet, "gzip, deflate, br")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
	require.Empty(t, rec.Header().Get("Content-Length"))
	require.Less(t, rec.Body.Len(), len(bigJSON)/4)
	require.Equal(t, bigJSON, gunzip(t, rec.Body.Bytes()))
}

func TestCompressSkips(t *testing.T) {
	small := `{"ok":true}`
	for name, tc := range map[string]struct {
		h      http.Handler
		method string
		ae     string
		hdr    []string
		vary   bool
	}{
		"small body":         {serveTyped("application/json", small), http.MethodGet, "gzip", nil, true},
		"no Accept-Encoding": {serveTyped("application/json", bigJSON), http.MethodGet, "", nil, true},
		"gzip refused (q=0)": {serveTyped("application/json", bigJSON), http.MethodGet, "gzip;q=0, br", nil, true},
		"identity only":      {serveTyped("application/json", bigJSON), http.MethodGet, "identity", nil, true},
		"star refused":       {serveTyped("application/json", bigJSON), http.MethodGet, "*;q=0", nil, true},
		"image":              {serveTyped("image/png", bigJSON), http.MethodGet, "gzip", nil, false},
		"svg":                {serveTyped("image/svg+xml", bigJSON), http.MethodGet, "gzip", nil, false},
		"zip":                {serveTyped("application/zip", bigJSON), http.MethodGet, "gzip", nil, false},
		"font":               {serveTyped("font/woff2", bigJSON), http.MethodGet, "gzip", nil, false},
		"no content type":    {http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, bigJSON) }), http.MethodGet, "gzip", nil, false},
		"event stream":       {serveTyped("text/event-stream", bigJSON), http.MethodGet, "gzip", nil, false},
		"HEAD":               {serveTyped("application/json", bigJSON), http.MethodHead, "gzip", nil, true},
		"Range":              {serveTyped("application/json", bigJSON), http.MethodGet, "gzip", []string{"Range", "bytes=0-9"}, true},
		"already encoded": {http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, bigJSON)
		}), http.MethodGet, "gzip", nil, false},
		"not a 200": {http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = io.WriteString(w, bigJSON)
		}), http.MethodGet, "gzip", nil, true},
		"declared small size": {http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "2")
			_, _ = io.WriteString(w, "OK")
		}), http.MethodGet, "gzip", nil, true},
	} {
		rec := doCompress(t, tc.h, tc.method, tc.ae, tc.hdr...)
		require.NotEqual(t, "gzip", rec.Header().Get("Content-Encoding"), name)
		if tc.method != http.MethodHead {
			got := rec.Body.String()
			require.True(t, got == small || got == bigJSON || got == "OK", name)
		}
		if tc.vary {
			require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"), name)
		} else {
			require.Empty(t, rec.Header().Get("Vary"), name)
		}
	}
	for _, ae := range []string{"GZIP", "x-gzip", "*", "br;q=1.0, gzip;q=0.5", "deflate, *;q=0.1"} {
		require.True(t, acceptsGzip(ae), ae)
	}
	for _, ae := range []string{"", "br", "gzip;q=0", "gzip;q=0.0, *", "identity"} {
		require.False(t, acceptsGzip(ae), ae)
	}
}

// http.ServeContent keeps working: a compressed 200 has no Content-Length and a weak ETag, a
// conditional request with that tag is a 304, and a Range request gets identity bytes.
func TestCompressWithServeContent(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		http.ServeContent(w, r, "app.js", time.Time{}, strings.NewReader(bigJSON))
	})
	rec := doCompress(t, h, http.MethodGet, "gzip")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Equal(t, `W/"abc"`, rec.Header().Get("ETag"))
	require.Empty(t, rec.Header().Get("Content-Length"))
	require.Empty(t, rec.Header().Get("Accept-Ranges"))
	require.Equal(t, bigJSON, gunzip(t, rec.Body.Bytes()))

	rec = doCompress(t, h, http.MethodGet, "gzip", "If-None-Match", `W/"abc"`)
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"), "a 304 repeats the Vary of the 200")

	rec = doCompress(t, h, http.MethodGet, "gzip", "Range", "bytes=0-9")
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, bigJSON[:10], rec.Body.String())
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))

	// A HEAD describes the uncompressed response, consistently: its length, no encoding, and the Vary.
	rec = doCompress(t, h, http.MethodHead, "gzip")
	require.Equal(t, 200, rec.Code)
	require.Equal(t, strconv.Itoa(len(bigJSON)), rec.Header().Get("Content-Length"))
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))

	// A handler's own Vary is kept and not repeated.
	rec = doCompress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Vary", "Origin, accept-encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bigJSON)
	}), http.MethodGet, "gzip")
	require.Equal(t, []string{"Origin, accept-encoding"}, rec.Header().Values("Vary"))
}

// Past maxCompressors responses in progress, a response goes out uncompressed (with its Vary)
// instead of waiting; once a slot frees, compression resumes.
func TestCompressBoundsConcurrentCompressors(t *testing.T) {
	saved := compressSlots
	compressSlots = make(chan struct{}, 1)
	t.Cleanup(func() { compressSlots = saved })

	release := make(chan struct{})
	started := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bigJSON)
		close(started)
		<-release // a slow reader: the compressor stays in use
	})
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- doCompress(t, slow, http.MethodGet, "gzip") }()
	<-started

	rec := doCompress(t, serveTyped("text/css", bigJSON), http.MethodGet, "gzip")
	require.Empty(t, rec.Header().Get("Content-Encoding"), "the bound is reached")
	require.Equal(t, bigJSON, rec.Body.String())
	require.Equal(t, "Accept-Encoding", rec.Header().Get("Vary"))
	rec = doCompress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Content-Length", strconv.Itoa(len(bigJSON)))
		_, _ = io.WriteString(w, bigJSON)
	}), http.MethodGet, "gzip")
	require.Empty(t, rec.Header().Get("Content-Encoding"), "a declared length takes the same way")
	require.Equal(t, strconv.Itoa(len(bigJSON)), rec.Header().Get("Content-Length"))

	close(release)
	first := <-done
	require.Equal(t, "gzip", first.Header().Get("Content-Encoding"))
	require.Equal(t, bigJSON, gunzip(t, first.Body.Bytes()))
	rec = doCompress(t, serveTyped("text/css", bigJSON), http.MethodGet, "gzip")
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), "the slot was given back")
	require.Empty(t, compressSlots, "every slot is released")
}

// A first write larger than the threshold, and one that crosses it after buffered bytes, both
// arrive intact.
func TestCompressWriteSplits(t *testing.T) {
	for name, parts := range map[string][]string{
		"one large write":      {bigJSON},
		"small then large":     {bigJSON[:100], bigJSON[100:]},
		"many small writes":    strings.SplitAfter(bigJSON, "},"),
		"exactly at threshold": {bigJSON[:compressMin], bigJSON[compressMin:]},
	} {
		rec := doCompress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			for _, p := range parts {
				n, err := io.WriteString(w, p)
				require.NoError(t, err)
				require.Equal(t, len(p), n)
			}
		}), http.MethodGet, "gzip")
		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), name)
		require.Equal(t, bigJSON, gunzip(t, rec.Body.Bytes()), name)
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

// Flush reaches the real writer: an event stream streams, and a compressed body flushed midway is
// already decodable up to that point.
func TestCompressFlushAndUnwrap(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		require.Equal(t, "data: 1\n\n", rec.Body.String(), "an event goes out at once")
		require.Same(t, rec, http.ResponseWriter(w.(interface{ Unwrap() http.ResponseWriter }).Unwrap()))
	})).ServeHTTP(rec, r)
	require.Equal(t, 1, rec.flushes)

	rec = &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bigJSON)
		w.(http.Flusher).Flush()
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		require.NoError(t, err)
		part := make([]byte, len(bigJSON))
		n, _ := io.ReadFull(zr, part)
		require.Equal(t, bigJSON, string(part[:n]), "the flushed part decodes")
	})).ServeHTTP(rec, r)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Equal(t, bigJSON, gunzip(t, rec.Body.Bytes()))
}

// A small body flushed before the threshold goes out uncompressed, and the rest follows it uncompressed.
func TestCompressEarlyFlushStaysIdentity(t *testing.T) {
	rec := doCompress(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "[")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, strings.Repeat("1,", 2000)+"1]")
	}), http.MethodGet, "gzip")
	require.Empty(t, rec.Header().Get("Content-Encoding"))
	require.Equal(t, "["+strings.Repeat("1,", 2000)+"1]", rec.Body.String())
}
