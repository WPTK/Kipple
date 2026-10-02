package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProxies(t *testing.T) {
	got, err := ParseProxies(" 192.0.2.10, 10.0.0.0/8 ,, 2001:db8::/32, ::ffff:198.51.100.7, ::ffff:203.0.113.0/120, 10.1.2.3/8 ")
	require.NoError(t, err)
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	require.Equal(t, []string{
		"192.0.2.10/32", "10.0.0.0/8", "2001:db8::/32", "198.51.100.7/32", "203.0.113.0/24",
		"10.0.0.0/8", // host bits are dropped
	}, s)

	none, err := ParseProxies("")
	require.NoError(t, err)
	require.Empty(t, none)

	for _, bad := range []string{"nope", "192.0.2.0/33", "192.0.2.0/x", "::ffff:10.0.0.0/64", "10.0.0.0/8, nope"} {
		_, err := ParseProxies(bad)
		require.Error(t, err, bad)
	}
}

func TestClientIP(t *testing.T) {
	trusted, err := ParseProxies("192.0.2.20, 172.16.0.0/12, 2001:db8:f::/48")
	require.NoError(t, err)
	type h = map[string][]string
	for _, tc := range []struct {
		name string
		peer string
		hdr  h
		want string
	}{
		{"no proxies configured: header ignored", "198.51.100.9:1", h{"X-Forwarded-For": {"203.0.113.9"}, "Cf-Connecting-Ip": {"203.0.113.9"}}, "198.51.100.9"},
		{"untrusted peer cannot spoof X-Forwarded-For", "198.51.100.9:1", h{"X-Forwarded-For": {"203.0.113.66, 203.0.113.9"}}, "198.51.100.9"},
		{"untrusted peer cannot spoof CF-Connecting-IP", "198.51.100.9:1", h{"Cf-Connecting-Ip": {"203.0.113.9"}}, "198.51.100.9"},
		{"trusted peer, no header: the peer", "192.0.2.20:1", nil, "192.0.2.20"},
		{"trusted peer, CF-Connecting-IP", "192.0.2.20:1", h{"Cf-Connecting-Ip": {"203.0.113.9"}}, "203.0.113.9"},
		{"trusted peer, junk CF-Connecting-IP", "192.0.2.20:1", h{"Cf-Connecting-Ip": {"junk"}}, "192.0.2.20"},
		{"trusted peer, one X-Forwarded-For hop", "192.0.2.20:1", h{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9"},
		{"rightmost hop wins over a spoofed leftmost", "192.0.2.20:1", h{"X-Forwarded-For": {"198.51.100.1, 203.0.113.9"}}, "203.0.113.9"},
		{"trusted hops on the right are skipped", "192.0.2.20:1", h{"X-Forwarded-For": {"198.51.100.1, 203.0.113.9, 172.16.5.5, 192.0.2.20"}}, "203.0.113.9"},
		{"every hop trusted: the leftmost", "192.0.2.20:1", h{"X-Forwarded-For": {"172.16.0.9, 172.16.5.5"}}, "172.16.0.9"},
		{"several header lines are one list", "192.0.2.20:1", h{"X-Forwarded-For": {"198.51.100.1", "203.0.113.9"}}, "203.0.113.9"},
		{"X-Forwarded-For beats CF-Connecting-IP", "192.0.2.20:1", h{"X-Forwarded-For": {"203.0.113.9"}, "Cf-Connecting-Ip": {"198.51.100.1"}}, "203.0.113.9"},
		{"junk rightmost hop: the peer, nothing to its left is believed", "192.0.2.20:1", h{"X-Forwarded-For": {"198.51.100.1, junk"}}, "192.0.2.20"},
		{"empty X-Forwarded-For falls back to CF-Connecting-IP", "192.0.2.20:1", h{"X-Forwarded-For": {""}, "Cf-Connecting-Ip": {"203.0.113.9"}}, "203.0.113.9"},
		{"blank X-Forwarded-For falls back to the peer", "192.0.2.20:1", h{"X-Forwarded-For": {" , "}}, "192.0.2.20"},
		{"trailing empty hop is ignored", "192.0.2.20:1", h{"X-Forwarded-For": {"198.51.100.1, 203.0.113.9, "}}, "203.0.113.9"},
		{"trailing comma only", "192.0.2.20:1", h{"X-Forwarded-For": {"203.0.113.9,"}}, "203.0.113.9"},
		{"bracketed IPv6 hop without a port", "192.0.2.20:1", h{"X-Forwarded-For": {"[2001:db8:1::9]"}}, "2001:db8:1::9"},
		{"bracketed IPv6 hop with a port", "192.0.2.20:1", h{"X-Forwarded-For": {"[2001:db8:1::9]:443"}}, "2001:db8:1::9"},
		{"hop with a port", "192.0.2.20:1", h{"X-Forwarded-For": {"203.0.113.9:5555"}}, "203.0.113.9"},
		{"CIDR-trusted peer", "172.17.0.1:1", h{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9"},
		{"IPv4-mapped peer is unmapped", "[::ffff:192.0.2.20]:1", h{"X-Forwarded-For": {"203.0.113.9"}}, "203.0.113.9"},
		{"IPv6 CIDR-trusted peer", "[2001:db8:f::1]:1", h{"X-Forwarded-For": {"2001:db8:1::9"}}, "2001:db8:1::9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			for k, v := range tc.hdr {
				r.Header[http.CanonicalHeaderKey(k)] = v
			}
			require.Equal(t, tc.want, ClientIP(r, trusted))
			if tc.name == "no proxies configured: header ignored" {
				require.Equal(t, tc.want, ClientIP(r, nil))
			}
		})
	}
}

func TestPeerTrusted(t *testing.T) {
	trusted, err := ParseProxies("10.0.0.0/8")
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.9.8.7:1"
	require.True(t, PeerTrusted(r, trusted))
	r.RemoteAddr = "11.9.8.7:1"
	require.False(t, PeerTrusted(r, trusted))
	r.RemoteAddr = "garbage"
	require.False(t, PeerTrusted(r, trusted))
}
