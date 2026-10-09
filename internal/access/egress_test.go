package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// The team domain names a host. An address is not a team domain, in any of the
// spellings a resolver or a URL parser would accept for one.
func TestTeamDomainIsNeverAnAddress(t *testing.T) {
	for _, in := range []string{
		"1.2.3.4", "127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "127.1", "0.0.0.0",
		"https://127.0.0.1", "2130706433", "0x7f.0.0.1", "[::1]", "::1", "08.8.8.8",
	} {
		_, err := NormalizeTeamDomain(in)
		require.Error(t, err, in)
	}
	for _, in := range []string{"myteam.cloudflareaccess.com", "https://myteam.cloudflareaccess.com/", "a.b.example", "team1.example"} {
		_, err := NormalizeTeamDomain(in)
		require.NoError(t, err, in)
	}
}

// Without a client of its own the verifier fetches keys through the guarded
// transport: a key set on a private address is not reachable, whatever the team
// domain setting says.
func TestKeySetIsNotFetchedFromAPrivateAddress(t *testing.T) {
	a, _, _ := testKeys(t)
	cs := newCertsServer(t, jwk("a", &a.PublicKey))
	v, err := New("myteam.cloudflareaccess.com", testAUD, Options{CertsURL: cs.URL})
	require.NoError(t, err)
	_, err = v.load(context.Background())
	require.Error(t, err)
	require.Zero(t, cs.hits.Load(), "the server was never reached")
}

// A redirect from the key set address is an answer, not a place to go.
func TestKeySetRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer first.Close()
	c := defaultClient()
	c.Transport = http.DefaultTransport // this test is about the redirect, not the address guard
	resp, err := c.Get(first.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Zero(t, elsewhere)
}

func TestKeySetClientUsesNoProxy(t *testing.T) {
	tr, ok := defaultClient().Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, tr.Proxy)
}
