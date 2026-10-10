package imgproxy

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)
	gifBytes  = append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 64)...)
	jpgBytes  = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), bytes.Repeat([]byte{0}, 64)...)
	webpBytes = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0}, 64)...)
)

func avifBytes(major string, compat ...string) []byte {
	box := []byte(major + "\x00\x00\x00\x00")
	for _, c := range compat {
		box = append(box, c...)
	}
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(8+len(box)))
	out := append(append(size, "ftyp"...), box...)
	return append(out, bytes.Repeat([]byte{0}, 64)...)
}

type rig struct {
	t   *testing.T
	srv *httptest.Server // the proxy
}

func newRig(t *testing.T, tune ...func(*Options)) *rig {
	t.Helper()
	fc := fetch.NewClient(fetch.ClientOptions{})
	opt := Options{
		Secret: secret,
		Transport: func(allowPrivate, insecure bool) http.RoundTripper {
			return fc.Transport(allowPrivate, insecure, false)
		},
	}
	for _, f := range tune {
		f(&opt)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", New(opt))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &rig{t: t, srv: srv}
}

func (r *rig) get(path string, hdr ...string) *http.Response {
	r.t.Helper()
	req, err := http.NewRequest("GET", r.srv.URL+path, nil)
	require.NoError(r.t, err)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (r *rig) fetchOrig(orig string, flags int, hdr ...string) *http.Response {
	return r.get(Path(secret, flags, orig), hdr...)
}

func upstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func serve(ct string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(body)
	}
}

func TestServesAllowedTypesWithHeaders(t *testing.T) {
	rg := newRig(t)
	cases := []struct {
		name, upstreamCT, want string
		body                   []byte
	}{
		{"png", "image/png", "image/png", pngBytes},
		{"jpeg", "application/octet-stream", "image/jpeg", jpgBytes},
		{"gif", "", "image/gif", gifBytes},
		{"webp", "image/webp", "image/webp", webpBytes},
		{"avif major", "application/octet-stream", "image/avif", avifBytes("avif")},
		{"avis major", "", "image/avif", avifBytes("avis")},
		{"avif compatible", "text/html", "image/avif", avifBytes("mif1", "miaf", "avif")},
		{"sniff wins over a lying upstream", "image/svg+xml", "image/png", pngBytes},
		{"inconclusive sniff, upstream on the list", "image/png", "image/png", []byte("not really an image but trusted")},
	}
	for _, c := range cases {
		up := upstream(t, serve(c.upstreamCT, c.body))
		resp := rg.fetchOrig(up.URL+"/a.img", FlagPrivateNet)
		b, _ := io.ReadAll(resp.Body)
		require.Equal(t, 200, resp.StatusCode, c.name)
		require.Equal(t, c.want, resp.Header.Get("Content-Type"), c.name)
		require.Equal(t, c.body, b, c.name)
		require.Equal(t, "private, max-age=2592000, immutable", resp.Header.Get("Cache-Control"))
		require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		require.Equal(t, "default-src 'none'", resp.Header.Get("Content-Security-Policy"))
	}
}

func TestRefusesSVGAndOtherTypes(t *testing.T) {
	rg := newRig(t)
	for name, c := range map[string]struct {
		ct   string
		body string
	}{
		"svg declared":       {"image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		"svg as png":         {"image/png", `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`},
		"xml prolog svg":     {"image/png", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`},
		"html":               {"image/png", `<!DOCTYPE html><html><script>alert(1)</script></html>`},
		"pdf":                {"image/jpeg", "%PDF-1.4 ..."},
		"plain not on list":  {"text/plain", "hello"},
		"octet not on list":  {"application/octet-stream", "\x00\x01\x02\x03"},
		"bmp is not allowed": {"image/bmp", "BM" + strings.Repeat("\x00", 64)},
	} {
		up := upstream(t, serve(c.ct, []byte(c.body)))
		resp := rg.fetchOrig(up.URL+"/x", FlagPrivateNet)
		b, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusUnsupportedMediaType, resp.StatusCode, name)
		require.Empty(t, b, name)
	}
}

func TestSignatureAndFlagsChecked(t *testing.T) {
	rg := newRig(t)
	up := upstream(t, serve("image/png", pngBytes))
	orig := up.URL + "/a.png"
	enc := base64.RawURLEncoding.EncodeToString([]byte(orig))
	sig := Sign(secret, FlagPrivateNet, orig)

	require.Equal(t, 200, rg.get("/img/"+sig+"/1/"+enc).StatusCode)
	// Tampered flags: the same signature no longer covers them.
	for _, f := range []string{"0", "2", "3"} {
		require.Equal(t, 403, rg.get("/img/"+sig+"/"+f+"/"+enc).StatusCode, f)
	}
	// Tampered URL.
	other := base64.RawURLEncoding.EncodeToString([]byte(orig + "x"))
	require.Equal(t, 403, rg.get("/img/"+sig+"/1/"+other).StatusCode)
	// Wrong secret, truncated, empty, padded, foreign signature.
	require.Equal(t, 403, rg.get("/img/"+Sign([]byte("other"), 1, orig)+"/1/"+enc).StatusCode)
	require.Equal(t, 403, rg.get("/img/"+sig[:21]+"/1/"+enc).StatusCode)
	require.Equal(t, 403, rg.get("/img/"+sig+"A/1/"+enc).StatusCode)
	require.Equal(t, 403, rg.get("/img/"+strings.Repeat("A", 22)+"/1/"+enc).StatusCode)
	// Malformed flags and payloads never reach the network.
	for _, f := range []string{"01", "+1", "8", "-1", "x", "1.0", "99999999999999999999"} {
		require.Equal(t, 400, rg.get("/img/"+sig+"/"+f+"/"+enc).StatusCode, f)
	}
	require.Equal(t, 400, rg.get("/img/"+sig+"/1/!!!notb64").StatusCode)
	require.Equal(t, 400, rg.get("/img/"+sig+"/1/"+strings.Repeat("A", 6000)).StatusCode)
}

func TestSignedButUnsafeURLsRefused(t *testing.T) {
	rg := newRig(t)
	for _, u := range []string{
		"file:///etc/passwd", "gopher://127.0.0.1:70/", "ftp://example.com/a.png", "javascript:alert(1)", "data:image/png;base64,AAAA",
		"http://", "//example.com/a.png", "/relative.png", "http://user:pw@example.com/a.png",
	} {
		require.Equal(t, 400, rg.fetchOrig(u, 0).StatusCode, u)
	}
}

func TestPrivateAddressesBlockedUnlessFlagged(t *testing.T) {
	rg := newRig(t)
	up := upstream(t, serve("image/png", pngBytes))
	require.Equal(t, 502, rg.fetchOrig(up.URL+"/a.png", 0).StatusCode, "loopback test server")
	require.Equal(t, 502, rg.fetchOrig(up.URL+"/a.png", FlagInsecureTLS).StatusCode, "insecure TLS does not allow private nets")
	require.Equal(t, 200, rg.fetchOrig(up.URL+"/a.png", FlagPrivateNet).StatusCode)
	// Literal addresses the guard must catch without any DNS: metadata endpoint,
	// RFC 1918, loopback v4 and v6, CGNAT, NAT64 embedding of a private v4, unique-local.
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/a.png", "http://192.168.1.1/a.png", "http://127.0.0.1:1/a.png",
		"http://[::1]:1/a.png", "http://100.64.0.1/a.png", "http://[64:ff9b::7f00:1]/a.png", "http://[fd00::1]/a.png",
		"http://0.0.0.0/a.png", "http://[::ffff:127.0.0.1]:1/a.png", "http://2130706433/a.png",
	} {
		require.Equal(t, 502, rg.fetchOrig(u, 0).StatusCode, u)
	}
}

func TestRedirectFollowedWithoutReferer(t *testing.T) {
	rg := newRig(t)
	var seen http.Header
	var mu sync.Mutex
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final.png", http.StatusFound)
			return
		}
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	resp := rg.fetchOrig(up.URL+"/start", FlagPrivateNet, "Cookie", "kipple_session=secret", "Referer", "https://kipple.example/", "Authorization", "Bearer x")
	require.Equal(t, 200, resp.StatusCode)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "image/*", seen.Get("Accept"))
	require.Empty(t, seen.Get("Referer"))
	require.Empty(t, seen.Get("Cookie"))
	require.Empty(t, seen.Get("Authorization"))
	require.Contains(t, seen.Get("User-Agent"), "Kipple")
}

func TestTooManyRedirects(t *testing.T) {
	rg := newRig(t)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, r.URL.Path+"x", http.StatusFound) })
	require.Equal(t, 502, rg.fetchOrig(up.URL+"/a", FlagPrivateNet).StatusCode)
}

func TestUpstreamErrorStatus(t *testing.T) {
	rg := newRig(t)
	for _, code := range []int{404, 500, 403, 206} {
		up := upstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		require.Equal(t, 502, rg.fetchOrig(up.URL+"/a", FlagPrivateNet).StatusCode, code)
	}
	up := upstream(t, serve("image/png", nil)) // empty body
	require.Equal(t, 502, rg.fetchOrig(up.URL+"/a", FlagPrivateNet).StatusCode, "empty body")
}

func TestContentLengthOverCapIs502WithNoBody(t *testing.T) {
	rg := newRig(t, func(o *Options) { o.MaxBytes = 1000 })
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "5000")
		_, _ = w.Write(append(pngBytes, bytes.Repeat([]byte{1}, 5000-len(pngBytes))...))
	})
	resp := rg.fetchOrig(up.URL+"/big.png", FlagPrivateNet)
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, 502, resp.StatusCode)
	require.Empty(t, b)
}

func TestUndeclaredOverflowAbortsAndDoesNotBuffer(t *testing.T) {
	const capBytes = 1 << 20
	rg := newRig(t, func(o *Options) { o.MaxBytes = capBytes })
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.(http.Flusher).Flush() // no Content-Length: chunked
		_, _ = w.Write(pngBytes)
		chunk := bytes.Repeat([]byte{7}, 64<<10)
		for i := 0; i < 400; i++ { // 25 MiB, far past the cap
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	resp := rg.fetchOrig(up.URL+"/huge.png", FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode) // status went out before the overflow was known
	n, err := io.Copy(io.Discard, resp.Body)
	require.Error(t, err, "the connection must be aborted, not ended cleanly")
	require.LessOrEqual(t, n, int64(capBytes))

	runtime.GC()
	runtime.ReadMemStats(&after)
	require.Less(t, int64(after.HeapAlloc)-int64(before.HeapAlloc), int64(4<<20), "nothing was buffered")
}

func TestValidatorsPassThroughAnd304(t *testing.T) {
	rg := newRig(t)
	var gotINM, gotIMS string
	var mu sync.Mutex
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotINM, gotIMS = r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	resp := rg.fetchOrig(up.URL+"/a.png", FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, `"v1"`, resp.Header.Get("ETag"))
	require.Equal(t, "Wed, 01 Jan 2025 00:00:00 GMT", resp.Header.Get("Last-Modified"))

	resp = rg.fetchOrig(up.URL+"/a.png", FlagPrivateNet, "If-None-Match", `"v1"`, "If-Modified-Since", "Wed, 01 Jan 2025 00:00:00 GMT")
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, 304, resp.StatusCode)
	require.Empty(t, b)
	require.Equal(t, `"v1"`, resp.Header.Get("ETag"))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, `"v1"`, gotINM)
	require.NotEmpty(t, gotIMS)
}

func TestConcurrencyLimitAndWait(t *testing.T) {
	rg := newRig(t, func(o *Options) { o.Concurrency = 2; o.Wait = 150 * time.Millisecond })
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			started.Done()
			<-release
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := rg.fetchOrig(up.URL+"/hold?i="+strconv.Itoa(i), FlagPrivateNet)
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	started.Wait()
	// Both slots are held: a third request queues, then gives up.
	resp := rg.fetchOrig(up.URL+"/third.png", FlagPrivateNet)
	require.Equal(t, 503, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Retry-After"))
	close(release)
	wg.Wait()
	// The slots came back.
	require.Equal(t, 200, rg.fetchOrig(up.URL+"/after.png", FlagPrivateNet).StatusCode)
}

func TestQueuedRequestGetsSlotWithinWait(t *testing.T) {
	rg := newRig(t, func(o *Options) { o.Concurrency = 1; o.Wait = 2 * time.Second })
	release := make(chan struct{})
	started := make(chan struct{})
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp := rg.fetchOrig(up.URL+"/hold", FlagPrivateNet)
		_, _ = io.Copy(io.Discard, resp.Body)
	}()
	<-started
	go func() { time.Sleep(100 * time.Millisecond); close(release) }()
	require.Equal(t, 200, rg.fetchOrig(up.URL+"/queued.png", FlagPrivateNet).StatusCode)
	<-done
}

func TestUpstreamTimeout(t *testing.T) {
	rg := newRig(t, func(o *Options) { o.Timeout = 50 * time.Millisecond })
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	require.Equal(t, 502, rg.fetchOrig(up.URL+"/slow", FlagPrivateNet).StatusCode)
}

func TestRewriter(t *testing.T) {
	http1 := "http://img.example/a.png?x=1&y=2"
	https1 := "https://img.example/a.png"

	only := Rewriter{Secret: secret, Flags: FlagPrivateNet}
	p := only.Rewrite(http1)
	require.True(t, strings.HasPrefix(p, "/img/"), p)
	parts := strings.Split(p, "/") // "", img, sig, flags, b64
	require.Len(t, parts, 5)
	require.Equal(t, "1", parts[3])
	raw, err := base64.RawURLEncoding.DecodeString(parts[4])
	require.NoError(t, err)
	require.Equal(t, http1, string(raw))
	require.Equal(t, Sign(secret, 1, http1), parts[2])
	require.Len(t, parts[2], 22)

	require.Equal(t, https1, only.Rewrite(https1), "http_only leaves https alone")
	require.Equal(t, "", only.Rewrite("data:image/png;base64,AAAA"), "a URL that cannot be proxied is dropped, never passed on")
	require.Equal(t, "", only.Rewrite(""))

	all := Rewriter{Secret: secret, All: true}
	require.True(t, strings.HasPrefix(all.Rewrite(https1), "/img/"))
	require.Equal(t, "", all.Rewrite("/relative.png"))
	require.Equal(t, "0", strings.Split(all.Rewrite(https1), "/")[3])
	long := "http://x.example/" + strings.Repeat("a", 5000)
	require.Equal(t, "", all.Rewrite(long))
	for _, svg := range []string{"https://x.example/d.svg", "http://x.example/D.SVG?v=2", "https://x.example/d.svgz#a"} {
		require.Equal(t, "", all.Rewrite(svg), "SVG is refused by the proxy, so the page never asks for it: "+svg)
		require.Equal(t, "", only.Rewrite(svg))
	}
	require.NotEqual(t, "", all.Rewrite("https://x.example/svg/a.png"))
}
