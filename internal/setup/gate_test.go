package setup

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenRefusal(t *testing.T) {
	gw := netip.MustParseAddr("172.17.0.1")
	g := Gate{Trusted: []netip.Addr{netip.MustParseAddr("192.0.2.20")}, Gateway: gw}
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
		{"container gateway", "172.17.0.1:5000", "127.0.0.1", nil, false, ""},
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
	// No gateway configured (not in a container): the would-be gateway is a LAN peer.
	r.RemoteAddr = "172.17.0.1:1"
	require.Equal(t, RefusePeer, Gate{}.OpenRefusal(r, "127.0.0.1", true, false))
}

func TestParseRouteGateway(t *testing.T) {
	table := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"eth0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	require.Equal(t, netip.MustParseAddr("172.17.0.1"), parseRouteGateway(strings.NewReader(table)))
	require.False(t, parseRouteGateway(strings.NewReader("Iface\tDestination\tGateway\n")).IsValid())
	require.False(t, parseRouteGateway(strings.NewReader("h\neth0\t00000000\tzz\n")).IsValid())
}
