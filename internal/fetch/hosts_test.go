package fetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSameOrSubdomain(t *testing.T) {
	require.True(t, SameOrSubdomain("example.com", "EXAMPLE.com."))
	require.True(t, SameOrSubdomain("example.com", "www.example.com"))
	require.False(t, SameOrSubdomain("www.example.com", "example.com"))
	require.False(t, SameOrSubdomain("example.com", "notexample.com"))
	require.True(t, SameOrSubdomain("::1", "[::1]"))
	require.False(t, SameOrSubdomain("", ""))
	require.False(t, SameOrSubdomain("1.2.3.4", "x.1.2.3.4"))
	require.False(t, SameOrSubdomain("news", "evil.news"), "a single-label LAN name is also a public TLD")
	require.True(t, SameOrSubdomain("news", "NEWS."))
}

func TestFeedHostVariant(t *testing.T) {
	require.True(t, FeedHostVariant("example.com", "www.example.com"))
	require.True(t, FeedHostVariant("www.example.com", "example.com"), "bare twin of a www feed host")
	require.True(t, FeedHostVariant("blog.example.com", "img.blog.example.com"))
	require.False(t, FeedHostVariant("blog.example.com", "example.com"), "a parent other than the www twin")
	require.False(t, FeedHostVariant("blog.example.com", "other.example.com"))
	require.False(t, FeedHostVariant("192.168.1.5", "nas.lan"), "an IP and a name cannot be matched without DNS")
	require.False(t, FeedHostVariant("", "example.com"))
	require.False(t, FeedHostVariant("news", "evil.news"), "no subdomains of a single-label feed host")
	require.False(t, FeedHostVariant("app", "www.app"))
}

func TestSameSite(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"example.com", "www.example.com", true},
		{"www.example.com", "example.com", true},
		{"feeds.example.co.uk", "example.co.uk", true},
		{"nas", "nas.lan", true},
		{"nas.lan", "nas", true},
		{"nas", "nas2.lan", false},
		{"nas", "nas.home.arpa", true},
		{"nas", "nas.local", true},
		{"nas.internal", "nas", true},
		{"nas", "nas.localdomain", true},
		{"nas", "nas.home", true},
		{"nas", "nas.corp", true},
		{"nas", "nas.fritz.box", false},        // under a real TLD
		{"nas", "nas.attacker.example", false}, // bare-name prefix is not a site
		{"nas.attacker.example", "nas", false},
		{"nas", "nas.x.lan", false},
		{"nas", "nas.localhost.example", false},
		{"nas", "other", false},
		{"example.com", "example.org", false},
		{"a.github.io", "b.github.io", false}, // public suffix: different sites
		{"192.168.1.5", "192.168.1.5", true},
		{"192.168.1.5", "nas.lan", false},
		{"192.168.1.5", "192.168.1.6", false},
		{"", "example.com", false},
	} {
		require.Equal(t, c.want, SameSite(c.a, c.b), "%s -> %s", c.a, c.b)
	}
}
