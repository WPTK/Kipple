package auth

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

// FuzzClientIP: ParseProxies never panics and returns masked prefixes; ClientIP never panics, answers the peer
// when the peer is not a trusted proxy, and otherwise answers the peer or a parsable, zone-free, unmapped
// address, never a token the sender made up.
func FuzzClientIP(f *testing.F) {
	for _, s := range []struct{ proxies, remote, xff, cf string }{
		{"192.0.2.20, 172.16.0.0/12, 2001:db8:f::/48", "192.0.2.20:1", "203.0.113.66, 203.0.113.9", ""},
		{"192.0.2.20", "198.51.100.9:1", "203.0.113.66", "203.0.113.7"},
		{"192.0.2.20", "192.0.2.20:1", "", "203.0.113.9"},
		{"192.0.2.20", "192.0.2.20:1", "junk, 203.0.113.9", ""},
		{"192.0.2.20", "192.0.2.20:1", "[2001:db8::1]:443, 192.0.2.20", ""},
		{"::ffff:192.0.2.0/120", "192.0.2.5:1", "fe80::1%eth0", ""},
		{"::ffff:0.0.0.0/64", "[::ffff:192.0.2.5]:1", "1.2.3.4, ", "x"},
		{"10.0.0.0/8, , 2001:db8::/32", "not an address", "1.2.3.4", ""},
		{"10.0.0.1/33", "10.0.0.1:1", "1.2.3.4", ""},
		{"0.0.0.0/0", "10.0.0.1:1", "01.2.3.4", "0x7f.1"},
	} {
		f.Add(s.proxies, s.remote, s.xff, s.cf)
	}
	f.Fuzz(func(t *testing.T, proxies, remote, xff, cf string) {
		trusted, err := ParseProxies(proxies)
		if err != nil {
			trusted = nil
		}
		for _, p := range trusted {
			if !p.IsValid() || p != p.Masked() || p.Addr().Is4In6() {
				t.Fatalf("ParseProxies(%q) returned %v, not a masked, unmapped prefix", proxies, p)
			}
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header["X-Forwarded-For"] = []string{xff}
		}
		if cf != "" {
			r.Header["Cf-Connecting-Ip"] = []string{cf}
		}
		got := ClientIP(r, trusted)
		peer, ok := Peer(r)
		if !ok {
			if got != remote {
				t.Fatalf("unparsable peer %q: got %q", remote, got)
			}
			return
		}
		if got == peer.String() {
			return
		}
		if !contains(trusted, peer) {
			t.Fatalf("untrusted peer %s: got %q, want the peer", peer, got)
		}
		a, err := netip.ParseAddr(got)
		if err != nil || a.Zone() != "" || a.Is4In6() || a.String() != got {
			t.Fatalf("client address %q (xff %q, cf %q) is not a plain IP", got, xff, cf)
		}
	})
}
