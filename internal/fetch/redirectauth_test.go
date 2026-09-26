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
		{"https://example.com/f", "https://www.example.com/g", true}, // subdomain: kept, as net/http does
		{"https://example.com/f", "https://a.b.Example.com./g", true},
		{"https://example.com/f", "http://www.example.com/g", false}, // subdomain but a downgrade
		{"https://www.example.com/f", "https://example.com/g", false}, // parent: net/http strips it too
		{"https://example.com/f", "https://badexample.com/g", false},
		{"https://example.com/f", "https://example.com.evil.test/g", false},
		{"https://example.com/f", "https://example.org/g", false},
		{"http://192.0.2.1/f", "http://192.0.2.1:8080/g", true},
		{"http://192.0.2.1/f", "http://x.192.0.2.1/g", false},
	} {
		require.Equal(t, tc.want, authAllowed(u(tc.feed), u(tc.target)), "%s -> %s", tc.feed, tc.target)
	}
	require.False(t, authAllowed(nil, u("https://example.com/")))
}

// A basic-auth feed that 301s from the bare host to www keeps its credentials
// on the redirect hop (it used to lose them and get 401 forever).
func TestAuthKeptOnSubdomainRedirectHop(t *testing.T) {
	c := NewClient(ClientOptions{})
	var hops []Hop
	feed, _ := url.Parse("https://example.com/feed")
	hc := c.httpClient(variant{}, &hops, feed)
	prev := httptest.NewRequest(http.MethodGet, "https://example.com/feed", nil)
	next := httptest.NewRequest(http.MethodGet, "https://www.example.com/feed", nil)
	next.Header.Set("Authorization", "Basic Ym9iOnNlY3JldA==")
	require.NoError(t, hc.CheckRedirect(next, []*http.Request{prev}))
	require.Equal(t, "Basic Ym9iOnNlY3JldA==", next.Header.Get("Authorization"))

	other := httptest.NewRequest(http.MethodGet, "https://example.org/feed", nil)
	other.Header.Set("Authorization", "Basic Ym9iOnNlY3JldA==")
	require.NoError(t, hc.CheckRedirect(other, []*http.Request{prev}))
	require.Empty(t, other.Header.Get("Authorization"))
}
