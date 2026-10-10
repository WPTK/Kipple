package api

import (
	"testing"

	"github.com/WPTK/kipple/internal/imgproxy"
)

// Both network exceptions apply to the feed's own host, its subdomains and its
// bare/www twin, and to nothing else.
func TestScopeNetExceptionsToFeedHost(t *testing.T) {
	t.Parallel()
	secret := []byte("0123456789abcdef0123456789abcdef")
	flags := imgproxy.FlagPrivateNet | imgproxy.FlagInsecureTLS
	rw := imgproxy.Rewriter{Secret: secret, Flags: flags, All: true}
	bare := imgproxy.Rewriter{Secret: secret, All: true}
	scoped := scopePrivateNet(rw, "nas.lan")
	for _, raw := range []string{"http://nas.lan/a.png", "http://img.nas.lan/a.png", "http://www.nas.lan/a.png", "https://NAS.lan/a.png"} {
		if got, want := scoped(raw), rw.Rewrite(raw); got != want {
			t.Errorf("%s: got %q, want the granted %q", raw, got, want)
		}
	}
	for _, raw := range []string{"http://cdn.example/a.png", "http://192.168.1.1/a.png", "http://nas.lan.evil.example/a.png"} {
		if got, want := scoped(raw), bare.Rewrite(raw); got != want {
			t.Errorf("%s: got %q, want no grants %q", raw, got, want)
		}
	}
	if got, want := scopePrivateNet(rw, "")("http://nas.lan/a.png"), bare.Rewrite("http://nas.lan/a.png"); got != want {
		t.Errorf("unknown feed host: got %q, want no grants %q", got, want)
	}
}
