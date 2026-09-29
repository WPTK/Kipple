package setup

import (
	"bufio"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Reasons the open gate refuses a request (answered as
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

// Gate is the fixed part of the open gate, worked out once at start.
type Gate struct {
	// Trusted are the configured proxies: a request from one is forwarded by
	// definition.
	Trusted []netip.Addr
	// Gateway is the default gateway of the container this process runs in when
	// its default route is a veth (a bridge network), else the zero Addr. Docker
	// delivers every -p 127.0.0.1:... connection from it, but on Docker Desktop,
	// rootless setups and IPv6 without ip6tables it also delivers other hosts'
	// connections, so it only counts as local together with a loopback Host (see
	// OpenRefusal); in host networking it is the router and never special.
	Gateway netip.Addr
	// Tailnet reports that this machine has a Tailscale address. Only then does a
	// peer in 100.64.0.0/10 count as a tailnet device: the range is shared
	// carrier-grade NAT space elsewhere (other ISP subscribers, cloud networks).
	Tailnet bool
}

// OpenRefusal is the network part of the open gate (design 5.4), checked on
// every request of an open-mode account: "" when r may use open mode, else the
// reason. host is the normalized Host (NormalizeHost) and hostOK whether it
// passed the Host gate; openLAN is the security.open_lan setting.
//
// It fences accidents and well-behaved proxies, not a deliberate attacker who
// can reach the port through something that forwards without saying so (a bare
// nginx proxy_pass, socat): open mode's notice requires that nothing but this
// computer and the tailnet can reach Kipple at all.
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
	case peer.IsLoopback():
		return ""
	case tailscaleV4.Contains(peer) || tailscaleV6.Contains(peer):
		if g.Tailnet || openLAN {
			return "" // without a local tailnet address, only the owner's LAN opt-in admits the range
		}
		return RefusePeer
	case g.Gateway.IsValid() && peer == g.Gateway && loopbackHost(host):
		return "" // a browser on this computer through Docker's port forward
	case openLAN && peer.IsPrivate(): // RFC 1918 and ULA (the gateway with any other Host too)
		return ""
	}
	return RefusePeer
}

// SignInRefusal is OpenRefusal plus the browser check for the requests that
// grant open-mode access (a session, the switch to open mode, a Reader API
// password): the Origin must be present and name the very host the request was
// sent to. A same-machine reverse proxy that rewrites Host to the upstream
// address (nginx's default proxy_pass) forwards a browser's public Origin,
// which then disagrees.
func (g Gate) SignInRefusal(r *http.Request, host string, hostOK, openLAN bool) string {
	if reason := g.OpenRefusal(r, host, hostOK, openLAN); reason != "" {
		return reason
	}
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return RefuseForwarded
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return RefuseForwarded
	}
	return ""
}

// loopbackHost reports a Host that names this computer: localhost, *.localhost
// or a loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
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
// in, or the zero Addr when it is not in a container (no /.dockerenv or
// /run/.containerenv), when the default route is not a veth (host networking,
// where the gateway is the LAN router; slirp-style rootless networking, where
// every connection arrives from the gateway), or when the tables cannot be read.
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
	iface, gw := parseRouteGateway(f)
	if !gw.IsValid() || !isVeth("/sys/class/net", iface) {
		return netip.Addr{}
	}
	return gw
}

// isVeth reports whether iface is one end of a veth pair: its iflink (the peer's
// index, in another namespace) differs from its own ifindex. A physical NIC or
// a tap device links to itself.
func isVeth(sysNet, iface string) bool {
	if iface == "" || strings.ContainsAny(iface, `/\`) || iface == "." || iface == ".." {
		return false
	}
	read := func(name string) (int, bool) {
		b, err := os.ReadFile(sysNet + "/" + iface + "/" + name) // #nosec G304 -- a kernel interface name under /sys/class/net
		if err != nil {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return n, err == nil
	}
	idx, ok1 := read("ifindex")
	link, ok2 := read("iflink")
	return ok1 && ok2 && idx != link
}

// parseRouteGateway finds the default route's interface and gateway in a
// /proc/net/route table (hex, little-endian IPv4).
func parseRouteGateway(r io.Reader) (string, netip.Addr) {
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
		return f[0], a
	}
	return "", netip.Addr{}
}

// LocalTailnet reports whether this machine has a Tailscale address: one in
// Tailscale's own IPv6 range, or a 100.64.0.0/10 address on an interface named
// like Tailscale's (tailscale0, "Tailscale" on Windows).
func LocalTailnet() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		named := strings.HasPrefix(strings.ToLower(ifc.Name), "tailscale")
		for _, a := range addrs {
			pfx, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := pfx.Addr().Unmap()
			if tailscaleV6.Contains(ip) || (named && tailscaleV4.Contains(ip)) {
				return true
			}
		}
	}
	return false
}
