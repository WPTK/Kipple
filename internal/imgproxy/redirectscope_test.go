package imgproxy

import (
	"bytes"
	"io"
	"net/http"
	"sync"
	"testing"
)

// hostRT answers by host name, without any network, and records which
// transport variant each hop went through.
type hostRT struct {
	priv, insecure bool
	mu             *sync.Mutex
	hops           *[]string
}

func (r hostRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	v := "guarded"
	if r.priv || r.insecure {
		v = "granted"
	}
	*r.hops = append(*r.hops, req.URL.Hostname()+" "+v)
	r.mu.Unlock()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
	switch req.URL.Hostname() {
	case "feed.lan":
		resp.StatusCode = http.StatusFound
		resp.Header.Set("Location", req.URL.Query().Get("to"))
		resp.Body = io.NopCloser(bytes.NewReader(nil))
	default:
		resp.Header.Set("Content-Type", "image/png")
		resp.Body = io.NopCloser(bytes.NewReader(pngBytes))
	}
	return resp, nil
}

// A private-network or insecure-TLS grant is signed for the image's host only:
// a redirect hop to another host goes through the guarded transport, a hop to a
// subdomain or the www twin keeps the grant.
func TestRedirectHopsAreScopedToTheImageHost(t *testing.T) {
	cases := []struct {
		name, to string
		flags    int
		want     []string
	}{
		{"private grant, other host", "http://router.lan/x.png", FlagPrivateNet, []string{"feed.lan granted", "router.lan guarded"}},
		{"insecure grant, other host", "https://cdn.example/x.png", FlagInsecureTLS, []string{"feed.lan granted", "cdn.example guarded"}},
		{"private grant, subdomain", "http://img.feed.lan/x.png", FlagPrivateNet, []string{"feed.lan granted", "img.feed.lan granted"}},
		{"no grant", "http://router.lan/x.png", 0, []string{"feed.lan guarded", "router.lan guarded"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var hops []string
			rg := newRig(t, func(o *Options) {
				o.Transport = func(allowPrivate, insecure bool) http.RoundTripper {
					return hostRT{priv: allowPrivate, insecure: insecure, mu: &mu, hops: &hops}
				}
			})
			resp := rg.fetchOrig("http://feed.lan/r?to="+c.to, c.flags)
			_ = resp.Body.Close()
			mu.Lock()
			defer mu.Unlock()
			if len(hops) != len(c.want) {
				t.Fatalf("hops = %v, want %v", hops, c.want)
			}
			for i := range hops {
				if hops[i] != c.want[i] {
					t.Fatalf("hops = %v, want %v", hops, c.want)
				}
			}
		})
	}
}
