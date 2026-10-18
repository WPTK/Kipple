package main

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	ts := httptest.NewServer(newServer().routes())
	t.Cleanup(ts.Close)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	return ts, c
}

func get(t *testing.T, c *http.Client, url string, hdr ...string) (*http.Response, []byte, error) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, b, err
}

func TestOKAndEncodings(t *testing.T) {
	ts, c := start(t)
	resp, b, err := get(t, c, ts.URL+"/ok")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(b), "<rss") {
		t.Fatalf("ok: %v %v", resp, err)
	}
	for path, open := range map[string]func(io.Reader) (io.Reader, error){
		"/gzip":        func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) },
		"/deflate":     func(r io.Reader) (io.Reader, error) { return zlib.NewReader(r) },
		"/deflate-raw": func(r io.Reader) (io.Reader, error) { return flate.NewReader(r), nil },
	} {
		resp, b, err := get(t, c, ts.URL+path)
		if err != nil {
			t.Fatal(path, err)
		}
		if resp.Header.Get("Content-Encoding") == "" {
			t.Errorf("%s: no Content-Encoding", path)
		}
		zr, err := open(strings.NewReader(string(b)))
		if err != nil {
			t.Fatal(path, err)
		}
		plain, err := io.ReadAll(zr)
		if err != nil || string(plain) != goodFeed {
			t.Errorf("%s: round trip failed: %v", path, err)
		}
	}
	if _, b, _ := get(t, c, ts.URL+"/enc/latin1"); !strings.Contains(string(b), "\xe9") {
		t.Error("latin1 route should carry raw 0xE9 bytes")
	}
	if _, b, _ := get(t, c, ts.URL+"/chunked"); string(b) != goodFeed {
		t.Error("chunked body differs")
	}
}

func TestTruncatedAndReset(t *testing.T) {
	ts, c := start(t)
	for _, p := range []string{"/truncated", "/truncated-chunked"} {
		if _, _, err := get(t, c, ts.URL+p); err == nil {
			t.Errorf("%s: expected a read error, got none", p)
		}
	}
	if _, _, err := get(t, c, ts.URL+"/reset-conn"); err == nil {
		t.Error("reset-conn: expected an error")
	}
}

func TestRedirects(t *testing.T) {
	ts, c := start(t)
	resp, b, err := get(t, c, ts.URL+"/redirect/5")
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(b), "<rss") {
		t.Fatalf("5-hop chain should end at the feed: %v %v", resp, err)
	}
	for _, p := range []string{"/loop-a", "/loop-self"} {
		_, _, err := get(t, c, ts.URL+p)
		if err == nil || !strings.Contains(err.Error(), "redirects") {
			t.Errorf("%s: want a stopped-after-redirects error, got %v", p, err)
		}
	}
	nr := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _, _ = get(t, nr, ts.URL+"/redirect-to?to=http://10.0.0.1/x&code=307")
	if resp == nil || resp.StatusCode != 307 || resp.Header.Get("Location") != "http://10.0.0.1/x" {
		t.Errorf("redirect-to: %v", resp)
	}
}

func Test304Variants(t *testing.T) {
	ts, c := start(t)
	if resp, _, _ := get(t, c, ts.URL+"/304-always"); resp.StatusCode != 304 {
		t.Errorf("304-always gave %d", resp.StatusCode)
	}
	resp, _, _ := get(t, c, ts.URL+"/304-etag")
	if resp.StatusCode != 200 || resp.Header.Get("ETag") == "" {
		t.Fatalf("304-etag first fetch: %d", resp.StatusCode)
	}
	if resp, _, _ := get(t, c, ts.URL+"/304-etag", "If-None-Match", resp.Header.Get("ETag")); resp.StatusCode != 304 {
		t.Errorf("304-etag conditional gave %d", resp.StatusCode)
	}
	if resp, _, _ := get(t, c, ts.URL+"/304-lastmod", "If-Modified-Since", "Mon, 01 Jan 2024 00:00:00 GMT"); resp.StatusCode != 304 {
		t.Errorf("304-lastmod conditional gave %d", resp.StatusCode)
	}
	if resp, _, _ := get(t, c, ts.URL+"/304-ignores-validators", "If-None-Match", `"bf-ignored"`); resp.StatusCode != 200 {
		t.Errorf("304-ignores-validators gave %d", resp.StatusCode)
	}
	var codes []int
	for i := 0; i < 4; i++ {
		resp, _, _ := get(t, c, ts.URL+"/304-then-200?n=2", "If-None-Match", `"bf-seq"`)
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 304 || codes[1] != 304 || codes[2] != 200 {
		t.Errorf("304-then-200 sequence %v", codes)
	}
}

func TestStatusBurstsAndRetryAfter(t *testing.T) {
	ts, c := start(t)
	var codes []int
	for i := 0; i < 5; i++ {
		resp, _, _ := get(t, c, ts.URL+"/5xx-burst?n=3")
		codes = append(codes, resp.StatusCode)
	}
	if want := []int{503, 503, 503, 200, 200}; !equal(codes, want) {
		t.Errorf("burst %v, want %v", codes, want)
	}
	get(t, c, ts.URL+"/reset")
	if resp, _, _ := get(t, c, ts.URL+"/5xx-burst?n=1&code=502"); resp.StatusCode != 502 {
		t.Errorf("after reset the burst should restart, got %d", resp.StatusCode)
	}
	resp, _, _ := get(t, c, ts.URL+"/429?after=77")
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "77" {
		t.Errorf("429: %d %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	resp, _, _ = get(t, c, ts.URL+"/429-date?after=60")
	if _, err := http.ParseTime(resp.Header.Get("Retry-After")); err != nil {
		t.Errorf("429-date header %q: %v", resp.Header.Get("Retry-After"), err)
	}
	if resp, _, _ := get(t, c, ts.URL+"/status/418"); resp.StatusCode != 418 {
		t.Errorf("status/418 gave %d", resp.StatusCode)
	}
}

func TestSlowDripAndHuge(t *testing.T) {
	ts, c := start(t)
	start := time.Now()
	_, b, err := get(t, c, ts.URL+"/slow-drip?chunk=64&delay=20")
	if err != nil || string(b) != goodFeed {
		t.Fatalf("slow-drip body: %v", err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("slow-drip finished too fast to be dripping")
	}
	short := &http.Client{Timeout: 100 * time.Millisecond}
	if _, _, err := get(t, short, ts.URL+"/slow-drip?chunk=1&delay=50"); err == nil {
		t.Error("a client timeout should cut a slow drip short")
	}
	_, b, err = get(t, c, ts.URL+"/huge?mb=2")
	if err != nil || len(b) < 2<<20 {
		t.Errorf("huge gave %d bytes, err %v", len(b), err)
	}
}

func TestSelfSignedTLS(t *testing.T) {
	ts := httptest.NewTLSServer(newServer().routes())
	defer ts.Close()
	strict := &http.Client{Timeout: 5 * time.Second}
	_, err := strict.Get(ts.URL + "/ok")
	var ua interface{ Error() string }
	if err == nil || !errors.As(err, &ua) || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a default client should reject the self-signed certificate, got %v", err)
	}
	loose := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := loose.Get(ts.URL + "/ok")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("insecure client: %v", err)
	}
	if _, err := selfSigned(); err != nil {
		t.Errorf("selfSigned: %v", err)
	}
}

func TestLoopbackOnly(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": false, "0.0.0.0:1": false, ":1": false, "192.168.3.1:1": false, "nonsense": false} {
		if loopback(addr) != want {
			t.Errorf("loopback(%q) = %v", addr, !want)
		}
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
