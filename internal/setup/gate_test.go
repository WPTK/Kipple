package setup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This machine's Tailscale addresses in the tests.
var (
	tsLocal4 = netip.MustParseAddr("100.100.100.1")
	tsLocal6 = netip.MustParseAddr("fd7a:115c:a1e0::100")
)

func tailnetUp() []netip.Addr { return []netip.Addr{tsLocal4, tsLocal6} }

// arrivedOn records the local address the connection arrived on, as net/http does.
func arrivedOn(r *http.Request, local string) *http.Request {
	if local == "" {
		return r
	}
	ap := netip.MustParseAddrPort(local)
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.TCPAddrFromAddrPort(ap)))
}

func TestOpenRefusal(t *testing.T) {
	g := Gate{Trusted: func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")} }, Tailnet: tailnetUp}
	type tc struct {
		name  string
		peer  string
		host  string
		hdr   map[string]string
		want  string
		local string // the address the connection arrived on ("": unknown)
	}
	for _, c := range []tc{
		// Local peers: this computer, link-local, RFC 1918 and ULA, whatever address
		// they reached (a container's gateway is one of them).
		{"loopback v4", "127.0.0.1:5000", "127.0.0.1", nil, "", ""},
		{"loopback v6", "[::1]:5000", "localhost", nil, "", ""},
		{"mapped loopback", "[::ffff:127.0.0.1]:5000", "localhost", nil, "", ""},
		{"link-local v4", "169.254.7.7:5000", "nas", nil, "", ""},
		{"link-local v6", "[fe80::1]:5000", "nas", nil, "", ""},
		{"link-local v6 with a zone", "[fe80::1%eth0]:5000", "nas", nil, "", ""},
		{"10/8 first", "10.0.0.0:5000", "nas", nil, "", ""},
		{"10/8 last", "10.255.255.255:5000", "nas", nil, "", ""},
		{"172.15.255.255 is public", "172.15.255.255:5000", "nas", nil, RefusePeer, ""},
		{"172.16.0.0", "172.16.0.0:5000", "nas", nil, "", ""},
		{"172.31.255.255", "172.31.255.255:5000", "nas", nil, "", ""},
		{"172.32.0.0 is public", "172.32.0.0:5000", "nas", nil, RefusePeer, ""},
		{"192.168/16", "192.168.1.20:5000", "192.168.1.10", nil, "", ""},
		{"mapped lan peer", "[::ffff:192.168.1.20]:5000", "nas", nil, "", ""},
		{"ula", "[fd00::5]:5000", "nas", nil, "", ""},
		{"container gateway", "172.17.0.1:5000", "localhost", nil, "", ""},
		{"docker desktop gateway", "192.168.65.1:5000", "localhost", nil, "", ""},
		// Everything else is not local.
		{"public v4", "203.0.113.9:5000", "192.168.1.10", nil, RefusePeer, "192.168.1.10:1919"},
		{"public v6", "[2001:db8::9]:5000", "nas", nil, RefusePeer, ""},
		{"cgnat just below tailscale", "100.63.255.255:5000", "nas", nil, RefusePeer, "100.100.100.1:1919"},
		{"cgnat just above tailscale", "100.128.0.1:5000", "nas", nil, RefusePeer, "192.168.1.10:1919"},
		{"unparsable peer", "pipe", "localhost", nil, RefusePeer, ""},
		// Proxies and tunnels fail closed, whoever the peer is.
		{"trusted proxy", "192.0.2.20:5000", "127.0.0.1", nil, RefuseForwarded, ""},
		{"cloudflared on loopback", "127.0.0.1:5000", "127.0.0.1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, RefuseForwarded, ""},
		{"lan peer with a forwarding header", "192.168.1.20:5000", "nas", map[string]string{"X-Forwarded-For": "203.0.113.9"}, RefuseForwarded, ""},
		{"access jwt", "127.0.0.1:5000", "127.0.0.1", map[string]string{"Cf-Access-Jwt-Assertion": "x"}, RefuseForwarded, ""},
		{"forwarded", "127.0.0.1:5000", "localhost", map[string]string{"Forwarded": "for=203.0.113.9"}, RefuseForwarded, ""},
		{"xff", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": "203.0.113.9"}, RefuseForwarded, ""},
		{"x-real-ip", "127.0.0.1:5000", "localhost", map[string]string{"X-Real-IP": "203.0.113.9"}, RefuseForwarded, ""},
		{"xfh", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-Host": "rss.example.com"}, RefuseForwarded, ""},
		{"empty header still counts", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": ""}, RefuseForwarded, ""},
		// Tailscale Serve: the exact shape tailscaled sends, from loopback.
		{"tailscale serve", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "box.tail1234.ts.net"}, "", ""},
		{"tailscale serve over plain http", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "fd7a:115c:a1e0::5", "X-Forwarded-Host": "box.tail1234.ts.net"}, "", ""},
		// #128: a Host ending in .ts.net is the client's choice. A proxy on this
		// machine that passes Host through (nginx proxy_set_header Host $host,
		// Caddy) says where the request really came from, and that is not the tailnet.
		{"ts.net host through nginx", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http"}, RefuseForwarded, ""},
		{"ts.net host through nginx over https", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}, RefuseForwarded, ""},
		{"ts.net host, a tailnet xff appended to", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103, 203.0.113.9"}, RefuseForwarded, ""},
		{"ts.net host, xfh names another host", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Host": "rss.example.com"}, RefuseForwarded, ""},
		{"ts.net host, xfp http", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "http"}, RefuseForwarded, ""},
		{"ts.net host, xfh alone", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-Host": "anything.ts.net"}, RefuseForwarded, ""},
		{"ts.net host, unparsable xff", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "unknown"}, RefuseForwarded, ""},
		{"tailscale funnel", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "Tailscale-Funnel-Request": "?1"}, RefuseForwarded, ""},
		{"funnel header alone", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"Tailscale-Funnel-Request": "?1"}, RefuseForwarded, ""},
		{"ts.net serve still refuses cloudflare", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, RefuseForwarded, ""},
		{"ts.net from the lan is not serve", "192.168.1.20:5000", "box.tail1234.ts.net", map[string]string{"X-Forwarded-For": "1.2.3.4"}, RefuseForwarded, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := arrivedOn(httptest.NewRequest("POST", "/api/auth/open", nil), c.local)
			r.Host = c.host
			r.RemoteAddr = c.peer
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			require.Equal(t, c.want, g.OpenRefusal(r, c.host, true))
		})
	}

	// A peer in Tailscale's ranges is a tailnet device only on this machine's own
	// Tailscale address, or when it reached a private address (a LAN or a
	// container's bridge). Carrier-grade NAT, cloud and Kubernetes overlays share
	// the range (#175): a CGNAT, public, link-local or unknown local address refuses.
	peers := map[string]string{"v4": "100.101.102.103:5000", "v4 low edge": "100.64.0.0:5000", "v4 high edge": "100.127.255.255:5000",
		"v6": "[fd7a:115c:a1e0::1]:5000", "mapped v4": "[::ffff:100.101.102.103]:5000"}
	locals := []struct{ name, addr, want string }{
		{"own tailscale v4", "100.100.100.1:1919", ""},
		{"own tailscale v6", "[fd7a:115c:a1e0::100]:1919", ""},
		{"own tailscale v4 mapped", "[::ffff:100.100.100.1]:1919", ""},
		{"lan", "192.168.1.10:1919", ""},
		{"mapped lan", "[::ffff:192.168.1.10]:1919", ""},
		{"10/8", "10.1.2.3:1919", ""},
		{"172.16.0.0", "172.16.0.0:1919", ""},
		{"172.31.255.255", "172.31.255.255:1919", ""},
		{"container bridge", "172.17.0.2:1919", ""},
		{"ula", "[fd00::10]:1919", ""},
		{"172.15.255.255", "172.15.255.255:1919", RefusePeer},
		{"172.32.0.0", "172.32.0.0:1919", RefusePeer},
		{"another cgnat address", "100.127.0.7:1919", RefusePeer},
		{"public v4", "203.0.113.5:1919", RefusePeer},
		{"public v6", "[2001:db8::10]:1919", RefusePeer},
		{"link-local v4", "169.254.1.5:1919", RefusePeer},
		{"link-local v6", "[fe80::1%eth0]:1919", RefusePeer},
		{"unknown", "", RefusePeer},
	}
	for pn, peer := range peers {
		for _, l := range locals {
			t.Run("tailscale "+pn+" reaching "+l.name, func(t *testing.T) {
				r := arrivedOn(httptest.NewRequest("POST", "/", nil), l.addr)
				r.RemoteAddr = peer
				require.Equal(t, l.want, g.OpenRefusal(r, "nas", true))
			})
		}
	}
	// Without a local Tailscale address (or with the scan empty), only a private address of this machine admits the range.
	r := arrivedOn(httptest.NewRequest("POST", "/", nil), "100.100.100.1:1919")
	r.RemoteAddr = "100.101.102.103:1"
	require.Equal(t, RefusePeer, Gate{}.OpenRefusal(r, "nas", true), "arrived on a CGNAT address that is not this machine's tailnet one")
	require.Equal(t, RefusePeer, Gate{Tailnet: func() []netip.Addr { return nil }}.OpenRefusal(r, "nas", true))
	r = arrivedOn(r, "192.168.1.10:1919")
	require.Equal(t, "", Gate{}.OpenRefusal(r, "nas", true))

	// The Serve shape on a machine without any Tailscale address is not Serve.
	serve := httptest.NewRequest("POST", "/", nil)
	serve.Host, serve.RemoteAddr = "box.tail1234.ts.net", "127.0.0.1:1"
	serve.Header.Set("X-Forwarded-For", "100.101.102.103")
	require.Equal(t, RefuseForwarded, Gate{}.OpenRefusal(serve, "box.tail1234.ts.net", true))
	require.Equal(t, RefuseForwarded, Gate{Tailnet: func() []netip.Addr { return nil }}.OpenRefusal(serve, "box.tail1234.ts.net", true))
	require.Equal(t, "", g.OpenRefusal(serve, "box.tail1234.ts.net", true))
	serve.Header.Add("X-Forwarded-For", "100.101.102.104")
	require.Equal(t, RefuseForwarded, g.OpenRefusal(serve, "box.tail1234.ts.net", true), "two X-Forwarded-For lines")

	r = httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	require.Equal(t, RefuseHost, g.OpenRefusal(r, "evil.example", false), "the Host gate comes first")
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
		return g.SignInRefusal(r, h, ok && HostAllowed(h, "", nil))
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

// The tailnet answer is scanned once, then refreshed in the background after
// the recheck window, so Tailscale coming up after Kipple is noticed without a
// restart and no request waits on the scan.
func TestTailnetCacheRefreshesInTheBackground(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	up, scans := false, 0
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	scan := func() bool { mu.Lock(); defer mu.Unlock(); scans++; return up }
	c := newTailnetCache(clock, func() []netip.Addr {
		if scan() {
			return []netip.Addr{tsLocal4}
		}
		return nil
	})
	up4 := func() bool { return len(c.get()) > 0 }
	require.False(t, up4())
	require.False(t, up4())
	mu.Lock()
	require.Equal(t, 1, scans, "cached")
	up = true // tailscaled comes up later
	now = now.Add(tailnetRecheck)
	mu.Unlock()
	require.False(t, up4(), "the slightly stale answer is served while the refresh runs")
	require.Eventually(t, up4, 5*time.Second, 10*time.Millisecond)

	// Long quiet: an old answer is never used, the scan happens first.
	mu.Lock()
	up = false // Tailscale went away
	now = now.Add(time.Hour)
	mu.Unlock()
	require.False(t, up4(), "an hour-old true does not admit anyone")
	require.NotPanics(t, func() { _ = TailnetCheck()() })
}

// The Tailscale addresses are listed as the interfaces report them, never
// panicking on a machine without any.
func TestLocalTailnetAddrs(t *testing.T) {
	for _, a := range LocalTailnetAddrs() {
		require.True(t, tailscaleV4.Contains(a) || tailscaleV6.Contains(a), a)
	}
}
