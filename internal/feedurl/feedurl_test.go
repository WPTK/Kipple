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

// TestNormalizeTypedForms: every way a person types or pastes a feed or site address reads as the
// absolute URL they meant, in every entry point (they all reach parse).
func TestNormalizeTypedForms(t *testing.T) {
	for raw, want := range map[string]string{
		"example.com":                        "https://example.com",
		"  example.com/feed.xml \n":          "https://example.com/feed.xml",
		" https://example.com/rss\t":         "https://example.com/rss",
		"www.example.com/blog?format=rss":    "https://www.example.com/blog?format=rss",
		"//example.com/feed":                 "https://example.com/feed",
		"localhost:8080/feed":                "https://localhost:8080/feed",
		"example.com:8443/feed":              "https://example.com:8443/feed",
		"feed://example.com/rss":             "https://example.com/rss",
		"feed:https://example.com/rss":       "https://example.com/rss",
		"FEED:http://example.com/rss":        "http://example.com/rss",
		"feed:example.com/rss":               "https://example.com/rss",
		"pcast://example.com/podcast.xml":    "https://example.com/podcast.xml",
		"itpc://example.com/podcast.xml":     "https://example.com/podcast.xml",
		"podcast://example.com/p.xml":        "https://example.com/p.xml",
		"rss://example.com/rss":              "https://example.com/rss",
		"https://example.com/feed#comments":  "https://example.com/feed",
		"HTTPS://Example.COM/Feed":           "https://example.com/Feed",
		"HTTP://EXAMPLE.com:80/x":            "http://example.com/x",
		"http:/example.com/feed":             "http://example.com/feed",
		"https:example.com/feed":             "https://example.com/feed",
		"https://bücher.example/feed":        "https://b%C3%BCcher.example/feed",
		"http://[2001:db8::1]:8080/feed.xml": "http://[2001:db8::1]:8080/feed.xml",
	} {
		got, err := Normalize(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}
	// Still not addresses: a relative path, a lone word, other schemes, the internal pseudo-URLs.
	for _, bad := range []string{"", "   ", "/relative", "nas", "ftp://example.com/x", "mailto:me",
		"javascript:alert(1)", "kipple:archive", "kipple:deleting:12", "feed:", "http://", "file:///etc/passwd"} {
		_, err := Normalize(bad)
		require.Error(t, err, bad)
	}
	// An IDN host is accepted in either spelling and keyed by what was typed.
	k, err := Key("https://BÜCHER.example/feed")
	require.NoError(t, err)
	require.Equal(t, "bücher.example/feed", k)
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

// FuzzKeyAndNormalizeMatchesKeyOfNormalize: KeyAndNormalize must equal Normalize followed by Key
// for every input, errors included. Regression seeds are the cases a fuzz run found: whitespace
// at the end of the query before a fragment, and an IPv6 zone that does not survive a re-parse.
func FuzzKeyAndNormalizeMatchesKeyOfNormalize(f *testing.F) {
	for _, s := range []string{
		"http://h/p?a=b #x", "http://h/p?a=b\t#x", "http://h/p?a=b ", "https://Example.com:443/Feed?x=1#frag",
		"http://[fe80::1%25eth0]/feed", "http://[fe80::1%zz]/feed", "http://[::1%25]/x", "http://[fe80::1%25a b]/f",
		"http://h:80/p", "  http://h/p  ", "http://h/%zz", "http://h/a b?c d#e f", "ftp://h/x", "http:///x", "",
		"http://h?#", "http://h/p?#x", "HTTP://H:8080/P?Q=%41",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		norm, err := Normalize(raw)
		var wantKey string
		if err == nil {
			wantKey, err = Key(norm)
		}
		key, gotNorm, gerr := KeyAndNormalize(raw)
		if err != nil {
			if gerr == nil {
				t.Fatalf("KeyAndNormalize(%q) = %q, %q, nil; Normalize then Key fails: %v", raw, key, gotNorm, err)
			}
			return
		}
		if gerr != nil || key != wantKey || gotNorm != norm {
			t.Fatalf("KeyAndNormalize(%q) = %q, %q, %v; want %q, %q", raw, key, gotNorm, gerr, wantKey, norm)
		}
	})
}

// Credentials in the URL are refused everywhere a feed URL is parsed, so they are
// never stored, logged or exported; the feed's HTTP authentication is the one path.
func TestUserinfoRejected(t *testing.T) {
	for _, raw := range []string{"https://bob:secret@example.com/feed", "http://bob@example.com/feed", "https://:pw@example.com/f"} {
		_, err := Normalize(raw)
		require.ErrorIs(t, err, ErrUserinfo, raw)
		_, _, err = KeyAndNormalize(raw)
		require.ErrorIs(t, err, ErrUserinfo, raw)
		_, err = Key(raw)
		require.ErrorIs(t, err, ErrUserinfo, raw)
		_, err = Host(raw)
		require.ErrorIs(t, err, ErrUserinfo, raw)
	}
	_, err := Normalize("https://example.com/feed?u=bob@x")
	require.NoError(t, err, "an @ in the query is not userinfo")
}
