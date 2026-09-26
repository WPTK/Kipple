package fetch

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBlockedDeprecatedRanges covers the ranges the first blocklist missed:
// IPv6 site-local, IPv4-compatible IPv6 (which Unmap leaves alone, so
// ::127.0.0.1 would otherwise pass) and the 6to4 relay anycast block.
func TestBlockedDeprecatedRanges(t *testing.T) {
	for _, ip := range []string{
		"fec0::1", "feff:ffff::1", // site-local fec0::/10
		"::127.0.0.1", "::10.0.0.1", "::8.8.8.8", "::1.2.3.4", // IPv4-compatible ::/96
		"192.88.99.1", "192.88.99.255", // 6to4 relay anycast
	} {
		require.True(t, Blocked(netip.MustParseAddr(ip)), ip)
	}
	for _, ip := range []string{"192.88.98.1", "192.88.100.1", "fe00::1", "2001:db9::1", "1::1"} {
		require.False(t, Blocked(netip.MustParseAddr(ip)), ip)
	}
}
