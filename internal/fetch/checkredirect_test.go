package fetch

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func hopRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return &http.Request{URL: u, Header: http.Header{}}
}

func TestCheckRedirectPolicy(t *testing.T) {
	via := func(raw ...string) []*http.Request {
		var out []*http.Request
		for _, r := range raw {
			out = append(out, hopRequest(t, r))
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		from    string
		to      string
		prior   int // earlier hops before from
		refused bool
	}{
		{"http to http", "http://a.example/", "http://b.example/", 0, false},
		{"http to https", "http://a.example/", "https://a.example/", 0, false},
		{"https to https", "https://a.example/", "https://b.example/", 0, false},
		{"https to http", "https://a.example/", "http://a.example/", 0, true},
		{"gopher", "http://a.example/", "gopher://a.example/", 0, true},
		{"file", "http://a.example/", "file:///etc/passwd", 0, true},
		{"ftp", "http://a.example/", "ftp://a.example/x", 0, true},
		{"data", "http://a.example/", "data:text/plain,x", 0, true},
		{"javascript", "http://a.example/", "javascript:alert(1)", 0, true},
		{"fifth redirect", "http://a.example/", "http://b.example/", 4, false},
		{"sixth redirect", "http://a.example/", "http://b.example/", 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := via("http://start.example/")
			for i := 0; i < tc.prior; i++ {
				chain = append(chain, hopRequest(t, "http://mid.example/"))
			}
			chain[len(chain)-1] = hopRequest(t, tc.from)
			err := CheckRedirect(hopRequest(t, tc.to), chain)
			if tc.refused {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCheckRedirectStripsWhatTheLastHopSaid(t *testing.T) {
	req := hopRequest(t, "http://bob:secret@b.example/x")
	req.Header.Set("Referer", "http://a.example/?token=1")
	require.NoError(t, CheckRedirect(req, []*http.Request{hopRequest(t, "http://a.example/")}))
	require.Nil(t, req.URL.User)
	require.Empty(t, req.Header.Get("Referer"))
}

func TestCheckRedirectErrorsAreTold(t *testing.T) {
	loop := make([]*http.Request, maxHops+1)
	for i := range loop {
		loop[i] = hopRequest(t, "http://a.example/")
	}
	err := CheckRedirect(hopRequest(t, "http://a.example/"), loop)
	require.ErrorIs(t, err, ErrTooManyHops)
	class, _ := Classify(err)
	require.Equal(t, ClassRedirectLoop, class)

	err = CheckRedirect(hopRequest(t, "http://a.example/"), []*http.Request{hopRequest(t, "https://a.example/")})
	require.True(t, errors.Is(err, ErrRedirectRefused))
	class, _ = Classify(err)
	require.Equal(t, ClassRedirectLoop, class)
}
