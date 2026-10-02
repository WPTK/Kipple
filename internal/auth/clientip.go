package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParseProxies parses KIPPLE_TRUSTED_PROXY_IPS: comma-separated single
// addresses and CIDR ranges. An address is the range of one. IPv4-mapped IPv6
// forms are unmapped, like the peer address they are compared with.
func ParseProxies(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var p netip.Prefix
		if strings.Contains(part, "/") {
			var err error
			if p, err = netip.ParsePrefix(part); err != nil {
				return nil, fmt.Errorf("invalid address range %q: %w", part, err)
			}
		} else {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("invalid IP %q: %w", part, err)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if a := p.Addr(); a.Is4In6() {
			if p.Bits() < 96 {
				return nil, fmt.Errorf("invalid address range %q: an IPv4-mapped range needs at least 96 bits", part)
			}
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Peer is the TCP peer address of r, unmapped and without a zone.
func Peer(r *http.Request) (netip.Addr, bool) {
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

func contains(trusted []netip.Prefix, a netip.Addr) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// PeerTrusted reports whether the TCP peer of r is one of the trusted proxies.
func PeerTrusted(r *http.Request, trusted []netip.Prefix) bool {
	peer, ok := Peer(r)
	return ok && contains(trusted, peer)
}

// ClientIP is the one place a request's client address is decided. A peer that
// is not a trusted proxy is the client, and every forwarding header it sends is
// ignored (it could write anything). A trusted proxy's client is the rightmost
// X-Forwarded-For hop that is not itself a trusted proxy: each proxy appends the
// address it received from, so everything left of that hop was written by
// someone the chain does not vouch for (the leftmost entry is what a client
// spoofs). With no X-Forwarded-For, CF-Connecting-IP (Cloudflare Tunnel) is the
// client; with neither, or with a header that does not parse, it is the peer.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, ok := Peer(r)
	if !ok {
		return r.RemoteAddr
	}
	if !contains(trusted, peer) {
		return peer.String()
	}
	if vals := r.Header.Values("X-Forwarded-For"); len(vals) > 0 {
		hops := strings.Split(strings.Join(vals, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, ok := parseHop(hops[i])
			if !ok {
				break // nothing further left can be believed either
			}
			if i == 0 || !contains(trusted, a) {
				return a.String()
			}
		}
		return peer.String()
	}
	if a, ok := parseHop(r.Header.Get("CF-Connecting-IP")); ok {
		return a.String()
	}
	return peer.String()
}

// parseHop reads one forwarded address, with or without a port.
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	a, err := netip.ParseAddr(s)
	if err != nil {
		ap, perr := netip.ParseAddrPort(s)
		if perr != nil {
			return netip.Addr{}, false
		}
		a = ap.Addr()
	}
	return a.WithZone("").Unmap(), true
}
