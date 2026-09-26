package fetch

import (
	"net/netip"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// normHost lowercases a hostname and drops a trailing dot and IPv6 brackets.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	return strings.TrimSuffix(h, ".")
}

func isIPHost(h string) bool {
	_, err := netip.ParseAddr(h)
	return err == nil || strings.ContainsAny(h, ":%")
}

// SameOrSubdomain reports whether host is base or a subdomain of it: the rule
// net/http uses to keep Authorization on a redirect (shouldCopyHeaderOnRedirect).
// An IP literal matches only itself. Comparison ignores case and a trailing dot.
func SameOrSubdomain(base, host string) bool {
	base, host = normHost(base), normHost(host)
	if base == "" || host == "" {
		return false
	}
	if base == host {
		return true
	}
	if isIPHost(base) || isIPHost(host) {
		return false
	}
	return strings.HasSuffix(host, "."+base)
}

// FeedHostVariant reports whether host may use the per-feed network exceptions
// of a feed on feedHost (full-text extraction, image proxying): the same host, a
// subdomain of it, or its bare/www. twin (www.example.com and example.com). IP
// literals match only themselves.
func FeedHostVariant(feedHost, host string) bool {
	if SameOrSubdomain(feedHost, host) {
		return true
	}
	f, h := normHost(feedHost), normHost(host)
	if f == "" || isIPHost(f) || isIPHost(h) {
		return false
	}
	return strings.TrimPrefix(f, "www.") == strings.TrimPrefix(h, "www.")
}

// SameSite reports whether a move from host a to host b stays with the same
// site, so per-feed settings tied to the host (network exceptions, HTTP
// credentials) may follow it: the same host, the same registrable domain
// (example.com -> www.example.com, feeds.example.co.uk -> example.co.uk), or a
// single-label LAN name gaining or losing its domain (nas -> nas.lan). An IP
// literal matches only itself.
func SameSite(a, b string) bool {
	a, b = normHost(a), normHost(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if isIPHost(a) || isIPHost(b) {
		return false
	}
	// A bare LAN name and its qualified form (search domain).
	if !strings.Contains(a, ".") && strings.HasPrefix(b, a+".") {
		return true
	}
	if !strings.Contains(b, ".") && strings.HasPrefix(a, b+".") {
		return true
	}
	ra, erra := publicsuffix.EffectiveTLDPlusOne(a)
	rb, errb := publicsuffix.EffectiveTLDPlusOne(b)
	return erra == nil && errb == nil && ra == rb
}
