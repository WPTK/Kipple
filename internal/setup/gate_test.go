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
	g := Gate{Trusted: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}, Tailnet: tailnetUp}
	type tc struct {
		name    string
		peer    string
		host    string
		hdr     map[string]string
		openLAN bool
		want    string
		local   string // the address the connection arrived on ("": unknown)
	}
	for _, c := range []tc{
		{"loopback v4", "127.0.0.1:5000", "127.0.0.1", nil, false, "", ""},
		{"loopback v6", "[::1]:5000", "localhost", nil, false, "", ""},
		{"mapped loopback", "[::ffff:127.0.0.1]:5000", "localhost", nil, false, "", ""},
		{"tailscale v4", "100.101.102.103:5000", "nas", nil, false, "", "100.100.100.1:1919"},
		{"tailscale v6", "[fd7a:115c:a1e0::1]:5000", "nas", nil, false, "", "[fd7a:115c:a1e0::100]:1919"},
		{"tailscale v4 via a mapped listener", "[::ffff:100.101.102.103]:5000", "nas", nil, false, "", "[::ffff:100.100.100.1]:1919"},
		// A CGNAT or tailnet-range source that reached this machine on another
		// address (its LAN interface) did not come over the tailnet.
		{"tailscale range on the lan address", "100.101.102.103:5000", "nas", nil, false, RefusePeer, "192.168.1.10:1919"},
		{"tailscale v6 range on the lan address", "[fd7a:115c:a1e0::1]:5000", "nas", nil, false, RefusePeer, "[fd00::10]:1919"},
		{"tailscale range, local address unknown", "100.101.102.103:5000", "nas", nil, false, RefusePeer, ""},
		{"tailscale range on the lan address with open_lan", "100.101.102.103:5000", "nas", nil, true, "", "192.168.1.10:1919"},
		// In a container even this computer arrives from the bridge gateway, which
		// cannot be told from the LAN: it needs the LAN opt-in, whatever Host it names.
		{"container gateway naming localhost", "172.17.0.1:5000", "localhost", nil, false, RefusePeer, ""},
		{"container gateway with open_lan", "172.17.0.1:5000", "localhost", nil, true, "", ""},
		{"docker desktop gateway with open_lan", "192.168.65.1:5000", "localhost", nil, true, "", ""},
		{"another bridge peer", "172.17.0.5:5000", "127.0.0.1", nil, false, RefusePeer, ""},
		{"lan refused by default", "192.168.1.20:5000", "192.168.1.10", nil, false, RefusePeer, ""},
		{"lan with open_lan", "192.168.1.20:5000", "192.168.1.10", nil, true, "", ""},
		{"ula with open_lan", "[fd00::5]:5000", "nas", nil, true, "", ""},
		{"public peer even with open_lan", "203.0.113.9:5000", "192.168.1.10", nil, true, RefusePeer, ""},
		{"cgnat outside tailscale", "100.63.0.1:5000", "nas", nil, false, RefusePeer, ""},
		{"trusted proxy", "192.0.2.20:5000", "127.0.0.1", nil, false, RefuseForwarded, ""},
		{"cloudflared on loopback", "127.0.0.1:5000", "127.0.0.1", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, false, RefuseForwarded, ""},
		{"access jwt", "127.0.0.1:5000", "127.0.0.1", map[string]string{"Cf-Access-Jwt-Assertion": "x"}, false, RefuseForwarded, ""},
		{"forwarded", "127.0.0.1:5000", "localhost", map[string]string{"Forwarded": "for=203.0.113.9"}, false, RefuseForwarded, ""},
		{"xff", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": "203.0.113.9"}, false, RefuseForwarded, ""},
		{"x-real-ip", "127.0.0.1:5000", "localhost", map[string]string{"X-Real-IP": "203.0.113.9"}, false, RefuseForwarded, ""},
		{"xfh", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-Host": "rss.example.com"}, false, RefuseForwarded, ""},
		{"empty header still counts", "127.0.0.1:5000", "localhost", map[string]string{"X-Forwarded-For": ""}, false, RefuseForwarded, ""},
		{"tailscale serve", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "box.tail1234.ts.net"}, false, "", ""},
		{"tailscale serve over plain http", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "fd7a:115c:a1e0::5", "X-Forwarded-Host": "box.tail1234.ts.net"}, false, "", ""},
		// #128: a Host ending in .ts.net is the client's choice. A proxy on this
		// machine that passes Host through (nginx proxy_set_header Host $host,
		// Caddy) says where the request really came from, and that is not the tailnet.
		{"ts.net host through nginx", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "http"}, false, RefuseForwarded, ""},
		{"ts.net host through nginx over https", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "X-Forwarded-Proto": "https"}, false, RefuseForwarded, ""},
		{"ts.net host, a tailnet xff appended to", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103, 203.0.113.9"}, false, RefuseForwarded, ""},
		{"ts.net host, xfh names another host", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Host": "rss.example.com"}, false, RefuseForwarded, ""},
		{"ts.net host, xfp http", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "100.101.102.103", "X-Forwarded-Proto": "http"}, false, RefuseForwarded, ""},
		{"ts.net host, xfh alone", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-Host": "anything.ts.net"}, false, RefuseForwarded, ""},
		{"ts.net host, unparsable xff", "127.0.0.1:5000", "anything.ts.net",
			map[string]string{"X-Forwarded-For": "unknown"}, false, RefuseForwarded, ""},
		{"tailscale funnel", "127.0.0.1:5000", "box.tail1234.ts.net",
			map[string]string{"X-Forwarded-For": "203.0.113.9", "Tailscale-Funnel-Request": "?1"}, false, RefuseForwarded, ""},
		{"funnel header alone", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"Tailscale-Funnel-Request": "?1"}, false, RefuseForwarded, ""},
		{"ts.net serve still refuses cloudflare", "127.0.0.1:5000", "box.tail1234.ts.net", map[string]string{"CF-Connecting-IP": "203.0.113.9"}, false, RefuseForwarded, ""},
		{"ts.net from the lan is not serve", "192.168.1.20:5000", "box.tail1234.ts.net", map[string]string{"X-Forwarded-For": "1.2.3.4"}, true, RefuseForwarded, ""},
		{"unparsable peer", "pipe", "localhost", nil, false, RefusePeer, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := arrivedOn(httptest.NewRequest("POST", "/api/auth/open", nil), c.local)
			r.Host = c.host
			r.RemoteAddr = c.peer
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			require.Equal(t, c.want, g.OpenRefusal(r, c.host, true, c.openLAN))
		})
	}
	// The Serve shape on a machine without any Tailscale address is not Serve.
	serve := httptest.NewRequest("POST", "/", nil)
	serve.Host, serve.RemoteAddr = "box.tail1234.ts.net", "127.0.0.1:1"
	serve.Header.Set("X-Forwarded-For", "100.101.102.103")
	require.Equal(t, RefuseForwarded, Gate{}.OpenRefusal(serve, "box.tail1234.ts.net", true, false))
	require.Equal(t, RefuseForwarded, Gate{Tailnet: func() []netip.Addr { return nil }}.OpenRefusal(serve, "box.tail1234.ts.net", true, false))
	require.Equal(t, "", g.OpenRefusal(serve, "box.tail1234.ts.net", true, false))
	serve.Header.Add("X-Forwarded-For", "100.101.102.104")
	require.Equal(t, RefuseForwarded, g.OpenRefusal(serve, "box.tail1234.ts.net", true, false), "two X-Forwarded-For lines")

	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "127.0.0.1:1"
	require.Equal(t, RefuseHost, g.OpenRefusal(r, "evil.example", false, true), "the Host gate comes first")

	// Without a local tailnet address the CGNAT range is not the tailnet: only the LAN opt-in admits it.
	r = arrivedOn(r, "100.100.100.1:1919")
	r.RemoteAddr = "100.101.102.103:1"
	require.Equal(t, RefusePeer, Gate{}.OpenRefusal(r, "nas", true, false))
	require.Equal(t, "", Gate{}.OpenRefusal(r, "nas", true, true))
	require.Equal(t, RefusePeer, Gate{Tailnet: func() []netip.Addr { return nil }}.OpenRefusal(r, "nas", true, false))
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
