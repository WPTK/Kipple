package fetch

import (
	"strings"
	"testing"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/WPTK/kipple/internal/testseed"
)

// FuzzHostRules: the host-matching rules that decide where per-feed network exceptions and credentials may
// follow a feed (SameSite, SameOrSubdomain, FeedHostVariant). Properties: SameSite is symmetric, a usable host
// matches itself and an empty one matches nothing, an IP literal or a wildcard-DNS name (nip.io and the like)
// matches only itself whatever spelling it comes in, and a same-site answer for two different hosts has a
// reason (one registrable domain or a reserved local suffix), never just a shared tail.
func FuzzHostRules(f *testing.F) {
	for _, s := range [][2]string{
		{"example.com", "www.example.com"}, {"nas", "nas.lan"}, {"nas", "nas.attacker.example"},
		{"10-0-0-5.nip.io", "169-254-169-254.nip.io"}, {"nip.io", "x.nip.io"}, {"X.NIP.IO.", "x.nip.io"},
		{"127.0.0.1", "[127.0.0.1]"}, {"::1", "[::1]"}, {"a.example.co.uk", "b.example.co.uk"},
		{"co.uk", "example.co.uk"}, {"x.nip.io..", "y.nip.io"}, {"①.nip.io", "nip.io"},
		{"xn--nip-4ta.io", "nip.io"}, {"localtest.me", "app.localtest.me"}, {"", ""},
	} {
		f.Add(s[0], s[1])
	}
	for _, h := range testseed.Lines("../testseed/testdata/hosts.txt") {
		f.Add(h, "www."+h)
		f.Add(h, h+".nip.io")
		f.Add(h+".nip.io", h)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		na, nb := normHost(a), normHost(b)
		same := SameSite(a, b)

		if same != SameSite(b, a) {
			t.Fatalf("SameSite not symmetric: %q %q", a, b)
		}
		// FeedHostVariant is asymmetric only through SameOrSubdomain (a subdomain of the feed host).
		if FeedHostVariant(a, b) != FeedHostVariant(b, a) && !SameOrSubdomain(a, b) && !SameOrSubdomain(b, a) {
			t.Fatalf("FeedHostVariant asymmetric without a subdomain: %q %q", a, b)
		}
		if na != "" && !SameSite(a, a) {
			t.Fatalf("SameSite(%q, itself) is false", a)
		}
		if na == "" && (same || SameOrSubdomain(a, b) || FeedHostVariant(a, b)) {
			t.Fatalf("an empty host matched: %q %q", a, b)
		}

		if na != nb {
			for _, r := range []struct {
				rule string
				got  bool
			}{{"SameSite", same}, {"SameOrSubdomain", SameOrSubdomain(a, b)}, {"FeedHostVariant", FeedHostVariant(a, b)}} {
				if r.got && (isIPHost(na) || isIPHost(nb)) {
					t.Fatalf("%s: an IP literal matched another host: %q %q", r.rule, a, b)
				}
				if r.got && (wildcardDNS(na) || wildcardDNS(nb)) {
					t.Fatalf("%s: a wildcard-DNS name matched another host: %q %q", r.rule, a, b)
				}
			}
			// A spelling a resolver may fold to a wildcard-DNS name (full-width letters, an ideographic dot)
			// must not pass as an ordinary host.
			for _, h := range []string{a, b} {
				p, err := idna.Lookup.ToASCII(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), "."))
				if err == nil && wildcardDNS(p) && !wildcardDNS(normHost(h)) && same {
					t.Fatalf("SameSite let %q (IDNA %q, a wildcard-DNS name) match another host: %q", h, p, b)
				}
			}
			if same {
				ra, erra := publicsuffix.EffectiveTLDPlusOne(na)
				rb, errb := publicsuffix.EffectiveTLDPlusOne(nb)
				if !(erra == nil && errb == nil && ra == rb) && !bareQualified(na, nb) && !bareQualified(nb, na) {
					t.Fatalf("SameSite(%q, %q) has no reason", a, b)
				}
			}
			if SameOrSubdomain(a, b) && !strings.HasSuffix(nb, "."+na) {
				t.Fatalf("SameOrSubdomain(%q, %q) is not a subdomain", a, b)
			}
		}
	})
}
