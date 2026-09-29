package setup

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenRefusal(t *testing.T) {
	g := Gate{Trusted: []netip.Addr{netip.MustParseAddr("192.0.2.20")}, Tailnet: func() bool { return true }}
	type tc struct {
		name    string
		peer    string
		host    string
		hdr     map[string]string
		openLAN bool
		want    string
	}
	for _, c := range []tc{
		{"loopback v4", "127.0.0.1:5000", "127.0.0.1", nil, false, ""},
		{"loopback v6", "[::1]:5000", "localhost", nil, false, ""},
		{"mapped loopback", "[::ffff:127.0.0.1]:5000", "localhost", nil, false, ""},
		{"tailscale v4", "100.101.102.103:5000", "nas", nil, false, ""},
		{"tailscale v6", "[fd7a:115c:a1e0::1]:5000", "nas", nil, false, ""},
		// In a container even this computer arrives from the bridge gateway, which
		// cannot be told from the LAN: it needs the LAN opt-in, whatever Host it names.
		{"container gateway naming localhost", "172.17.0.1:5000", "localhost", nil, false, RefusePeer},
		{"container gateway with open_lan", "172.17.0.1:5000", "localhost", nil, true, ""},
		{"docker desktop gateway with open_lan", "192.168.65.1:5000", "localhost", nil, true, ""},
		{"another bridge peer", "172.17.0.5:5000", "127.0.0.1", nil, false, RefusePeer},
		{"lan refused by default", "192.168.1.20:5000", "192.168.1.10", nil, false, RefusePeer},
		{"lan with open_lan", "192.168.1.20:5000", "192.168.1.10", nil, true, ""},
		{"ula with open_lan", "[fd00::5]:5000", "nas", nil, true, ""},
		{"public peer even with open_lan", "203.0.113.9:5000", "192.168.1.10", nil, true, RefusePeer},
		{"cgnat outside tailscale", "100.63.0.1:5000", "nas", nil, false, RefusePeer},
		{"trusted proxy", "192.0.2.20:5000", "127.0.0.1", nil, false, RefuseForwarded},
		{"cloudflared on loopback", "127.0.0.1:5000", "127.0.0.1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, false, RefuseForwarded},
		{"access jwt", "127.0.0.1:5000", "127.0.0.1", map[string]string{"Cf-Access-Jwt-Assertion": "x"}, false, RefuseForwarded},
		{"forwarded", "127.0.0.1:5000", "localhost", map[string]string{"Forwarded": "for=203.0.113.9"}, false, RefuseForwarded},
		{"xff", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": "203.0.113.9"}, false, RefuseForwarded},
		{"x-real-ip", "127.0.0.1:5000", "localhost", map[string]string{"X-Real-IP": "203.0.113.9"}, false, RefuseForwarded},
		{"xfh", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-Host": "rss.example.com"}, false, RefuseForwarded},
		{"empty header still counts", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": ""}, false, RefuseForwarded},
		{"tailscale serve", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "box.tail1234.ts.net"}, false, ""},
		{"tailscale funnel", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "Tailscale-Funnel-Request": "?1"}, false, RefuseForwarded},
		{"funnel header alone", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"Tailscale-Funnel-Request": "?1"}, false, RefuseForwarded},
		{"ts.net serve still refuses cloudflare", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, false, RefuseForwarded},
		{"ts.net from the lan is not serve", "192.168.1.20:5000", "box.tail1234.ts.net", map[string]string{"X-Forwarded-For": "1.2.3.4"}, true, RefuseForwarded},
		{"unparsable peer", "pipe", "localhost", nil, false, RefusePeer},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/auth/open", nil)
			r.RemoteAddr = c.peer
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			require.Equal(t, c.want, g.OpenRefusal(r, c.host, true, c.openLAN))
		})
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	require.Equal(t, RefuseHost, g.OpenRefusal(r, "evil.example", false, true), "the Host gate comes first")

	// Without a local tailnet address the CGNAT range is not the tailnet: only the LAN opt-in admits it.
	r.RemoteAddr = "100.101.102.103:1"
	require.Equal(t, RefusePeer, Gate{}.OpenRefusal(r, "nas", true, false))
	require.Equal(t, "", Gate{}.OpenRefusal(r, "nas", true, true))
	r.RemoteAddr = "[fd7a:115c:a1e0::9]:1"
	require.Equal(t, RefusePeer, Gate{}.OpenRefusal(r, "nas", true, false))
}

// Granting access also needs a browser Origin naming the host the request was
// sent to: a same-machine proxy that rewrites Host forwards the public Origin.
func TestSignInRefusalChecksTheOrigin(t *testing.T) {
	g := Gate{}
	req := func(host, origin string) string {
		r := httptest.NewRequest("POST", "/api/auth/open", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		h, ok := NormalizeHost(host)
		return g.SignInRefusal(r, h, ok && HostAllowed(h, nil), false)
	}
	require.Equal(t, "", req("127.0.0.1:1919", "http://127.0.0.1:1919"))
	require.Equal(t, "", req("localhost:1919", "http://LOCALHOST:1919"))
	require.Equal(t, RefuseForwarded, req("127.0.0.1:1919", ""), "no Origin")
	require.Equal(t, RefuseForwarded, req("127.0.0.1:1919", "null"))
	require.Equal(t, RefuseForwarded, req("127.0.0.1:1919", "https://rss.example.com"), "nginx rewrote Host")
	require.Equal(t, RefuseForwarded, req("127.0.0.1:1919", "http://127.0.0.1:8080"))
	require.Equal(t, RefuseForwarded, req("127.0.0.1:1919", "::"))
	require.Equal(t, RefuseHost, req("evil.example:1919", "http://evil.example:1919"))
}

func TestTailnetCheckIsCached(t *testing.T) {
	check := TailnetCheck()
	first := check()
	require.Equal(t, first, check(), "the same answer within the recheck window")
	require.NotPanics(t, func() { _ = LocalTailnet() })
}
