package discover

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzLinks: link extraction from arbitrary HTML never panics, is bounded, and
// only offers absolute http(s) URLs that are not the page itself.
func FuzzLinks(f *testing.F) {
	for _, s := range []string{
		"", `<link rel="alternate" type="application/rss+xml" href="/feed">`,
		`<link rel="ALTERNATE  stylesheet" type="application/atom+xml" href="//h/a.xml" title="T">`,
		`<link rel=alternate type=application/feed+json href="javascript:x">`, `<base href="http://[::1"><link rel=alternate type=application/rss+xml href=x>`,
		strings.Repeat(`<link rel=alternate type=application/rss+xml href=/f>`, 100),
	} {
		f.Add(s, "https://example.com/page")
	}
	f.Fuzz(func(t *testing.T, body, base string) {
		cs := links([]byte(body), base, "https://example.com/self")
		if len(cs) > maxCandidates {
			t.Fatalf("%d candidates", len(cs))
		}
		for _, c := range cs {
			u, err := url.Parse(c.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				t.Fatalf("bad candidate %q", c.URL)
			}
			if c.URL == "https://example.com/self" {
				t.Fatal("self offered as a candidate")
			}
		}
	})
}
