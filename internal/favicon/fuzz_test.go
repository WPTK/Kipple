package favicon

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzIconLinks: icon-link extraction from arbitrary HTML never panics, is
// bounded, offers only absolute http(s) URLs without a fragment, never an .svg
// path, never a duplicate, and returns them ranked.
func FuzzIconLinks(f *testing.F) {
	for _, s := range []string{
		"", `<link rel="icon" href="/favicon.png" sizes="32x32">`,
		`<link rel="shortcut icon" href="favicon.ico"><link rel=apple-touch-icon href=//cdn.example.net/a.png>`,
		`<base href="/sub/"><link rel=icon href=x.png sizes="16x16 any 999999x2">`,
		`<link rel=icon type=image/svg+xml href=/a><link rel=icon href=/b.SVG#x>`,
		`<base href="http://[::1"><link rel=icon href="javascript:x">`, `<link rel=icon href="data:image/png;base64,AA">`,
		strings.Repeat(`<link rel=icon href=/f>`, 100),
	} {
		f.Add(s, "https://example.com/page")
	}
	f.Fuzz(func(t *testing.T, body, base string) {
		cs := iconLinks([]byte(body), base)
		if len(cs) > maxLinks {
			t.Fatalf("%d candidates", len(cs))
		}
		seen := map[string]bool{}
		for i, c := range cs {
			u, err := url.Parse(c.URL)
			if err != nil || !httpURL(u) || u.Fragment != "" {
				t.Fatalf("bad candidate %q", c.URL)
			}
			if strings.HasSuffix(strings.ToLower(u.Path), ".svg") {
				t.Fatalf("svg candidate %q", c.URL)
			}
			if seen[c.URL] {
				t.Fatalf("duplicate %q", c.URL)
			}
			seen[c.URL] = true
			if c.Size < 0 || c.Size > 10000 {
				t.Fatalf("size %d", c.Size)
			}
			if i > 0 && less(c, cs[i-1]) {
				t.Fatalf("not ranked: %+v before %+v", cs[i-1], c)
			}
		}
	})
}

// less is rank's order, restated for the fuzz check.
func less(a, b candidate) bool {
	x := []candidate{b, a}
	rank(x)
	return x[0] == a && a != b
}

// FuzzSniff: the raster check never panics and only ever names the five
// allowed types.
func FuzzSniff(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte{0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0, 4, 0, 0, 0, 22, 0, 0, 0, 1, 2, 3, 4})
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "))
	f.Add([]byte("GIF89a"))
	f.Add([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		ct, err := sniff(b)
		if err != nil {
			return
		}
		switch ct {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon":
		default:
			t.Fatalf("type %q", ct)
		}
	})
}
