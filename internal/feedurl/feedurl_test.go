package feedurl

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeKeyHost(t *testing.T) {
	n, err := Normalize("HTTP://Example.COM:80/Feed.xml?a=1#frag")
	require.NoError(t, err)
	require.Equal(t, "http://example.com/Feed.xml?a=1", n)

	k1, _ := Key("http://Example.com/feed?x=1")
	k2, _ := Key("https://example.com:443/feed?x=1#top")
	require.Equal(t, "example.com/feed?x=1", k1)
	require.Equal(t, k1, k2, "http and https variants collide")

	k3, _ := Key("https://example.com:8443/feed")
	require.Equal(t, "example.com:8443/feed", k3)

	h, _ := Host("https://Sub.Example.com:8443/x")
	require.Equal(t, "sub.example.com", h)

	k6, _ := Key("http://[::1]:8080/f")
	require.Equal(t, "[::1]:8080/f", k6)

	_, err = Key("ftp://x/y")
	require.Error(t, err)
	_, err = Key("/relative")
	require.Error(t, err)
}

func TestKeyAndNormalize(t *testing.T) {
	for _, raw := range []string{"HTTP://Example.COM:80/Feed.xml?a=1#frag", " https://x.org:443/a b ", "https://[::1]:8080/p"} {
		key, norm, err := KeyAndNormalize(raw)
		require.NoError(t, err)
		wn, _ := Normalize(raw)
		wk, _ := Key(raw)
		require.Equal(t, wn, norm)
		require.Equal(t, wk, key)
	}
	for _, bad := range []string{"", "ftp://x/y", "/relative", "http://"} {
		_, _, err := KeyAndNormalize(bad)
		require.Error(t, err, bad)
	}
}
