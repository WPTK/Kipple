package api

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// A feed added with "Allow addresses on my own network" may reach its own
// host, not whatever the page redirects to: a hop to another host goes through
// the guard like any other request.
func TestDiscoveryHopsLeaveTheNetworkExceptionBehind(t *testing.T) {
	t.Parallel()
	var secret atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/secret":
			secret.Add(1)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(rssBody))
		default:
			// The page is reached as localhost; the redirect names the same server by its address.
			http.Redirect(w, r, "http://"+strings.Replace(r.Host, "localhost", "127.0.0.1", 1)+"/secret", http.StatusFound)
		}
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	h := newHarness(t) // the real dial guard
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://localhost" + port + "/start", "allow_private_net": true}))
	require.Equal(t, 422, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Zero(t, secret.Load(), "the redirect target was never asked")
}

// A page added with "Allow addresses on my own network" that links one feed on another site: the
// grant stays with the page's site. A link to a private address there is refused before it is
// fetched, and a feed found on a public site is subscribed without the grant.
func TestDiscoveredFeedOnAnotherSiteLeavesTheNetworkExceptionBehind(t *testing.T) {
	t.Parallel()
	var feedHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/private":
			// The page is reached as localhost; its feed link names another site on a private address.
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="http://` +
				strings.Replace(r.Host, "localhost", "127.0.0.1", 1) + `/feed.xml"></head></html>`))
		case "/public":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="http://feeds.example/feed.xml"></head></html>`))
		case "/feed.xml":
			feedHits.Add(1)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(rssBody))
		}
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	h := newHarness(t) // the real dial guard
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://localhost" + port + "/private", "allow_private_net": true}))
	require.Equal(t, 400, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Zero(t, feedHits.Load(), "the linked feed was never asked")
	require.Zero(t, h.count("SELECT count(*) FROM feeds"))

	// Every address answers from the test server here, so the public site's feed can be reached.
	shortWaits(t)
	addr := srv.Listener.Addr().String()
	toSrv := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	t.Cleanup(toSrv.CloseIdleConnections)
	h2 := newHarness(t, func(o *Options) { o.Guard = func(bool, bool, bool) http.RoundTripper { return toSrv } })
	c2 := h2.login()
	code, body, _ = h2.api(c2, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://localhost" + port + "/public", "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	feed := body["feed"].(map[string]any)
	require.Equal(t, "http://feeds.example/feed.xml", feed["url"])
	require.Equal(t, false, feed["allow_private_net"])
}
