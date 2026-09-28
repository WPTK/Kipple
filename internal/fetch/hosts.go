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
// An IP literal matches only itself, and so does a single-label name: a LAN
// host such as "news" or "app" is also a public TLD, and anyone can register
// evil.news and point it at a private address. Comparison ignores case and a
// trailing dot.
func SameOrSubdomain(base, host string) bool {
	base, host = normHost(base), normHost(host)
	if base == "" || host == "" {
		return false
	}
	if base == host {
		return true
	}
	if isIPHost(base) || isIPHost(host) || !strings.Contains(base, ".") {
		return false
	}
	return strings.HasSuffix(host, "."+base)
}

// FeedHostVariant reports whether host may use the per-feed network exceptions
// of a feed on feedHost (full-text extraction, image proxying): the same host, a
// subdomain of it, or its bare/www. twin (www.example.com and example.com). IP
// literals and single-label names match only themselves.
func FeedHostVariant(feedHost, host string) bool {
	if SameOrSubdomain(feedHost, host) {
		return true
	}
	f, h := normHost(feedHost), normHost(host)
	if f == "" || isIPHost(f) || isIPHost(h) || !strings.Contains(f, ".") {
		return false // no www. twin of a single-label name either (www.app is a public name)
	}
	return strings.TrimPrefix(f, "www.") == strings.TrimPrefix(h, "www.")
}

// SameSite reports whether a move from host a to host b stays with the same
// site, so per-feed settings tied to the host (network exceptions, HTTP
// credentials) may follow it: the same host, the same registrable domain
// (example.com -> www.example.com, feeds.example.co.uk -> example.co.uk), or a
// single-label LAN name gaining or losing one of the reserved local suffixes in
// localSuffixes (nas -> nas.lan, nas.home.arpa -> nas). An IP literal matches
// only itself.
//
// The bare-name rule is deliberately limited to those suffixes: "nas" ->
// "nas.attacker.example" is not the same site, because anyone can register a
// public name that starts with a LAN host's label and point it at a private
// address, and a same-site hop inherits the feed's network exceptions.
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
	if bareQualified(a, b) || bareQualified(b, a) {
		return true
	}
	ra, erra := publicsuffix.EffectiveTLDPlusOne(a)
	rb, errb := publicsuffix.EffectiveTLDPlusOne(b)
	return erra == nil && errb == nil && ra == rb
}

// localSuffixes are the search domains a single-label LAN name may gain or lose
// and stay the same site: names reserved for private use that no public
// registrant can hold (.local RFC 6762, .home.arpa RFC 8375, .internal ICANN
// 2024), and the common router and OS defaults that are not delegated TLDs
// (.lan; .localdomain; .home and .corp, which ICANN will not delegate).
// Vendor names under a real TLD (fritz.box) are left out: someone else could
// hold them.
var localSuffixes = []string{".lan", ".local", ".home.arpa", ".internal", ".localdomain", ".home", ".corp"}

// bareQualified reports whether bare is a single-label name and qualified is
// exactly bare plus one of localSuffixes (nas and nas.lan; not nas.x.lan).
func bareQualified(bare, qualified string) bool {
	if strings.Contains(bare, ".") {
		return false
	}
	for _, s := range localSuffixes {
		if qualified == bare+s {
			return true
		}
	}
	return false
}
