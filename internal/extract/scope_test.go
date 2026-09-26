package extract

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func htmlPage(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/a", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(article(longBody())))
	})
	return mux
}

// A feed's allow_private_net covers its own host only: an article link on
// another host that resolves to a private address is refused.
func TestAllowPrivateOnlyForFeedHost(t *testing.T) {
	srv := page(t, htmlPage(t).ServeHTTP)
	e := newExtractor()

	_, err := e.Extract(context.Background(), Target{URL: srv.URL + "/a", AllowPrivate: true, FeedHost: "feed.example.com"})
	var ee *Error
	require.True(t, errors.As(err, &ee), "%v", err)
	require.Contains(t, ee.Msg, "not allowed", "an article off the feed's host gets the guard")

	_, err = e.Extract(context.Background(), Target{URL: srv.URL + "/a", AllowPrivate: true})
	require.Error(t, err, "no feed host: the exception applies nowhere")

	res, err := e.Extract(context.Background(), Target{URL: srv.URL + "/a", AllowPrivate: true, FeedHost: "127.0.0.1"})
	require.NoError(t, err, "the feed's own host keeps its exception")
	require.Greater(t, res.WordCount, 100)
}

// Each redirect hop is checked on its own: a page on the feed's host that
// redirects to another host does not carry the exception there.
func TestAllowPrivateNotCarriedAcrossRedirect(t *testing.T) {
	mux := htmlPage(t)
	var srvURL string
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		// localhost is another host name for the same private address
		http.Redirect(w, r, strings.Replace(srvURL, "127.0.0.1", "localhost", 1)+"/a", http.StatusFound)
	})
	srv := page(t, mux.ServeHTTP)
	srvURL = srv.URL

	_, err := newExtractor().Extract(context.Background(), Target{URL: srv.URL + "/away", AllowPrivate: true, FeedHost: "127.0.0.1"})
	var ee *Error
	require.True(t, errors.As(err, &ee), "%v", err)
	require.Contains(t, ee.Msg, "not allowed")
}

type recRT struct {
	name string
	mu   *sync.Mutex
	log  *[]string
}

func (r recRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	*r.log = append(*r.log, r.name+" "+req.URL.Host)
	r.mu.Unlock()
	return nil, errors.New("recorded")
}

// The transport choice: the exceptions (private net, insecure TLS) go only to
// requests for the feed's host, case-insensitively.
func TestTransportScopedToFeedHost(t *testing.T) {
	var mu sync.Mutex
	var log []string
	var flags []string
	e := New(Options{Transport: func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
		name := "guarded"
		if allowPrivate || insecureTLS {
			name = "exempt"
		}
		mu.Lock()
		flags = append(flags, name)
		mu.Unlock()
		return recRT{name: name, mu: &mu, log: &log}
	}})

	rt := e.transport(Target{InsecureTLS: true, FeedHost: "Feed.Example.com"})
	for _, u := range []string{"https://feed.example.com/a", "https://cdn.example.com/a", "https://FEED.example.com:8443/b"} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		_, _ = rt.RoundTrip(req)
	}
	require.Equal(t, []string{"exempt feed.example.com", "guarded cdn.example.com", "exempt FEED.example.com:8443"}, log)

	log, flags = nil, nil
	_ = e.transport(Target{InsecureTLS: true}) // no feed host
	_ = e.transport(Target{FeedHost: "feed.example.com"})
	require.Equal(t, []string{"guarded", "guarded"}, flags, "without a feed host or an exception only the guarded transport is built")
}
