package setup

import (
	"bufio"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// Reasons the open gate refuses a request (POST /api/auth/open answers them as
// {"error":"open_refused","reason":...}).
const (
	RefuseHost      = "host"      // the Host header is not an allowed name (DNS rebinding)
	RefuseForwarded = "forwarded" // a proxy or tunnel is in front
	RefusePeer      = "peer"      // the TCP peer is not local (loopback, Tailscale, the container gateway)
)

// forwardHeaders mark a request that came through a proxy or tunnel. Any one of
// them fails the open gate (except the Tailscale Serve case below, which is
// allowed the X-Forwarded-* trio).
var forwardHeaders = []string{"CF-Connecting-IP", "Cf-Access-Jwt-Assertion", "Forwarded", "X-Real-IP",
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Tailscale-Funnel-Request"}

// tailscaleServeTolerated are the headers Tailscale Serve adds on a tailnet-only
// proxy (it is an httputil.ReverseProxy on the same machine).
var tailscaleServeTolerated = map[string]bool{"X-Forwarded-For": true, "X-Forwarded-Host": true, "X-Forwarded-Proto": true}

var (
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Gate is the fixed part of the open gate: the trusted proxies (a request from
// one is forwarded by definition) and the container's default gateway, through
// which Docker's userland proxy delivers every -p 127.0.0.1:... connection.
type Gate struct {
	Trusted []netip.Addr
	// Gateway is the container's default gateway; the zero Addr when not in a
	// container (then it is never special: on a bare host the gateway is the
	// router, and a router that masquerades a port forward looks exactly like it).
	Gateway netip.Addr
}

// OpenRefusal is the open gate (design 5.4): "" when r may use open mode, else
// the reason. host is the normalized Host (NormalizeHost) and hostOK whether it
// passed the Host gate; openLAN is the security.open_lan setting.
func (g Gate) OpenRefusal(r *http.Request, host string, hostOK, openLAN bool) string {
	if !hostOK {
		return RefuseHost
	}
	peer, ok := peerAddr(r)
	if !ok {
		return RefusePeer
	}
	for _, t := range g.Trusted {
		if t == peer {
			return RefuseForwarded
		}
	}
	// Tailscale Serve: tailscaled on this machine proxies tailnet-only HTTPS for a
	// *.ts.net name. Funnel (public) requests carry Tailscale-Funnel-Request.
	tsServe := peer.IsLoopback() && strings.HasSuffix(host, ".ts.net") && len(r.Header.Values("Tailscale-Funnel-Request")) == 0
	for _, h := range forwardHeaders {
		if len(r.Header.Values(h)) == 0 {
			continue
		}
		if tsServe && tailscaleServeTolerated[h] {
			continue
		}
		return RefuseForwarded
	}
	switch {
	case peer.IsLoopback(), tailscaleV4.Contains(peer), tailscaleV6.Contains(peer):
		return ""
	case g.Gateway.IsValid() && peer == g.Gateway:
		return ""
	case openLAN && peer.IsPrivate(): // RFC 1918 and ULA
		return ""
	}
	return RefusePeer
}

func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// ContainerGateway is the default gateway of the container this process runs
// in, read once from /proc/net/route, or the zero Addr when it is not in a
// container (no /.dockerenv or /run/.containerenv) or the table cannot be read.
func ContainerGateway() netip.Addr {
	inContainer := false
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			inContainer = true
		}
	}
	if !inContainer {
		return netip.Addr{}
	}
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return netip.Addr{}
	}
	defer f.Close()
	return parseRouteGateway(f)
}

// parseRouteGateway finds the default route's gateway in a /proc/net/route
// table (hex, little-endian IPv4).
func parseRouteGateway(r io.Reader) netip.Addr {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil || len(b) != 4 {
			continue
		}
		a := netip.AddrFrom4([4]byte{b[3], b[2], b[1], b[0]})
		if a.IsUnspecified() {
			continue
		}
		return a
	}
	return netip.Addr{}
}
