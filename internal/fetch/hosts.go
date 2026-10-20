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
// An IP literal matches only itself, and so do a single-label name and a
// wildcard-DNS name (wildcardDNSSuffixes). A single-label LAN host such as
// "news" or "app" is also a public TLD, and anyone can register evil.news and
// point it at a private address. Comparison ignores case and a
// trailing dot.
func SameOrSubdomain(base, host string) bool {
	base, host = normHost(base), normHost(host)
	if base == "" || host == "" {
		return false
	}
	if base == host {
		return true
	}
	if isIPHost(base) || isIPHost(host) || !strings.Contains(base, ".") || wildcardDNS(base) || wildcardDNS(host) {
		return false
	}
	return strings.HasSuffix(host, "."+base)
}

// FeedHostVariant reports whether host may use the per-feed network exceptions
// of a feed on feedHost (full-text extraction, image proxying): the same host, a
// subdomain of it, or its bare/www. twin (www.example.com and example.com). IP
// literals and wildcard-DNS names match only themselves. A single-label name
// matches only itself, in both directions: the public name www.app is not the
// twin of the LAN name app. Neither is a public suffix (www.co.uk and co.uk) or a
// name with an empty label.
func FeedHostVariant(feedHost, host string) bool {
	if SameOrSubdomain(feedHost, host) {
		return true
	}
	f, h := normHost(feedHost), normHost(host)
	// The twin relation is "the same name once one leading www. is dropped", stated on that trimmed name so it is
	// symmetric in f and h.
	name := strings.TrimPrefix(f, "www.")
	if !hasTwin(name) || isIPHost(f) || isIPHost(h) || wildcardDNS(f) || wildcardDNS(h) {
		return false
	}
	return name == strings.TrimPrefix(h, "www.")
}

// hasTwin reports whether a bare name can have a www. twin: it is a registrable or deeper name, not a single label
// (also a public suffix), not any other public suffix, and has no empty label.
func hasTwin(name string) bool {
	if strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return false
	}
	suffix, _ := publicsuffix.PublicSuffix(name)
	return suffix != name
}

// SameSite reports whether a move from host a to host b stays with the same
// site, so per-feed settings tied to the host (network exceptions, HTTP
// credentials) may follow it: the same host, the same registrable domain
// (example.com -> www.example.com, feeds.example.co.uk -> example.co.uk), or a
// single-label LAN name gaining or losing one of the reserved local suffixes in
// localSuffixes (nas -> nas.lan, nas.home.arpa -> nas). An IP literal or a
// wildcard-DNS name (wildcardDNSSuffixes) matches only itself.
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
	if isIPHost(a) || isIPHost(b) || wildcardDNS(a) || wildcardDNS(b) {
		return false
	}
	if bareQualified(a, b) || bareQualified(b, a) {
		return true
	}
	ra, erra := publicsuffix.EffectiveTLDPlusOne(a)
	rb, errb := publicsuffix.EffectiveTLDPlusOne(b)
	return erra == nil && errb == nil && ra == rb
}

// wildcardDNSSuffixes are public services whose names spell the address they resolve to
// (10-0-0-5.nip.io is 10.0.0.5, 169-254-169-254.nip.io is 169.254.169.254): two names under one of
// them are two unrelated hosts, not one site, so every host rule here matches such a name only
// exactly, like an IP literal.
var wildcardDNSSuffixes = []string{"nip.io", "sslip.io", "xip.io", "traefik.me", "localtest.me", "lvh.me", "local.gd"}

// wildcardDNS reports whether h (normalized) is a name under one of wildcardDNSSuffixes.
func wildcardDNS(h string) bool {
	for _, s := range wildcardDNSSuffixes {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// localSuffixes are the search domains a single-label LAN name may gain or lose
// and stay the same site: names reserved for private use that no public
// registrant can hold (.local RFC 6762, .home.arpa RFC 8375, .internal ICANN
// 2024), and the common router and OS defaults that are not delegated TLDs
// (.lan; .localdomain; .home and .corp, which ICANN will not delegate).
// Vendor names under a real TLD (fritz.box) are left out: someone else could
// hold them.
var localSuffixes = []string{".lan", ".local", ".home.arpa", ".internal", ".localdomain", ".home", ".corp"}

// LANOnlyName reports whether host is a name no public registrant can point at a private address on purpose: a
// name under one of localSuffixes (nas.lan, printer.home.arpa). A single-label name is left out (news and app are
// public TLDs), and so is any name under a real TLD, however it resolves today: public DNS can change what it
// points at, so only the owner may decide to let such a feed reach the network.
func LANOnlyName(host string) bool {
	h := normHost(host)
	if h == "" || isIPHost(h) || wildcardDNS(h) || strings.Contains(h, "..") {
		return false
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(h, s) && len(h) > len(s) {
			return true
		}
	}
	return false
}

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
