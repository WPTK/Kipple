package imgproxy

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// FuzzProxyPath: hostile {sig}/{flags}/{u} path values never panic and are
// always refused (400/403); a correctly signed but non-http(s) or malformed
// origin is refused with 400 before anything is fetched.
func FuzzProxyPath(f *testing.F) {
	f.Add("", "0", "")
	f.Add("abc", "-1", "!!!")
	f.Add("abc", "99999999999999999999", base64.RawURLEncoding.EncodeToString([]byte("https://x/y.png")))
	f.Add("abc", "007", base64.RawURLEncoding.EncodeToString([]byte("ftp://x")))
	f.Add(Sign(secret, 0, "file:///etc/passwd"), "0", base64.RawURLEncoding.EncodeToString([]byte("file:///etc/passwd")))
	h := New(Options{Secret: secret})
	f.Fuzz(func(t *testing.T, sig, flags, enc string) {
		serve := func(sig, flags, enc string) int {
			r := httptest.NewRequest("GET", "/img/x/0/x", nil)
			r.SetPathValue("sig", sig)
			r.SetPathValue("flags", flags)
			r.SetPathValue("u", enc)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w.Code
		}
		if c := serve(sig, flags, enc); c != http.StatusBadRequest && c != http.StatusForbidden {
			t.Fatalf("unsigned request got %d", c)
		}
		// Validly signed: decode enc as raw bytes so the fuzzer controls the origin.
		orig := string(bytes.TrimSpace([]byte(sig + enc)))
		if u, err := url.Parse(orig); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return // would fetch
		}
		n, _ := strconv.Atoi(flags)
		if n < 0 || n > maxFlags {
			n = 0
		}
		e := base64.RawURLEncoding.EncodeToString([]byte(orig))
		if len(e) > base64.RawURLEncoding.EncodedLen(maxURLLen) {
			return
		}
		if c := serve(Sign(secret, n, orig), strconv.Itoa(n), e); c != http.StatusBadRequest {
			t.Fatalf("signed non-http origin %q got %d", orig, c)
		}
	})
}

// FuzzWebPCost: the WebP allocation model never panics on arbitrary bytes and
// an accepted file has a positive price.
func FuzzWebPCost(f *testing.F) {
	f.Add(webpBytes, uint8(40), uint8(30))
	f.Add(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f"), 0, 0, 0, 0), uint8(1), uint8(1))
	f.Add(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00"), make([]byte, 10)...), uint8(9), uint8(9))
	f.Fuzz(func(t *testing.T, data []byte, w, h uint8) {
		if n, ok := webpDecodeCost(bytes.NewReader(data), int64(len(data)), int(w), int(h)); ok && n <= 0 {
			t.Fatal("accepted with a non-positive cost")
		}
	})
}
