package fetch

import (
	"fmt"
	"net/netip"
	"syscall"
)

// BlockedError is returned by the dial guard for a forbidden destination.
type BlockedError struct{ IP netip.Addr }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("address %s is not allowed (private, loopback, link-local or reserved range)", e.IP)
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",      // "this" network
		"100.64.0.0/10",  // CGNAT
		"192.0.0.0/24",   // IETF protocol assignments
		"198.18.0.0/15",  // benchmarking
		"240.0.0.0/4",    // reserved + broadcast
		"64:ff9b::/96",   // NAT64
		"64:ff9b:1::/48", // local-use NAT64
		"2002::/16",      // 6to4
		"2001::/32",      // Teredo
		"100::/64",       // discard-only
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Blocked reports whether ip must not be dialled: loopback, private,
// link-local, multicast, unspecified, CGNAT, NAT64, 6to4, Teredo, unique-local
// and the other reserved ranges.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ssrfControl returns a net.Dialer.Control that runs after DNS resolution on the
// concrete address about to be connected, so DNS rebinding cannot bypass it.
func ssrfControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	if allowPrivate {
		return nil
	}
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("ssrf guard: unparseable address %q: %w", address, err)
		}
		if Blocked(ap.Addr()) {
			return &BlockedError{IP: ap.Addr().Unmap()}
		}
		return nil
	}
}
