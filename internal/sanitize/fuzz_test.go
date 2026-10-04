package sanitize

import (
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

var xssSeeds = []string{
	"", "<p>hi</p>", `<script>alert(1)</script>`, `<img src=x onerror=alert(1)>`, `<a href="javascript:alert(1)">x</a>`,
	`<a href="  JaVa&#x09;Script:alert(1)">x</a>`, `<svg onload=alert(1)>`, `<iframe src="https://www.youtube.com/embed/abc"></iframe>`,
	`<video src="http://x/y.mp4" autoplay onplay=x></video>`, `<a href="#fn1">1</a><a id=fn1>`, `<style>@import 'x'</style>`,
	`<math><mtext><table><mglyph><style><img src=x onerror=alert(1)>`, `<div style="background:url(javascript:x)">`,
	`<a href="data:text/html,<script>x</script>">x</a>`, `<form action=javascript:x><button>`, `<base href="//evil/">`,
	`<object data="x"></object><embed src=x>`, `<a href="https://fragment.kipple.invalid/0/0">x</a>`,
	`<noscript><p title="</noscript><img src=x onerror=alert(1)>">`, "<a href=\"\x00javascript:x\">",
}

var jsScheme = regexp.MustCompile(`(?i)^[\x00-\x20]*j[\x00-\x20]*a[\x00-\x20]*v[\x00-\x20]*a[\x00-\x20]*s[\x00-\x20]*c[\x00-\x20]*r[\x00-\x20]*i[\x00-\x20]*p[\x00-\x20]*t[\x00-\x20]*:`)

// assertSafeHTML parses out with the same tokenizer a browser-ish parser uses
// and fails on any live script, event handler or javascript:/data: URL.
func assertSafeHTML(t *testing.T, label, in, out string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(out))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		switch tok.Data {
		case "script", "object", "embed", "base", "form", "style", "link", "meta":
			t.Fatalf("%s: <%s> survived\nin:  %q\nout: %q", label, tok.Data, in, out)
		}
		for _, a := range tok.Attr {
			k := strings.ToLower(a.Key)
			if strings.HasPrefix(k, "on") {
				t.Fatalf("%s: handler %s survived\nin:  %q\nout: %q", label, a.Key, in, out)
			}
			if k == "href" || k == "src" || k == "srcset" || k == "action" || k == "formaction" || k == "poster" {
				if jsScheme.MatchString(a.Val) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "data:text/html") {
					t.Fatalf("%s: dangerous %s=%q survived\nin:  %q\nout: %q", label, a.Key, a.Val, in, out)
				}
			}
		}
	}
}

// FuzzContent: the ingest pipeline never panics and its output is safe HTML;
// re-sanitizing it does not introduce anything unsafe.
func FuzzContent(f *testing.F) {
	for _, s := range xssSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		out, text := Content(src, "https://example.com/post/")
		assertSafeHTML(t, "Content", src, out)
		_ = text
		out2, _ := Content(out, "https://example.com/post/")
		assertSafeHTML(t, "Content(Content)", src, out2)
	})
}

// FuzzServeHTML: the serve-time pass over stored (already sanitized) HTML, and
// over hostile HTML too, never panics, stays safe and is idempotent.
func FuzzServeHTML(f *testing.F) {
	for _, s := range xssSeeds {
		f.Add(s)
		c, _ := Content(s, "https://example.com/")
		f.Add(c)
	}
	opt := ServeOptions{
		Image:         func(u string) string { return "/img/" + u },
		Thumb:         func(u string) string { return "/thumb/" + u },
		StripTracking: true,
	}
	f.Fuzz(func(t *testing.T, src string) {
		stored, _ := Content(src, "https://example.com/")
		out := ServeHTML(stored, opt)
		assertSafeHTML(t, "ServeHTML(Content)", src, out)
		// Idempotence is a property of the pass without the image proxy (which by design wraps URLs again).
		plain := ServeHTML(stored, ServeOptions{StripTracking: true})
		if again := ServeHTML(plain, ServeOptions{StripTracking: true}); again != plain {
			t.Fatalf("not idempotent\nstored: %q\nfirst:  %q\nsecond: %q", stored, plain, again)
		}
	})
}

// FuzzURLHelpers: URL resolution, tracking removal, absolutize and plain text never panic;
// ResolveURL only ever returns an absolute http(s) URL or nothing.
func FuzzURLHelpers(f *testing.F) {
	for _, s := range []string{"", "/a", "//h/a", "javascript:x", "https://a/b?utm_source=x&id=1#f", "http://[::1", "\x00", "mailto:a@b"} {
		f.Add(s, "https://example.com/base/")
	}
	f.Fuzz(func(t *testing.T, ref, base string) {
		if u := ResolveURL(ref, base); u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			t.Fatalf("ResolveURL(%q,%q)=%q is not http(s)", ref, base, u)
		}
		_ = StripTracking(ref)
		_ = PlainText(ref)
		_ = LeadImage(ref, base)
		_ = ServeHTML(ref, ServeOptions{Image: func(s string) string { return s }})
	})
}
