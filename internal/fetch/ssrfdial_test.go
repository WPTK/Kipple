package fetch

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fakedns"
)

var errStop = errors.New("test: stop before connecting")

// dialProbe dials addr through a net.Dialer whose Control is the real SSRF guard
// followed by a stop, so an address the guard lets through is recorded and the
// connection is never made. It returns the addresses the guard allowed.
func dialProbe(t *testing.T, addr string) (allowed []netip.Addr, err error) {
	t.Helper()
	var mu sync.Mutex
	guard := ssrfControl(false)
	d := &net.Dialer{Control: func(network, address string, c syscall.RawConn) error {
		if gerr := guard(network, address, c); gerr != nil {
			return gerr
		}
		ap, perr := netip.ParseAddrPort(address)
		require.NoError(t, perr)
		mu.Lock()
		allowed = append(allowed, ap.Addr().Unmap())
		mu.Unlock()
		return errStop
	}}
	_, err = d.Dial("tcp", addr)
	require.Error(t, err, "the probe never connects")
	return allowed, err
}

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// The guard runs on every address the resolver returned, at dial time, so a name
// with a mix of public and private answers never reaches the private ones, and
// a name that resolves to private addresses only is refused outright.
func TestGuardFiltersEveryResolvedAddress(t *testing.T) {
	zone := map[string][]netip.Addr{
		"all-private.test":   addrs("127.0.0.1", "::1", "10.0.0.5", "192.168.1.9"),
		"metadata.test":      addrs("169.254.169.254"),
		"cgnat.test":         addrs("100.64.0.7", "100.127.255.254"),
		"mapped.test":        addrs("::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254"),
		"embedded.test":      addrs("64:ff9b::7f00:1", "2002:7f00:1::1", "::127.0.0.1"),
		"private-first.test": addrs("10.0.0.5", "93.184.216.34"),
		"public-first.test":  addrs("93.184.216.34", "10.0.0.5"),
		"mixed-v4-v6.test":   addrs("fd00::1", "2606:4700:4700::1111", "192.168.1.9", "1.1.1.1"),
	}
	fakedns.Install(t, func(name string) []netip.Addr { return zone[name] })

	for _, name := range []string{"all-private.test", "metadata.test", "cgnat.test", "mapped.test", "embedded.test"} {
		allowed, err := dialProbe(t, name+":80")
		require.Empty(t, allowed, name)
		var be *BlockedError
		require.ErrorAs(t, err, &be, name)
	}
	for name, want := range map[string][]netip.Addr{
		"private-first.test": addrs("93.184.216.34"),
		"public-first.test":  addrs("93.184.216.34"),
		"mixed-v4-v6.test":   addrs("2606:4700:4700::1111", "1.1.1.1"),
	} {
		allowed, _ := dialProbe(t, name+":80")
		require.ElementsMatch(t, want, allowed, name+": only the public answers are ever dialled")
	}
}

// The decision is made on the address of each dial, not on a lookup made when the
// URL was accepted: a name that answered with a public address once and then
// answers with a private one (DNS rebinding) is dialled the first time and
// refused the second.
func TestGuardFollowsTheAddressOfEachDial(t *testing.T) {
	var mu sync.Mutex
	rebound := false
	fakedns.Install(t, func(name string) []netip.Addr {
		if name != "rebind.test" {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if rebound {
			return addrs("169.254.169.254")
		}
		return addrs("93.184.216.34")
	})

	allowed, _ := dialProbe(t, "rebind.test:80")
	require.Equal(t, addrs("93.184.216.34"), allowed)

	mu.Lock()
	rebound = true
	mu.Unlock()
	allowed, err := dialProbe(t, "rebind.test:80")
	require.Empty(t, allowed)
	var be *BlockedError
	require.ErrorAs(t, err, &be)
	require.Equal(t, netip.MustParseAddr("169.254.169.254"), be.IP)
}

// A literal address is guarded in every spelling the dialer accepts: it skips DNS,
// so the Control hook is the only check.
func TestGuardRefusesLiteralSpellings(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1:80", "[::1]:80", "[::ffff:127.0.0.1]:80", "[::ffff:7f00:1]:80", "[0:0:0:0:0:ffff:7f00:1]:80",
		"169.254.169.254:80", "[::ffff:169.254.169.254]:80", "[fe80::1%25lo]:80",
		"100.64.0.1:80", "10.0.0.1:80", "172.16.0.1:80", "172.31.255.255:80", "192.168.0.1:80",
		"0.0.0.0:80", "[::]:80", "224.0.0.1:80", "255.255.255.255:80", "[fd00::1]:80", "[fc00::1]:80",
		"[64:ff9b::a00:1]:80", "[2002:a00:1::]:80", "[2001:0:4136:e378:8000:63bf:3fff:fdd2]:80",
	} {
		allowed, err := dialProbe(t, addr)
		require.Empty(t, allowed, addr)
		var be *BlockedError
		require.ErrorAs(t, err, &be, addr)
	}
	// Ports and public neighbours of the private ranges are not blocked: the guard is about addresses.
	for _, addr := range []string{"8.8.8.8:8080", "172.15.255.255:80", "172.32.0.1:80", "100.63.255.255:80", "100.128.0.1:80", "[2606:4700:4700::1111]:6379"} {
		allowed, _ := dialProbe(t, addr)
		require.Len(t, allowed, 1, addr)
	}
}
