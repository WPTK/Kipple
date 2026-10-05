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
		default:
			serveRSS(w, r)
		}
	})
	return srv.URL, c, hits
}

// A feed stored as a page address reports the first feed the page links, without fetching it:
// the scheduler fetches it next, under its own host's limits.
func TestPageAtAFeedURLReportsItsFirstFeed(t *testing.T) {
	base, c, hits := discoverySite(t, pageWith([2]string{"/feed.xml", "application/rss+xml"}, [2]string{"/comments.xml", "application/rss+xml"}))
	res := doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.True(t, res.Success())
	require.Equal(t, base+"/feed.xml", res.Discovered, "the first linked feed, in page order")
	require.Nil(t, res.Feed)
	require.False(t, res.SetValidators)
	require.Equal(t, base+"/", res.Snap.URL)
	require.Equal(t, map[string]int{"/": 1}, hits, "no second request")
}

func TestRelativeLinksAndBaseHref(t *testing.T) {
	base, c, _ := discoverySite(t, pageWith([2]string{"../feed.xml", "application/atom+xml; charset=utf-8"}))
	res := doFetch(t, c, snapFor(base+"/blog/"))
	require.Equal(t, base+"/feed.xml", res.Discovered)

	page := `<!doctype html><html><head><base href="/sub/dir/"><link rel="alternate" type="application/rss+xml" href="feed.xml"></head></html>`
	base, c, _ = discoverySite(t, page)
	res = doFetch(t, c, snapFor(base+"/"))
	require.Equal(t, base+"/sub/dir/feed.xml", res.Discovered, "a <base href> sets the base, as in a browser")
}

func TestPageWithoutFeedLinksIsAPlainParseError(t *testing.T) {
	for _, page := range []string{
		pageWith(),
		pageWith([2]string{"/api/posts.json", "application/json"}), // an API, not a JSON Feed
	} {
		base, c, _ := discoverySite(t, page)
		res := doFetch(t, c, snapFor(base+"/"))
		require.Equal(t, OutcomeError, res.Outcome)
		require.Equal(t, ClassParse, res.ErrClass)
		require.Contains(t, res.ErrMsg, "does not link to a feed")
		require.Empty(t, res.Discovered)
	}
	base, c, _ := discoverySite(t, pageWith([2]string{"/feed.json", "application/feed+json"}))
	require.Equal(t, base+"/feed.json", doFetch(t, c, snapFor(base+"/")).Discovered)
}

// Only a URL as it was given, never fetched successfully, is followed: after a success, after a
// discovery or a URL edit (URLChanged), a page is the parse error it is. So it is followed once.
func TestDiscoveryOnlyForTheGivenURL(t *testing.T) {
	base, c, _ := discoverySite(t, pageWith([2]string{"/feed.xml", "application/rss+xml"}))
	s := snapFor(base + "/")
	s.LastSuccessAt = t0.Unix() - 3600
	res := doFetch(t, c, s)
	require.Equal(t, ClassParse, res.ErrClass)
	require.Empty(t, res.Discovered)
	s = snapFor(base + "/")
	s.URLChanged = true
	res = doFetch(t, c, s)
	require.Equal(t, ClassParse, res.ErrClass)
	require.Empty(t, res.Discovered)
}

// The decision itself, with no network: credentials stay with their host, exceptions with their
// site, and a literal private address is refused without the exception.
func TestDiscoverFeedRefusals(t *testing.T) {
	page := func(href string) []byte { return []byte(pageWith([2]string{href, "application/rss+xml"})) }
	run := func(s Snapshot, href string) *Result {
		return discoverFeed(&Result{Snap: s, FinalURL: s.URL}, page(href))
	}
	auth := Snapshot{ID: 1, URL: "https://example.com/", HTTPAuth: "bob:secret"}
	res := run(auth, "https://example.com/feed.xml")
	require.Equal(t, "https://example.com/feed.xml", res.Discovered, "same host: followed")
	res = run(auth, "https://feeds.example.com/feed.xml")
	require.Equal(t, ClassParse, res.ErrClass, "same site, another host: withheld")
	require.Contains(t, res.ErrMsg, "the feed's login is only sent to example.com")
	require.Empty(t, res.Discovered)

	priv := Snapshot{ID: 1, URL: "http://nas.lan/", AllowPrivateNet: true}
	require.Equal(t, "http://nas.lan/rss", run(priv, "/rss").Discovered)
	res = run(priv, "https://elsewhere.example.org/rss")
	require.Contains(t, res.ErrMsg, "on another site")

	plain := Snapshot{ID: 1, URL: "https://example.com/"}
	res = run(plain, "http://192.168.1.10/feed.xml")
	require.Equal(t, ClassSSRF, res.ErrClass)
	require.Empty(t, res.Discovered)
	res = run(plain, "http://[::1]/feed.xml")
	require.Equal(t, ClassSSRF, res.ErrClass)
	require.Equal(t, "https://feeds.example.org/x", run(plain, "https://feeds.example.org/x").Discovered)
}

func TestLooksHTML(t *testing.T) {
	for _, s := range []string{"  <!DOCTYPE html><html>", "<html lang=en>", "\ufeff<!doctype html>", "<!-- generated --><!DOCTYPE html>",
		"<head><title>x</title></head>", `<?xml version="1.0" encoding="utf-8"?><!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "x"><html xmlns="http://www.w3.org/1999/xhtml">`,
		`<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml">`} {
		require.True(t, LooksHTML([]byte(s)), s)
	}
	for _, s := range []string{rssBody, `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">`, "", "<!-- unterminated", `{"version":"https://jsonfeed.org/version/1"}`} {
		require.False(t, LooksHTML([]byte(s)), s)
	}
}
