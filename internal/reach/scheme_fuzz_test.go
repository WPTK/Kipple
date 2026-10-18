package reach

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// FuzzSchemeAndPublicURL: the scheme Kipple believes a request used. For a public URL that passes
// NormalizePublicURL and any Host header and X-Forwarded-Proto from an untrusted peer without TLS, "https" is
// answered only for the exact origin of an https public URL (its host, and no port or its port), and the
// answer is always "http" or "https". NormalizePublicURL is idempotent and what it accepts, CheckPublicURL
// accepts and Host can read.
func FuzzSchemeAndPublicURL(f *testing.F) {
	for _, s := range [][3]string{
		{"https://rss.example.com", "rss.example.com", "https"},
		{"https://rss.example.com", "RSS.EXAMPLE.COM.:443", ""},
		{"https://rss.example.com", "rss.example.com:1919", "https"},
		{"https://rss.example.com:8443", "rss.example.com:8443", ""},
		{"https://rss.example.com:8443", "rss.example.com", ""},
		{"https://rss.example.com", "rss.example.com:443@evil.example", ""},
		{"https://rss.example.com", "evil.example/rss.example.com", ""},
		{"http://nas.local:1919", "nas.local:1919", "https"},
		{"https://[::1]:8443", "[::1]:8443", ""},
		{"https://bücher.example", "xn--bcher-kva.example", ""},
		{"https://rss.example.com/", "rss.example.com:", ""},
		{"https://rss.example.com", "rss.example.com:0443", ""},
	} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, publicURL, hostHeader, xfp string) {
		norm, err := NormalizePublicURL(publicURL)
		if err == nil {
			if err := CheckPublicURL(norm); err != nil {
				t.Fatalf("NormalizePublicURL(%q) = %q, which CheckPublicURL refuses: %v", publicURL, norm, err)
			}
			again, err := NormalizePublicURL(norm)
			if err != nil || again != norm {
				t.Fatalf("NormalizePublicURL not idempotent: %q -> %q -> %q (%v)", publicURL, norm, again, err)
			}
			if norm != "" && Host(norm) == "" {
				t.Fatalf("a valid public URL %q has no host the gate can read", norm)
			}
		}
		if err != nil {
			norm = ""
		}

		st := &State{PublicURL: norm, PublicHost: Host(norm)}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = hostHeader
		r.RemoteAddr = "203.0.113.9:4000" // not a trusted proxy: nothing is trusted here
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		got := st.Scheme(r)
		if got != "http" && got != "https" {
			t.Fatalf("Scheme = %q", got)
		}
		if got != "https" {
			return
		}
		// An untrusted request is https only for the public URL's exact origin.
		pu, perr := url.Parse(norm)
		if perr != nil || pu.Scheme != "https" {
			t.Fatalf("https for a public URL that is not https: %q, Host %q", norm, hostHeader)
		}
		hu, herr := url.Parse("https://" + hostHeader)
		if herr != nil || hu.User != nil || strings.ContainsAny(hostHeader, "/?#@\\ ") {
			t.Fatalf("https for a Host header that is not a bare host[:port]: %q (public %q)", hostHeader, norm)
		}
		if !strings.EqualFold(strings.TrimSuffix(hu.Hostname(), "."), strings.TrimSuffix(pu.Hostname(), ".")) &&
			Host("https://"+hostHeader) != st.PublicHost {
			t.Fatalf("https for another host: Host %q, public %q", hostHeader, norm)
		}
		want := pu.Port()
		if want == "" {
			want = "443"
		}
		if p := hu.Port(); p != "" && p != want {
			t.Fatalf("https for another port: Host %q, public %q", hostHeader, norm)
		}
	})
}
