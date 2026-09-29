package setup

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Reasons the open gate refuses a request (answered as
// {"error":"open_refused","reason":...}).
const (
	RefuseHost      = "host"      // the Host header is not an allowed name (DNS rebinding)
	RefuseForwarded = "forwarded" // a proxy or tunnel is in front
	RefusePeer      = "peer"      // the TCP peer is not this computer or the tailnet (nor the LAN, unless allowed)
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

	// Tailnet reports whether this machine has a Tailscale address (nil: never).
	// Only then does a peer in Tailscale's ranges count as a tailnet device:
	// 100.64.0.0/10 is shared carrier-grade NAT space elsewhere (other ISP
	// subscribers, cloud networks). TailnetCheck asks the system, cached.
	Tailnet func() bool
}

// OpenRefusal is the network part of the open gate (design 5.4), checked on
// every request of an open-mode account: "" when r may use open mode, else the
// reason. host is the normalized Host (NormalizeHost) and hostOK whether it
// passed the Host gate; openLAN is the security.open_lan setting.
//
// Only a loopback peer is this computer. In a container that is never the case:
// Docker delivers even a -p 127.0.0.1:... connection from the bridge gateway,
// and on Docker Desktop, rootless setups or IPv6 without ip6tables it delivers
// other hosts' connections from that same address, so nothing at this end can
// tell them apart; there the gateway is an ordinary LAN peer, admitted only with
// security.open_lan (and the published port's bind address is what keeps the
// LAN out).
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
		if (g.Tailnet != nil && g.Tailnet()) || openLAN {
			return "" // without a local tailnet address, only the owner's LAN opt-in admits the range
		}
		return RefusePeer
	case openLAN && peer.IsPrivate(): // RFC 1918 and ULA, a container's gateway included
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

// tailnetRecheck is how long TailnetCheck trusts its last answer: Tailscale may
// come up after Kipple (boot order) or be installed later.
const tailnetRecheck = 30 * time.Second

// TailnetCheck returns a Gate.Tailnet for this machine: LocalTailnet, asked
// once at the first call and then refreshed in the background at most every
// tailnetRecheck, so no request waits on the interface scan after the first.
func TailnetCheck() func() bool {
	return newTailnetCache(time.Now, LocalTailnet).get
}

type tailnetCache struct {
	now  func() time.Time
	scan func() bool

	mu   sync.Mutex
	at   time.Time
	last bool
	busy bool
}

func newTailnetCache(now func() time.Time, scan func() bool) *tailnetCache {
	return &tailnetCache{now: now, scan: scan}
}

func (c *tailnetCache) get() bool {
	c.mu.Lock()
	if c.at.IsZero() {
		c.mu.Unlock()
		v := c.scan()
		c.mu.Lock()
		if c.at.IsZero() {
			c.last, c.at = v, c.now()
		}
		v = c.last
		c.mu.Unlock()
		return v
	}
	if !c.busy && c.now().Sub(c.at) >= tailnetRecheck {
		c.busy = true
		go func() {
			v := c.scan()
			c.mu.Lock()
			c.last, c.at, c.busy = v, c.now(), false
			c.mu.Unlock()
		}()
	}
	v := c.last
	c.mu.Unlock()
	return v
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
