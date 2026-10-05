package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1":             "127.0.0.1",
		"127.0.0.1:1919":        "127.0.0.1",
		"127.0.0.1.":            "127.0.0.1",
		"[::1]:1919":            "::1",
		"[::1]":                 "::1",
		"[::FFFF:127.0.0.1]":    "::ffff:127.0.0.1",
		"::1":                   "::1",
		"LOCALHOST:1919":        "localhost",
		"localhost.":            "localhost",
		"NAS":                   "nas",
		"nas.local.:1919":       "nas.local",
		"rss.example.com:443":   "rss.example.com",
		"xn--bcher-kva.example": "xn--bcher-kva.example",
		"my_host.lan":           "my_host.lan",
	} {
		got, ok := NormalizeHost(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{
		"", ":1919", "a..b", "a.b..", "exa mple.com", "%65vil.com", "bücher.example", "evil.com:port",
		"evil.com:123456", "[::1", "[::1]x", "[127.0.0.1]", "[fe80::1%25eth0]:80", "::%]", "fe80::1%eth0", "a/b", "a\\b", "evil.com:1919:1919",
		strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com", "user@evil.com",
	} {
		_, ok := NormalizeHost(in)
		require.False(t, ok, in)
	}
}

// The Host gate of setup mode and open mode accepts a Host header exactly when
// its name cannot be resolved from public DNS. A DNS-rebinding attack from the
// internet needs a public name the attacker controls, resolving to this
// computer: the browser then calls it same-origin, and only the Host header
// gives it away.
func TestHostAllowed(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		// IP literals: a rebinding attack always carries a name.
		{"127.0.0.1", true},
		{"127.0.0.1:1919", true},
		{"127.0.0.1.", true},
		{"192.168.1.20", true},
		{"10.0.0.5:1919", true},
		{"100.101.102.103", true}, // Tailscale or carrier-grade NAT
		{"169.254.10.20", true},
		{"[::1]:1919", true},
		{"[::1]", true},
		{"[fe80::1]:1919", true},
		{"[fd7a:115c:a1e0::5]", true},
		{"[FD00::AB]:80", true},
		{"[::ffff:192.168.1.20]", true},
		// Single-label names: LLMNR, NetBIOS, a hosts file or a search domain.
		{"localhost", true},
		{"LOCALHOST:1919", true},
		{"localhost.", true},
		{"nas", true},
		{"NAS:1919", true},
		{"nas.", true},
		{"my_box", true},
		{"xn--bcher-kva", true}, // a single-label name in its punycode form is still single-label
		// The private suffixes.
		{"app.localhost", true},
		{"app.localhost:5173", true},
		{"nas.local", true},
		{"NAS.LOCAL.:1919", true},
		{"box.lan", true},
		{"box.home.arpa", true},
		{"svc.internal", true},
		{"a.b.svc.internal", true},
		{"machine.tailnet-abcd.ts.net", true},
		{"Machine.Tailnet-ABCD.ts.net.", true},
		{"xn--bcher-kva.local", true}, // an IDN under a private suffix
		// Public names: refused.
		{"rss.example.com", false},
		{"rss.example.com:443", false},
		{"RSS.EXAMPLE.COM.", false},
		{"evil.example", false},
		{"evil.example:1919", false},
		{"127.0.0.1.nip.io", false},
		{"127-0-0-1.sslip.io", false},
		{"192.168.1.20.nip.io", false},
		{"localhost.evil.example", false},
		{"nas.local.evil.example", false},
		{"svc.internal.evil.example", false},
		{"box.home.arpa.evil.example", false},
		{"evil.ts.net.example", false},
		{"evilts.net", false},
		{"ts.net.evil.example", false},
		{"example.lan.com", false},
		{"evillocal", true}, // single label, whatever it spells
		{"nas.locals", false},
		{"box.lan2", false},
		{"home.arpa.com", false},
		// Punycode lookalikes of a private suffix are other, public names.
		{"nas.xn--lcal-6qa", false},
		{"nas.xn--lca-4na", false},
		{"nas.xn--ln-1ka", false},
		{"xn--nas-local-xyz.com", false},
		// Not IP literals, and not names a browser sends.
		{"1.2.3", false},
		{"127.1", false},
		{"0x7f.0.0.1", false},
	} {
		host, ok := NormalizeHost(tc.host)
		require.Equal(t, tc.want, ok && HostAllowed(host, nil), tc.host)
	}
	require.False(t, HostAllowed("", nil))
}

func TestHostAllowedExtraEntries(t *testing.T) {
	extra := []string{"rss.example.com", "*.example.org"}
	for in, want := range map[string]bool{
		"rss.example.com":      true,
		"RSS.example.com.:443": true,
		"www.rss.example.com":  false,
		"a.example.org":        true,
		"a.b.example.org":      true,
		"example.org":          false, // *.suffix is for names under it
		"badexample.org":       false,
		"rss.example.net":      false, // a public name nobody listed
		"box.lan":              true,  // private, listed or not
	} {
		host, ok := NormalizeHost(in)
		require.True(t, ok, in)
		require.Equal(t, want, HostAllowed(host, extra), in)
	}
	require.False(t, HostAllowed("", extra))
}

func TestCheckHostEntry(t *testing.T) {
	for in, want := range map[string]string{
		" RSS.Example.com. ":  "rss.example.com",
		"*.Example.org":       "*.example.org",
		"*.home.corp":         "*.home.corp",
		"*.a.b.example.co.uk": "*.a.b.example.co.uk",
		"192.0.2.10":          "192.0.2.10",
		"[2001:db8::1]":       "2001:db8::1",
		"nas":                 "nas",
	} {
		got, err := CheckHostEntry(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, in := range []string{"", "*", "*.", "https://rss.example.com", "rss.example.com:443", "a b", "*.*.example.com", "rss.example.com/path", "*.192.0.2.1x!",
		"*.com", "*.io", "*.co.uk", "*.github.io", "*.1.2.3.4", "*.10", "*.newgtld", "*.zz"} {
		_, err := CheckHostEntry(in)
		require.Error(t, err, in)
	}
	list, err := ParseAllowedHosts(" a.example.com, ,*.b.example.com ")
	require.NoError(t, err)
	require.Equal(t, []string{"a.example.com", "*.b.example.com"}, list)
	list, err = ParseAllowedHosts("")
	require.NoError(t, err)
	require.Nil(t, list)
}

func FuzzHostGate(f *testing.F) {
	for _, s := range []string{"127.0.0.1:1919", "[::1]:80", "evil.example.", "LOCALHOST", "xn--bcher-kva.example",
		"[fe80::1%25eth0]", "a..b", "nas", "*.example.com", "", ":", "[", "1.2.3.4.nip.io"} {
		f.Add(s, "*.example.com")
	}
	f.Fuzz(func(t *testing.T, h, entry string) {
		host, ok := NormalizeHost(h)
		if ok {
			require.NotEmpty(t, host)
			require.Equal(t, strings.ToLower(host), host)
			again, ok2 := NormalizeHost(host)
			if strings.Contains(host, ":") {
				again, ok2 = NormalizeHost("[" + host + "]")
			}
			require.True(t, ok2, "a normalized host normalizes again: %q", host)
			require.Equal(t, host, again)
		}
		e, err := CheckHostEntry(entry)
		var extra []string
		if err == nil {
			again, err2 := CheckHostEntry(e)
			require.NoError(t, err2, "a normalized entry is accepted again: %q", e)
			require.Equal(t, e, again)
			require.NotEqual(t, "*", e)
			extra = []string{e}
		}
		allowed := HostAllowed(host, extra)
		if !ok {
			require.False(t, HostAllowed("", extra))
			return
		}
		// Anything allowed is local by shape or listed.
		if allowed && strings.Contains(host, ".") && !strings.Contains(host, ":") {
			local := false
			for _, suf := range privateHostSuffixes {
				local = local || strings.HasSuffix(host, suf)
			}
			listed := err == nil && (host == e || (strings.HasPrefix(e, "*.") && strings.HasSuffix(host, e[1:])))
			_, isIP := isIPv4(host)
			require.True(t, local || listed || isIP, "allowed: %q (entry %q)", host, e)
		}
	})
}

func isIPv4(s string) (string, bool) {
	if strings.Count(s, ".") != 3 {
		return "", false
	}
	for _, p := range strings.Split(s, ".") {
		if p == "" || len(p) > 3 || strings.Trim(p, "0123456789") != "" {
			return "", false
		}
	}
	return s, true
}
