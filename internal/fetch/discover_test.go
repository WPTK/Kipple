package fetch

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pageWith is a web page whose <head> links the given feeds (href, type) in order.
func pageWith(links ...[2]string) string {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><title>Blog</title>")
	for _, l := range links {
		b.WriteString(`<link rel="alternate" type="` + l[1] + `" href="` + l[0] + `">`)
	}
	b.WriteString(`</head><body><a href="/feed.xml">not a head link</a></body></html>`)
	return b.String()
}

func discoverySite(t *testing.T, page string) (base string, c *Client, hits map[string]int) {
	t.Helper()
	hits = map[string]int{}
	srv, c := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/", "/blog/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		case "/feed.xml", "/comments.xml":
			serveRSS(w, r)
		case "/gone.xml":
			w.WriteHeader(http.StatusGone)
		default:
			http.NotFound(w, r)
		}
	})
	return srv.URL, c, hits
}

// A feed stored as a web page address (a Reader API subscribe, an OPML outline) becomes the feed the
// page links, on its first fetch, in the same attempt.
func TestFirstFetchOfAPageDiscoversItsFeed(t *testing.T) {
	base, c, hits := discoverySite(t, pageWith([2]string{"/feed.xml", "application/rss+xml"}, [2]string{"/comments.xml", "application/rss+xml"}))
	res := doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, base+"/feed.xml", res.Discovered, "the first linked feed, in page order")
	require.Equal(t, base+"/feed.xml", res.FeedURL())
	require.Equal(t, base+"/", res.Snap.URL, "the snapshot stays the stored URL the commit checks")
	require.Len(t, res.Feed.Items, 1)
	require.Equal(t, 1, hits["/"])
	require.Equal(t, 1, hits["/feed.xml"])
	require.Zero(t, hits["/comments.xml"])
	require.Equal(t, RedirectClear, res.Redirect.Action)
}

func TestRelativeAndSchemeRelativeLinksResolveAgainstThePage(t *testing.T) {
	base, c, _ := discoverySite(t, pageWith([2]string{"../feed.xml", "application/atom+xml; charset=utf-8"}))
	res := doFetch(t, c, snapFor(base+"/blog/"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, base+"/feed.xml", res.Discovered)
}

func TestPageWithoutFeedLinksIsAPlainParseError(t *testing.T) {
	base, c, hits := discoverySite(t, pageWith())
	res := doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, OutcomeError, res.Outcome)
	require.Equal(t, ClassParse, res.ErrClass)
	require.Contains(t, res.ErrMsg, "does not link to a feed")
	require.Empty(t, res.Discovered)
	require.Zero(t, hits["/feed.xml"], "a link in the body is not a feed link")
}

// A feed that has fetched successfully before is never replaced by a page's feed.
func TestNoDiscoveryAfterAFirstSuccess(t *testing.T) {
	base, c, hits := discoverySite(t, pageWith([2]string{"/feed.xml", "application/rss+xml"}))
	s := snapFor(base + "/")
	s.LastSuccessAt = t0.Unix() - 3600
	res := doFetch(t, c, s)
	require.Equal(t, ClassParse, res.ErrClass)
	require.Empty(t, res.Discovered)
	require.Zero(t, hits["/feed.xml"])
}

// A linked feed that fails leaves the page as the address (the next attempt tries again), and a 410
// from it does not disable the feed.
func TestLinkedFeedThatFailsKeepsThePage(t *testing.T) {
	base, c, _ := discoverySite(t, pageWith([2]string{"/gone.xml", "application/rss+xml"}))
	res := doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, OutcomeError, res.Outcome)
	require.False(t, res.Gone)
	require.Empty(t, res.Discovered)
	require.Contains(t, res.ErrMsg, "this address is a web page; the feed it links, "+base+"/gone.xml, failed")
}

// A linked feed is never itself a page that is followed again.
func TestDiscoveryFollowsOneLinkOnly(t *testing.T) {
	base, c, hits := discoverySite(t, pageWith([2]string{"/blog/", "application/rss+xml"}))
	res := doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, ClassParse, res.ErrClass)
	require.Equal(t, 1, hits["/blog/"])
}

// With credentials or network exceptions, a page that links a feed on another site is not followed
// (the exceptions were granted for the feed's own site).
func TestDiscoveryKeepsExceptionsOnTheSite(t *testing.T) {
	base, c, _ := discoverySite(t, pageWith([2]string{"https://feeds.elsewhere.example/feed.xml", "application/rss+xml"}))
	s := snapFor(base + "/") // AllowPrivateNet: the test server is on loopback
	res := doFetch(t, c, s)
	require.Equal(t, ClassParse, res.ErrClass)
	require.Contains(t, res.ErrMsg, "https://feeds.elsewhere.example/feed.xml, on another site")
}

func TestLooksHTML(t *testing.T) {
	require.True(t, LooksHTML([]byte("  <!DOCTYPE html><html>")))
	require.True(t, LooksHTML([]byte("<html lang=en>")))
	require.False(t, LooksHTML([]byte(rssBody)))
	require.False(t, LooksHTML(nil))
}
