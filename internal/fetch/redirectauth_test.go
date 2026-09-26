package fetch

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// The feed's Basic credentials must not follow an https -> http downgrade on the
// same host: net/http keeps Authorization for it (it strips it only for an
// unrelated host), which would send the password in clear text.
func TestAuthDroppedOnHTTPSDowngradeRedirect(t *testing.T) {
	var plainAuth atomic.Value
	plainAuth.Store("unset")
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainAuth.Store(r.Header.Get("Authorization"))
		serveRSS(w, r)
	}))
	defer plain.Close()
	var tlsAuth atomic.Value
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsAuth.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, plain.URL+"/feed", http.StatusFound) // same host 127.0.0.1, plain http
	}))
	defer secure.Close()

	c := NewClient(ClientOptions{})
	s := snapFor(secure.URL + "/feed")
	s.AllowInsecureTLS = true
	s.HTTPAuth = "bob:secret"
	res := doFetch(t, c, s)
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, "Basic Ym9iOnNlY3JldA==", tlsAuth.Load(), "the feed's own https URL gets the credentials")
	require.Equal(t, "", plainAuth.Load(), "the http hop must not")
}

// An http feed that moves to https on the same host keeps its credentials.
func TestAuthKeptOnSameHostUpgradeRedirect(t *testing.T) {
	var tlsAuth atomic.Value
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsAuth.Store(r.Header.Get("Authorization"))
		serveRSS(w, r)
	}))
	defer secure.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secure.URL+"/feed", http.StatusMovedPermanently)
	}))
	defer plain.Close()

	c := NewClient(ClientOptions{})
	s := snapFor(plain.URL + "/feed")
	s.AllowInsecureTLS = true
	s.HTTPAuth = "bob:secret"
	res := doFetch(t, c, s)
	require.Equal(t, OutcomeOK, res.Outcome, res.ErrMsg)
	require.Equal(t, "Basic Ym9iOnNlY3JldA==", tlsAuth.Load())
}

func TestAuthAllowed(t *testing.T) {
	u := func(s string) *url.URL { v, err := url.Parse(s); require.NoError(t, err); return v }
	for _, tc := range []struct {
		feed, target string
		want         bool
	}{
		{"https://example.com/f", "https://example.com/g", true},
		{"https://example.com/f", "https://EXAMPLE.com:8443/g", true},
		{"http://example.com/f", "http://example.com/g", true},
		{"http://example.com/f", "https://example.com/g", true},
		{"https://example.com/f", "http://example.com/g", false},    // downgrade
		{"https://example.com/f", "https://a.example.com/g", false}, // subdomain: net/http would keep it
		{"https://example.com/f", "https://example.org/g", false},
	} {
		require.Equal(t, tc.want, authAllowed(u(tc.feed), u(tc.target)), "%s -> %s", tc.feed, tc.target)
	}
	require.False(t, authAllowed(nil, u("https://example.com/")))
}
