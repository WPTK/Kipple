package imgproxy

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"golang.org/x/image/webp"
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

// FuzzExifOrientation: the EXIF walk never panics on arbitrary bytes and only
// returns an orientation from 1 to 8. Seeded with a short APP1 length (the
// declared length 2 once sliced past the segment's own end).
func FuzzExifOrientation(f *testing.F) {
	f.Add([]byte("\xFF\xD8\xFF\xE1\x00\x02Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08"))
	f.Add([]byte("\xFF\xD8\xFF\xE1\x00\x07Exif\x00\x00"))
	f.Add([]byte("\xFF\xD8\xFF\xE0\x00\x00\xFF\xE1"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if o := exifOrientation(data); o < 1 || o > 8 {
			t.Fatalf("orientation %d", o)
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

// FuzzWebPLosslessCost: the WebP walk, at the size DecodeConfig reads from the
// same bytes (as planThumb calls it), never panics, and an accepted file is
// priced at least at its decoded picture (NRGBA, 4 bytes per pixel, for a
// lossless file; 1 for any WebP). Seeded with synthetic lossless streams of
// every shape and the libwebp files in golang.org/x/image's test data.
func FuzzWebPLosslessCost(f *testing.F) {
	for _, s := range []vp8lSpec{
		{w: 60, h: 40},
		{w: 60, h: 40, predictor: true, crossColor: true, subGreen: true, palette: true, cacheBits: 5, fullTrees: true},
		{w: 60, h: 40, cacheBits: 11, groups: 7},
		{w: 60, h: 40, groups: 40, sparse: true},
	} {
		f.Add(riffWebP(webpChunk("VP8L", synthVP8L(s, true))))
	}
	f.Add(synthWebPAlpha(60, 40, &vp8lSpec{w: 60, h: 40, palette: true, groups: 3}))
	for _, b := range libwebpFiles(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := webp.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width*cfg.Height > 1<<20 {
			return // a larger picture only makes each run slower (sub-images are walked pixel by pixel)
		}
		n, ok := webpDecodeCost(bytes.NewReader(data), int64(len(data)), cfg.Width, cfg.Height)
		if !ok {
			return
		}
		px := int64(cfg.Width) * int64(cfg.Height)
		if n < px {
			t.Fatalf("%dx%d accepted at %d bytes", cfg.Width, cfg.Height, n)
		}
		if len(data) >= 16 && string(data[12:16]) == "VP8L" && n < 4*px {
			t.Fatalf("lossless %dx%d accepted at %d bytes, under its NRGBA picture", cfg.Width, cfg.Height, n)
		}
	})
}
