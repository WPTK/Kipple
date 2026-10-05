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

// DNS-rebinding shapes: a name the attacker controls resolves to 127.0.0.1, so
// the browser calls it same-origin; only the Host header gives it away.
func TestHostAllowedRefusesRebindingShapes(t *testing.T) {
	for _, in := range []string{
		"evil.example:1919", "evil.example.", "EVIL.EXAMPLE", "127.0.0.1.nip.io", "127-0-0-1.sslip.io",
		"localhost.evil.example", "nas.local.evil.example", "evil.ts.net.example", "1.2.3", "127.1",
		"0x7f.0.0.1", "rss.example.com", "evilts.net", "example.lan.com",
	} {
		host, ok := NormalizeHost(in)
		require.False(t, ok && HostAllowed(host, nil), in)
	}
	for _, in := range []string{
		"127.0.0.1:1919", "[::1]:1919", "192.168.1.20", "localhost:1919", "app.localhost", "nas", "nas:1919",
		"nas.local", "box.lan", "box.home.arpa", "svc.internal", "machine.tailnet-abcd.ts.net",
	} {
		host, ok := NormalizeHost(in)
		require.True(t, ok && HostAllowed(host, nil), in)
	}
}

// Open mode's list is narrower: a .local name (mDNS), a single-label name
// (LLMNR, NetBIOS) or a router's DHCP name (.lan, .internal, .home.arpa) can be
// answered by any device on the LAN, which could rebind it to this computer and
// reach an open-mode instance through the owner's browser. Listed names still
// pass, and open mode never allows a name setup mode refuses.
func TestOpenHostAllowedIsNarrower(t *testing.T) {
	for _, in := range []string{
		"nas", "nas:1919", "evil.local", "nas.local:1919", "box.lan", "box.home.arpa", "svc.internal",
		"evil.example", "127.0.0.1.nip.io", "localhost.evil.example", "evilts.net",
	} {
		host, ok := NormalizeHost(in)
		require.False(t, ok && OpenHostAllowed(host, nil), in)
	}
	for _, in := range []string{
		"127.0.0.1:1919", "[::1]:1919", "192.168.1.20", "localhost:1919", "LOCALHOST", "app.localhost",
		"machine.tailnet-abcd.ts.net",
	} {
		host, ok := NormalizeHost(in)
		require.True(t, ok && OpenHostAllowed(host, nil), in)
		require.True(t, HostAllowed(host, nil), "setup mode allows everything open mode does: %s", in)
	}
	extra := []string{"nas", "*.local", "rss.example.com"}
	for _, in := range []string{"nas", "box.local", "rss.example.com"} {
		require.True(t, OpenHostAllowed(in, extra), in)
	}
	require.False(t, OpenHostAllowed("box.lan", extra))
	require.False(t, OpenHostAllowed("", extra))
}

// The wizard lists the name open mode was chosen under exactly when setup mode
// answers it and open mode would not: a LAN name. Not an IP literal, localhost
// or a Tailscale name (open mode answers those), not a listed name, and not a
// public one (setup mode refuses it, so the wizard never sees it).
func TestOpenHostToRemember(t *testing.T) {
	extra := []string{"listed.local", "*.corp.example"}
	for _, tc := range []struct{ in, want string }{
		{"nas.local:1919", "nas.local"},
		{"NAS.LOCAL.", "nas.local"},
		{"nas", "nas"},
		{"NAS:1919", "nas"},
		{"box.lan", "box.lan"},
		{"box.home.arpa", "box.home.arpa"},
		{"svc.internal:8080", "svc.internal"},
		{"xn--bcher-kva.local", "xn--bcher-kva.local"},
		{"127.0.0.1:1919", ""},
		{"192.168.1.20", ""},
		{"[::1]:1919", ""},
		{"[fe80::1]", ""},
		{"[FD00::AB]:80", ""},
		{"localhost:1919", ""},
		{"LOCALHOST.", ""},
		{"app.localhost", ""},
		{"box.tail1234.ts.net", ""},
		{"listed.local", ""},
		{"a.corp.example", ""},
		{"rss.example.com", ""},
		{"nas.local.evil.example", ""},
		{"evil.ts.net.example", ""},
		{"nas.xn--lcal-6qa", ""}, // a punycode lookalike of .local is another, public name
	} {
		host, ok := NormalizeHost(tc.in)
		require.True(t, ok, tc.in)
		require.Equal(t, tc.want, OpenHostToRemember(host, extra), tc.in)
		if tc.want != "" {
			_, err := CheckHostEntry(tc.want)
			require.NoError(t, err, "a remembered name is a valid allowed_hosts entry: %s", tc.want)
			require.True(t, OpenHostAllowed(host, append(extra, tc.want)), "once listed, open mode answers %s", tc.in)
		}
	}
	require.Equal(t, "", OpenHostToRemember("", nil))
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
		if OpenHostAllowed(host, extra) {
			require.True(t, allowed, "open mode allows only what setup mode allows: %q", host)
		}
		if !ok {
			require.False(t, HostAllowed("", extra))
			return
		}
		// Anything allowed is local by shape or listed.
		if allowed && strings.Contains(host, ".") && !strings.Contains(host, ":") {
			local := false
			for _, suf := range defaultHostSuffixes {
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
