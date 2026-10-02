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

	// Tailnet lists this machine's Tailscale addresses (nil or empty: none).
	// A peer in Tailscale's ranges counts as a tailnet device only when its
	// connection arrived on one of them: 100.64.0.0/10 is shared carrier-grade
	// NAT space elsewhere (other ISP subscribers, cloud networks), and a packet
	// from such a source that reached this machine through its LAN interface did
	// not come over the tailnet. TailnetCheck asks the system, cached.
	Tailnet func() []netip.Addr
}

// localAddr is the address the request's connection arrived on (net/http
// records it for every served request), or the zero Addr when unknown.
func localAddr(r *http.Request) netip.Addr {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return netip.Addr{}
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().WithZone("").Unmap()
}

// tailnetAddrs is Tailnet's answer (none without one).
func (g Gate) tailnetAddrs() []netip.Addr {
	if g.Tailnet == nil {
		return nil
	}
	return g.Tailnet()
}

// arrivedOverTailnet reports whether r arrived on one of this machine's
// Tailscale addresses. An unknown local address never does (fail closed).
func (g Gate) arrivedOverTailnet(r *http.Request) bool {
	local := localAddr(r)
	if !local.IsValid() {
		return false
	}
	for _, a := range g.tailnetAddrs() {
		if a == local {
			return true
		}
	}
	return false
}

// tailscaleServe reports whether r has the exact shape of a tailnet-only
// Tailscale Serve request. tailscaled (an httputil.ReverseProxy on this
// machine) sends it from loopback for a *.ts.net name, passes the Host
// through, and sets X-Forwarded-Host to that same Host, X-Forwarded-Proto to
// "https" when it terminated TLS, and X-Forwarded-For to exactly one address,
// the tailnet peer's own (tailscale ipn/ipnlocal/serve.go,
// addProxyForwardedHeaders; the ReverseProxy drops any incoming X-Forwarded-*
// first). The Host alone is the client's choice, so it decides nothing: an
// ordinary proxy in front (nginx, Caddy) that passes the client's Host through
// sets X-Forwarded-For to the client's real address or appends it to a list,
// and fails here. This machine must also have a Tailscale address at all.
// Anything else fails closed: the gate then refuses the request as forwarded.
func (g Gate) tailscaleServe(r *http.Request, peer netip.Addr, host string) bool {
	if !peer.IsLoopback() || !strings.HasSuffix(host, ".ts.net") || len(r.Header.Values("Tailscale-Funnel-Request")) != 0 {
		return false
	}
	if len(g.tailnetAddrs()) == 0 {
		return false
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) != 1 {
		return false
	}
	src, err := netip.ParseAddr(strings.TrimSpace(xff[0]))
	if err != nil {
		return false
	}
	if src = src.WithZone("").Unmap(); !tailscaleV4.Contains(src) && !tailscaleV6.Contains(src) {
		return false
	}
	if v := r.Header.Values("X-Forwarded-Host"); len(v) > 1 || (len(v) == 1 && !strings.EqualFold(v[0], r.Host)) {
		return false
	}
	if v := r.Header.Values("X-Forwarded-Proto"); len(v) > 1 || (len(v) == 1 && v[0] != "https") {
		return false
	}
	return true
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
	tsServe := g.tailscaleServe(r, peer, host)
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
		// Arrived on this machine's own Tailscale address: a tailnet device. Any
		// other way in (the LAN interface, a container's bridge), only the owner's
		// LAN opt-in admits the range, and then only when the connection arrived on
		// a private-range address of this machine (a LAN or a container's bridge,
		// where open_lan already trusts every device). 100.64.0.0/10 is also shared
		// carrier-grade NAT, cloud and Kubernetes overlay space: a machine whose
		// own address is there, or that is reached on a public one, would
		// otherwise let strangers in. An unknown local address fails closed.
		if g.arrivedOverTailnet(r) || (openLAN && localAddr(r).IsPrivate()) {
			return ""
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

// TailnetCheck returns a Gate.Tailnet for this machine: LocalTailnetAddrs,
// asked once at the first call and then refreshed in the background at most
// every tailnetRecheck, so no request waits on the interface scan after the
// first.
func TailnetCheck() func() []netip.Addr {
	return newTailnetCache(time.Now, LocalTailnetAddrs).get
}

type tailnetCache struct {
	now  func() time.Time
	scan func() []netip.Addr

	mu   sync.Mutex
	at   time.Time
	last []netip.Addr
	busy bool
}

func newTailnetCache(now func() time.Time, scan func() []netip.Addr) *tailnetCache {
	return &tailnetCache{now: now, scan: scan}
}

// get answers from the cache while it is fresh (tailnetRecheck), from the cache
// while a background refresh runs when it is a little older (up to twice
// that), and otherwise only after a fresh scan: an answer from long ago never
// admits anyone. A caller that finds another's fresh scan still running gets
// none (fail closed) rather than an old list.
func (c *tailnetCache) get() []netip.Addr {
	c.mu.Lock()
	age := c.now().Sub(c.at)
	switch {
	case !c.at.IsZero() && age < tailnetRecheck:
		v := c.last
		c.mu.Unlock()
		return v
	case !c.at.IsZero() && age < 2*tailnetRecheck:
		if !c.busy {
			c.busy = true
			go c.refresh()
		}
		v := c.last
		c.mu.Unlock()
		return v
	case c.busy:
		c.mu.Unlock()
		return nil
	}
	c.busy = true
	c.mu.Unlock()
	return c.refresh()
}

func (c *tailnetCache) refresh() []netip.Addr {
	v := c.scan()
	c.mu.Lock()
	c.last, c.at, c.busy = v, c.now(), false
	c.mu.Unlock()
	return v
}

// LocalTailnetAddrs lists this machine's Tailscale addresses: those in
// Tailscale's own IPv6 range, and 100.64.0.0/10 addresses on an interface named
// like Tailscale's (tailscale0, "Tailscale" on Windows).
func LocalTailnetAddrs() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
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
			ip := pfx.Addr().WithZone("").Unmap()
			if tailscaleV6.Contains(ip) || (named && tailscaleV4.Contains(ip)) {
				out = append(out, ip)
			}
		}
	}
	return out
}
