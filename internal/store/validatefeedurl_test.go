package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ValidateFeedURL is the one syntax check for every way a feed URL enters the
// database (Reader subscribe and quickadd, web add, URL edit, OPML import). It
// refuses what is wrong with a URL on its face: not http(s), credentials in it,
// or a literal address in a blocked range. A hostname is never resolved here,
// so a name that points at a private address, and the alternate numeric
// spellings of an address (2130706433, 0x7f.1, 017700000001), pass this check
// and are stopped when the fetcher dials; internal/fetch/ssrfmatrix_test.go
// proves that for every entry point that makes the request.
func TestValidateFeedURLRefusesWhatIsWrongOnItsFace(t *testing.T) {
	for _, raw := range []string{
		// scheme
		"", " ", "ftp://example.com/feed", "file:///etc/passwd", "gopher://127.0.0.1:70/_x", "javascript:alert(1)",
		"data:text/plain,hi", "/feed", "feed", "http:///feed", "http://", "ws://example.com/", "feed:ftp://example.com/",
		"mailto:a@example.com", "view-source:http://example.com/", "jar:http://example.com!/",
		// userinfo, including the tricks where the visible host differs from the real one
		"http://user:pass@example.com/feed", "http://user@example.com/feed", "http://:pass@example.com/feed",
		"http://example.com@127.0.0.1/feed", "http://example.com:80@169.254.169.254/", "https://good.example%40evil@10.0.0.1/",
		// literal addresses in blocked ranges, canonical and mapped
		"http://127.0.0.1/", "http://127.255.255.254:8080/", "http://[::1]/", "http://[::ffff:127.0.0.1]/", "http://[::ffff:7f00:1]/",
		"http://[0:0:0:0:0:ffff:7f00:1]/", "http://[::127.0.0.1]/", "http://10.0.0.1/", "http://172.16.0.1/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://[::ffff:169.254.169.254]/", "http://[fd00:ec2::254]/", "http://[fe80::1]/",
		"http://100.64.0.1/", "http://100.100.100.100/", "http://0.0.0.0/", "http://[::]/", "http://224.0.0.1/", "http://255.255.255.255/",
		"http://[64:ff9b::7f00:1]/", "http://[2002:7f00:1::]/", "http://[fd00::1]/",
		// scheme case
		"HTTP://127.0.0.1/",
	} {
		_, _, _, err := ValidateFeedURL(raw, false)
		require.Error(t, err, "%q", raw)
	}
}

// What a person types is read as the address they meant (feedurl.Normalize), in every entry point,
// and the result still gets every check above: a typed private address is refused like a full one.
func TestValidateFeedURLReadsTypedAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"example.com/feed":            "https://example.com/feed",
		"//example.com/feed":          "https://example.com/feed",
		" feed://example.com/rss ":    "https://example.com/rss",
		"feed:http://example.com/rss": "http://example.com/rss",
		"Example.COM#top":             "https://example.com",
	} {
		norm, _, _, err := ValidateFeedURL(raw, false)
		require.NoError(t, err, raw)
		require.Equal(t, want, norm, raw)
	}
	for _, raw := range []string{"192.168.1.1/feed", "feed://10.0.0.1/rss", "127.0.0.1:8080"} {
		_, _, _, err := ValidateFeedURL(raw, false)
		var bad *InvalidURLError
		require.ErrorAs(t, err, &bad, raw)
		require.True(t, bad.Private, raw)
	}
}

// With the feed's private-network exception on, a literal private address is
// accepted (a LAN feed), but credentials and non-http schemes never are.
func TestValidateFeedURLAllowPrivateKeepsTheSyntaxChecks(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080/rss", "http://192.168.1.5/feed", "http://[::1]/f", "http://nas.lan/rss"} {
		_, _, _, err := ValidateFeedURL(raw, true)
		require.NoError(t, err, raw)
	}
	for _, raw := range []string{"ftp://192.168.1.5/f", "http://user:pw@192.168.1.5/f", "file:///etc/passwd", "http://"} {
		_, _, _, err := ValidateFeedURL(raw, true)
		require.Error(t, err, raw)
	}
}

// A hostname, even one that is an alternate spelling of a private address, is
// not resolved at this layer; the dial-time guard owns it (see above).
func TestValidateFeedURLDoesNotResolveNames(t *testing.T) {
	for _, raw := range []string{
		"http://localhost/feed", "http://metadata.google.internal/", "http://internal.example/feed",
		"http://2130706433/", "http://0x7f000001/", "http://0x7f.1/", "http://017700000001/", "http://127.1/", "http://127.0.0.1./",
	} {
		_, _, _, err := ValidateFeedURL(raw, false)
		require.NoError(t, err, raw)
	}
}
