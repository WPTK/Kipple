package greader

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// [K §6.1] CORS: a client that runs in a web page on another origin can call every Reader API path.
// The API authenticates by the Authorization header (or T), never by a cookie, so the answer allows
// any origin and no credentials. The web app's own routes are not part of it.
func TestConformanceCORS(t *testing.T) {
	h := newHarness(t)
	seedConf(h)
	c := newConf(t, h)
	origin := map[string]string{"Origin": "https://reader.example"}

	// A preflight is answered before authentication (it never carries the token), for the prefixed
	// and the root forms alike, and for a path the API does not know.
	c.auth = ""
	for _, p := range []string{
		"/accounts/ClientLogin", rd + "stream/contents/user/-/state/com.google/reading-list", rd + "edit-tag",
		rd + "subscription/quickadd", rd + "no-such-endpoint", "/", "/check/compatibility",
	} {
		r := c.call(http.MethodOptions, p, "", map[string]string{
			"Origin": "https://reader.example", "Access-Control-Request-Method": "POST",
			"Access-Control-Request-Headers": "authorization, content-type",
		})
		require.Equal(t, http.StatusNoContent, r.code, p)
		require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"), p)
		require.Contains(t, r.header.Get("Access-Control-Allow-Methods"), "POST", p)
		require.Contains(t, r.header.Get("Access-Control-Allow-Headers"), "Authorization", p)
		require.Contains(t, r.header.Get("Access-Control-Allow-Headers"), "Content-Type", p)
		require.Empty(t, r.header.Get("Access-Control-Allow-Credentials"), p)
		require.Empty(t, r.body, p)
	}
	// The root forms too (a client that only takes a server address).
	root := newClient(t, h, "", confUA)
	r := root.doAny(http.MethodOptions, "/reader/api/0/subscription/list", "", origin)
	require.Equal(t, http.StatusNoContent, r.code)
	require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))

	// Real responses carry the origin and expose the headers a client reads: success, an
	// authentication failure (so the page can see the 401 and its bad-token header), a write.
	c.auth = "GoogleLogin auth=" + c.token
	r = c.call(http.MethodGet, rd+"subscription/list?output=json", "", origin)
	require.Equal(t, 200, r.code)
	require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, r.header.Get("Access-Control-Expose-Headers"), "ETag")
	r = c.call(http.MethodPost, rd+"edit-tag", "a="+url.QueryEscape(stateStarred)+"&i=1&T="+url.QueryEscape(c.token), origin)
	require.Equal(t, 200, r.code)
	require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	c.auth = "GoogleLogin auth=wrong"
	r = c.call(http.MethodGet, rd+"user-info", "", origin)
	require.Equal(t, 401, r.code)
	require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))
	require.Contains(t, r.header.Get("Access-Control-Expose-Headers"), "X-Reader-Google-Bad-Token")
	r = c.call(http.MethodPost, "/accounts/ClientLogin", loginBody(), origin)
	require.Equal(t, 200, r.code)
	require.Equal(t, "*", r.header.Get("Access-Control-Allow-Origin"))

	// Paths that are not the Reader API (the web app's /api routes) get no CORS headers, and an
	// OPTIONS there is not answered by the Reader API.
	other := newClient(t, h, "", confUA)
	for _, p := range []string{"/api/items", "/api/bootstrap", "/"} {
		r := other.doAny(http.MethodOptions, p, "", origin)
		require.Equal(t, http.StatusTeapot, r.code, p)
		require.Empty(t, r.header.Get("Access-Control-Allow-Origin"), p)
	}
	r = other.doAny(http.MethodGet, "/api/items", "", origin)
	require.Empty(t, r.header.Get("Access-Control-Allow-Origin"))
}
