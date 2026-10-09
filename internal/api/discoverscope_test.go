package api

import (
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
