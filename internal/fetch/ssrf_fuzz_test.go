package fetch

import (
	"errors"
	"net/netip"
	"testing"
)

// ssrfOracle says whether the dial guard must refuse a, written from the address ranges themselves (byte tests,
// not netip's helpers and not the guard's own prefix table): "this" network, private, loopback, CGNAT (which holds
// the 100.100.100.200 metadata address), link-local (which holds 169.254.169.254), the IETF, 6to4 relay and
// benchmarking blocks, multicast and everything from 224.0.0.0 up; for IPv6 the IPv4-compatible block (which
// holds :: and ::1), unique-local (which holds the fd00:ec2::254 metadata address), link-local, site-local,
// multicast, NAT64, local-use NAT64, 6to4, Teredo and the discard prefix. An IPv4-mapped address is judged as
// the IPv4 address inside it.
func ssrfOracle(a netip.Addr) bool {
	b := a.As16()
	if a.Is4In6() {
		v := [4]byte(b[12:])
		return ssrfOracle4(v)
	}
	if a.Is4() {
		return ssrfOracle4(a.As4())
	}
	zero := func(from, to int) bool {
		for _, x := range b[from:to] {
			if x != 0 {
				return false
			}
		}
		return true
	}
	switch {
	case zero(0, 12): // ::/96
		return true
	case b[0]&0xfe == 0xfc, b[0] == 0xff: // fc00::/7, ff00::/8
		return true
	case b[0] == 0xfe && b[1] >= 0x80: // fe80::/10 and fec0::/10
		return true
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && zero(4, 12): // 64:ff9b::/96
		return true
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && b[4] == 0x00 && b[5] == 0x01: // 64:ff9b:1::/48
		return true
	case b[0] == 0x20 && b[1] == 0x02: // 2002::/16
		return true
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0 && b[3] == 0: // 2001::/32
		return true
	case b[0] == 0x01 && b[1] == 0x00 && zero(2, 8): // 100::/64
		return true
	}
	return false
}

func ssrfOracle4(b [4]byte) bool {
	switch {
	case b[0] == 0, b[0] == 10, b[0] == 127, b[0] >= 224:
		return true
	case b[0] == 100 && b[1]&0xc0 == 64: // 100.64.0.0/10
		return true
	case b[0] == 169 && b[1] == 254:
		return true
	case b[0] == 172 && b[1]&0xf0 == 16:
		return true
	case b[0] == 192 && b[1] == 168:
		return true
	case b[0] == 192 && b[1] == 0 && b[2] == 0:
		return true
	case b[0] == 192 && b[1] == 88 && b[2] == 99:
		return true
	case b[0] == 198 && b[1]&0xfe == 18:
		return true
	}
	return false
}

// FuzzSSRFControl: the dial guard, given the address string a dialer hands it, never panics and refuses exactly
// the destinations ssrfOracle names: a blocked one with a *BlockedError, an unparsable one with some error, a
// public one not at all. Any parsable IP literal is also tried in its IPv4-mapped spelling and with a port,
// which must not change the verdict.
func FuzzSSRFControl(f *testing.F) {
	for _, s := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "172.32.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.100.100.200",
		"100.128.0.1", "0.0.0.0", "8.8.8.8", "1.1.1.1", "255.255.255.255", "224.0.0.1", "198.18.0.1", "198.20.0.1",
		"::1", "::", "::ffff:127.0.0.1", "::ffff:8.8.8.8", "::ffff:169.254.169.254", "fd00:ec2::254", "fe80::1%eth0",
		"fe80::1", "fec0::1", "ff02::1", "64:ff9b::7f00:1", "2002:7f00:1::1", "2001::1", "2001:db8::1", "2606:4700::1111",
		"100::1", "::7f00:1", "01.2.3.4", "0x7f.0.0.1", "127.1", "[::1]", "[::1]:80", "127.0.0.1:80", "", "x", "1.2.3.4.5",
	} {
		f.Add(s, uint16(443))
	}
	guard := ssrfControl(false)
	f.Fuzz(func(t *testing.T, s string, port uint16) {
		check := func(address string, want bool, parsable bool) {
			t.Helper()
			err := guard("tcp", address, nil)
			var be *BlockedError
			switch {
			case !parsable:
				if err == nil {
					t.Fatalf("unparsable address %q was let through", address)
				}
			case want:
				if !errors.As(err, &be) {
					t.Fatalf("address %q must be refused as blocked, got %v", address, err)
				}
			case err != nil:
				t.Fatalf("public address %q was refused: %v", address, err)
			}
		}
		// The string as given, the way a dialer passes "host:port".
		if ap, err := netip.ParseAddrPort(s); err != nil {
			check(s, false, false)
		} else {
			check(s, ssrfOracle(ap.Addr()), true)
		}
		// A bare IP literal, wrapped the way a dialer would after resolving it.
		a, err := netip.ParseAddr(s)
		if err != nil {
			return
		}
		want := ssrfOracle(a)
		check(netip.AddrPortFrom(a, port).String(), want, true)
		if got := Blocked(a); got != want {
			t.Fatalf("Blocked(%s) = %v, want %v", a, got, want)
		}
		if a.Is4() {
			mapped := netip.AddrFrom16(a.As16())
			check(netip.AddrPortFrom(mapped, port).String(), want, true)
		}
	})
}
