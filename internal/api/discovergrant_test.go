package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
	"github.com/stretchr/testify/require"
)

// grantSite is a loopback test server that answers on two sites: "localhost" and "127.0.0.1" are
// different hosts to fetch.SameSite, and both are private addresses to the dial guard. GET / is a page
// whose feed links are the given hrefs; every other path is a feed, and each request to one is counted.
type grantSite struct {
	port  string // ":12345"
	feeds atomic.Int64
}

func newGrantSite(t *testing.T, links ...string) *grantSite {
	t.Helper()
	gs := &grantSite{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			var b strings.Builder
			b.WriteString("<html><head><title>page</title>")
			for _, l := range links {
				b.WriteString(`<link rel="alternate" type="application/rss+xml" title="` + l + `" href="` + strings.ReplaceAll(l, "PORT", gs.port) + `">`)
			}
			b.WriteString("</head><body></body></html>")
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		gs.feeds.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)
	gs.port = srv.URL[strings.LastIndex(srv.URL, ":"):]
	return gs
}

func (gs *grantSite) url(host, path string) string { return "http://" + host + gs.port + path }

func (h *harness) feedCount() (n int) {
	h.t.Helper()
	require.NoError(h.t, h.db.Reader().QueryRow("SELECT COUNT(*) FROM feeds").Scan(&n))
	return n
}

func requireElsewhere(t *testing.T, code int, body map[string]any, link string) {
	t.Helper()
	require.Equal(t, "private_address", body["error"], body)
	msg, _ := body["message"].(string)
	require.Contains(t, msg, "The page links its feed on another site, at a private address ("+link+").")
	require.Contains(t, msg, `To use that feed, enter its own address, tick "Allow addresses on my own network"`)
	require.Contains(t, []int{400, 422}, code)
}

// A page added with "Allow addresses on my own network" that links its one feed on another site, at a
// literal private address: the option is for the page's site, so the feed is refused and never asked.
func TestAddLinkedFeedOnAnotherSiteAtPrivateAddressIsRefused(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "http://127.0.0.1PORT/feed")
	h := newHarness(t) // the real dial guard
	c := h.login()
	before := h.feedCount()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": gs.url("localhost", "/"), "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	requireElsewhere(t, code, body, gs.url("127.0.0.1", "/feed"))
	require.Zero(t, gs.feeds.Load(), "the linked feed was never asked")
	require.Equal(t, before, h.feedCount())
}

// The same with a name on another site that resolves to a private address: the probe of the linked feed
// runs with what the saved feed may reach (no exception there), so the guard refuses it before anything
// is created.
func TestAddLinkedFeedOnAnotherSiteResolvingPrivateIsRefused(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "http://localhostPORT/feed")
	h := newHarness(t)
	c := h.login()
	before := h.feedCount()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": gs.url("127.0.0.1", "/"), "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	requireElsewhere(t, code, body, gs.url("localhost", "/feed"))
	require.Zero(t, gs.feeds.Load(), "the linked feed was never asked")
	require.Equal(t, before, h.feedCount())
}

// A feed the page links on its own site (another port of the same host) keeps the exception.
func TestAddLinkedFeedOnTheSameSiteKeepsTheException(t *testing.T) {
	t.Parallel()
	feed := newGrantSite(t)
	gs := newGrantSite(t, feed.url("localhost", "/feed"))
	h := newHarness(t)
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": gs.url("localhost", "/"), "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok", body["status"])
	id := h.feedByURL(feed.url("localhost", "/feed"))
	require.Equal(t, "1", h.feedRow(id, "allow_private_net").String)
}

// A feed linked on another site that is reachable without the exception is added, without it.
func TestAddLinkedFeedOnAnotherSiteLeavesTheExceptionBehind(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "http://localhostPORT/feed")
	h := newHarness(t, func(o *Options) { o.Guard = openGuard }) // every address reachable
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": gs.url("127.0.0.1", "/"), "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	id := h.feedByURL(gs.url("localhost", "/feed"))
	require.Equal(t, "0", h.feedRow(id, "allow_private_net").String)
}

// A page that links several feeds: the pick comes back with the address entered (discovered_from), and
// is held to the same rule as a single linked feed, whether it is a literal or a name.
func TestAddPickedFeedOnAnotherSiteAtPrivateAddressIsRefused(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ page, other string }{{"localhost", "127.0.0.1"}, {"127.0.0.1", "localhost"}} {
		gs := newGrantSite(t, "http://"+tc.other+"PORT/a", "http://"+tc.other+"PORT/b")
		h := newHarness(t)
		c := h.login()
		before := h.feedCount()
		page := gs.url(tc.page, "/")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": page, "allow_private_net": true}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "choose", body["status"], body)
		require.Len(t, body["candidates"], 2)
		pick := gs.url(tc.other, "/b")
		code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": pick, "discovered_from": page, "allow_private_net": true}))
		requireElsewhere(t, code, body, pick)
		require.Zero(t, gs.feeds.Load(), "the picked feed was never asked")
		require.Equal(t, before, h.feedCount())
	}
}

// A pick on the site entered keeps the exception.
func TestAddPickedFeedOnTheSameSiteKeepsTheException(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "/a", "/b")
	h := newHarness(t)
	c := h.login()
	page := gs.url("localhost", "/")
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": page, "allow_private_net": true}))
	require.Equal(t, "choose", body["status"], code)
	pick := gs.url("localhost", "/b")
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": pick, "discovered_from": page, "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	require.Equal(t, "1", h.feedRow(h.feedByURL(pick), "allow_private_net").String)
}

func TestAddRefusesABadDiscoveredFrom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "https://a.example/feed", "discovered_from": "not an address"}))
	require.Equal(t, 400, code)
	require.Equal(t, "bad_request", body["error"])
}

// The feed editor: an exception the patch sets is for the address typed, not for a feed that page links
// on another site.
func TestEditLinkedFeedOnAnotherSiteAtPrivateAddressIsRefused(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "http://127.0.0.1PORT/feed")
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://old.example/feed")
	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(id), jsonStr(map[string]any{"url": gs.url("localhost", "/"), "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	requireElsewhere(t, code, body, gs.url("127.0.0.1", "/feed"))
	require.Contains(t, body["message"], "under Unsafe options below and save again.")
	require.Zero(t, gs.feeds.Load())
	require.Equal(t, "https://old.example/feed", h.feedRow(id, "url").String)
	require.Equal(t, "0", h.feedRow(id, "allow_private_net").String)
}

func TestEditLinkedFeedOnAnotherSiteLeavesTheExceptionBehind(t *testing.T) {
	t.Parallel()
	gs := newGrantSite(t, "http://localhostPORT/feed")
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	id := h.storeFeed("https://old.example/feed")
	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(id), jsonStr(map[string]any{"url": gs.url("127.0.0.1", "/"), "allow_private_net": true, "allow_insecure_tls": true}))
	require.Equal(t, 200, code, body)
	require.Equal(t, gs.url("localhost", "/feed"), h.feedRow(id, "url").String)
	require.Equal(t, "0", h.feedRow(id, "allow_private_net").String)
	require.Equal(t, "0", h.feedRow(id, "allow_insecure_tls").String)
}

// redirectSite serves a page at / that links /feed, and /feed redirects to to(port); requests to
// /secret are counted.
func redirectSite(t *testing.T, to func(port string) string) (port string, secret *atomic.Int64) {
	t.Helper()
	secret = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="` + linkOf(r) + `"></head></html>`))
		case "/feed":
			http.Redirect(w, r, to(port), http.StatusFound)
		case "/secret":
			secret.Add(1)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(rssBody))
		}
	}))
	t.Cleanup(srv.Close)
	port = srv.URL[strings.LastIndex(srv.URL, ":"):]
	return port, secret
}

// linkOf is the page's feed link: ?link= when given, else /feed on the page's own host.
func linkOf(r *http.Request) string {
	if l := r.URL.Query().Get("link"); l != "" {
		return l
	}
	return "/feed"
}

// is ticked, so the message says where the redirect goes rather than to tick it.
// is ticked, so the message says where the redirect goes rather than to tick the option.
func TestAddLinkedFeedRedirectingToAPrivateAddressElsewhereSaysSo(t *testing.T) {
	t.Parallel()
	port, secret := redirectSite(t, func(port string) string { return "http://127.0.0.1" + port + "/secret" })
	h := newHarness(t)
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://localhost" + port + "/", "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Contains(t, body["message"], "That address (http://localhost"+port+"/feed) redirects to a private address (http://127.0.0.1"+port+"/secret).")
	require.Contains(t, body["message"], "not one it redirects to")
	require.Zero(t, secret.Load())
}

// namePublicGuard treats the name localhost as a public address and 127.0.0.1 as a private one, so a
// test can have a public site: without the exception a request to 127.0.0.1 is refused as the dial guard
// refuses one.
func namePublicGuard(allowPrivate, _, _ bool) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !allowPrivate && r.URL.Hostname() == "127.0.0.1" {
			return nil, &fetch.BlockedError{IP: netip.MustParseAddr("127.0.0.1")}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A public feed on another site that redirects to a private address: the message is about the redirect,
// not about entering the linked feed's address (that would not help: the redirect goes elsewhere).
func TestAddLinkedPublicFeedRedirectingToAPrivateAddressSaysSo(t *testing.T) {
	t.Parallel()
	port, secret := redirectSite(t, func(port string) string { return "http://127.0.0.1" + port + "/secret" })
	h := newHarness(t, func(o *Options) { o.Guard = namePublicGuard })
	c := h.login()
	link := "http://localhost" + port + "/feed"
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://127.0.0.1" + port + "/?link=" + link, "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Contains(t, body["message"], "That address ("+link+") redirects to a private address (http://127.0.0.1"+port+"/secret).")
	require.NotContains(t, body["message"], "links its feed on another site")
	require.Zero(t, secret.Load())
}

// A feed row stored before the address cap (its address no longer parses) still has the host it was
// stored with, and an edit to a page on that site compares against it, as the patch itself does: the
// probe keeps the private-network exception and finds the feed the page links.
func TestEditFeedWithAnOverCapAddressKeepsTheExceptionOnItsSite(t *testing.T) {
	t.Parallel()
	feed := newGrantSite(t)
	gs := newGrantSite(t, feed.url("localhost", "/feed"))
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed(gs.url("localhost", "/old"), func(nf *store.NewFeed) { nf.AllowPrivateNet = true })
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		// url_key stays as it was: this path reads only the url and the host column.
		_, err := tx.ExecContext(ctx, "UPDATE feeds SET url = ? WHERE id = ?", gs.url("localhost", "/"+strings.Repeat("a", 5000)), id)
		return err
	}))
	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(id), jsonStr(map[string]any{"url": gs.url("localhost", "/")}))
	require.Equal(t, 200, code, body)
	require.Equal(t, feed.url("localhost", "/feed"), h.feedRow(id, "url").String)
	require.Equal(t, "1", h.feedRow(id, "allow_private_net").String)
}
