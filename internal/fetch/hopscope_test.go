package fetch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// A feed's allow_private_net covers its own site only: a redirect hop to another
// host is dialled through the guarded transport, so a LAN feed cannot redirect
// the fetcher to any other private address. A hop to the same host (another
// port) keeps the exception.
func TestPrivateNetExceptionIsScopedPerHop(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(serveRSS))
	t.Cleanup(target.Close)
	tu, _ := url.Parse(target.URL)

	for _, c := range []struct {
		name, to string
		ok       bool
	}{
		{"same host", "http://127.0.0.1:" + tu.Port() + "/feed", true},
		{"other host", "http://localhost:" + tu.Port() + "/feed", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, cl := feedServer(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, c.to, http.StatusFound)
			})
			res := doFetch(t, cl, snapFor(srv.URL+"/rss"))
			if c.ok {
				require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
				return
			}
			require.Equal(t, ClassSSRF, res.ErrClass, res.ErrMsg)
		})
	}
}

type recordRT struct {
	name string
	used *string
}

func (r recordRT) RoundTrip(*http.Request) (*http.Response, error) {
	*r.used = r.name
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

// The review exploit: a LAN feed on a bare hostname (http://nas/feed) with
// allow_private_net redirects to a public name that merely starts with that
// label (nas.attacker.example, whose DNS points at a private address). That hop
// must go through the guarded transport; only the reserved local suffixes
// (nas.lan, nas.home.arpa, ...) keep the grant.
func TestSiteScopedBareNamePrefixDoesNotInheritGrant(t *testing.T) {
	for _, c := range []struct {
		feed, hop, want string
	}{
		{"nas", "http://nas.attacker.example/", "guarded"},
		{"nas", "http://nas.evil.com:8080/feed", "guarded"},
		{"nas", "http://nas.x.lan/", "guarded"},
		{"nas", "http://nas/other", "granted"},
		{"nas", "http://nas.lan/feed", "granted"},
		{"nas", "http://NAS.home.arpa./feed", "granted"},
		{"nas", "http://nas.internal/feed", "granted"},
		{"nas.local", "http://nas/feed", "granted"},
		{"nas.attacker.example", "http://nas/feed", "guarded"},
	} {
		var used string
		s := &siteScoped{host: c.feed, granted: recordRT{"granted", &used}, guarded: recordRT{"guarded", &used}}
		req, err := http.NewRequest(http.MethodGet, c.hop, nil)
		require.NoError(t, err)
		resp, err := s.RoundTrip(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.Equal(t, c.want, used, "%s -> %s", c.feed, c.hop)
	}
}
