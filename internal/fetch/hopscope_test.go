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
