package fetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/favicon"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgproxy"
)

// Every outbound client follows one redirect policy: http and https only, no
// step from https down to http, and nothing about the previous hop (a Referer,
// credentials in the Location) is carried to the next. The matrix drives each
// client through its public entry point against local servers and looks at what
// the second server received.

var redirectSecret = []byte("matrix-secret-0123456789abcdef")

const redirectFeed = `<?xml version="1.0"?><rss version="2.0"><channel><title>t</title><link>http://x/</link><description>d</description>` +
	`<item><title>one</title><link>http://x/1</link><guid>1</guid></item></channel></rss>`

// sink records the requests a server received.
type redirectSink struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (s *redirectSink) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, r)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(redirectFeed))
}

func (s *redirectSink) got() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.reqs...)
}

// A client drives one request chain that starts at start.
type matrixClient struct {
	name string
	run  func(t *testing.T, start string)
}

func matrixClients() []matrixClient {
	guard := fetch.NewClient(fetch.ClientOptions{})
	ctx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	}
	return []matrixClient{
		{"fetch", func(t *testing.T, start string) {
			c, cancel := ctx()
			defer cancel()
			snap := fetch.Snapshot{ID: 1, URL: start, AllowPrivateNet: true, AllowInsecureTLS: true, IntervalS: 1800, DedupMode: fetch.DedupAuto, HonorTTL: true}
			guard.Fetch(c, snap, time.Now())
		}},
		{"discover", func(t *testing.T, start string) {
			c, cancel := ctx()
			defer cancel()
			_, _ = discover.Find(c, guard.Transport(true, true, false), "ua", "", start, true)
		}},
		{"extract", func(t *testing.T, start string) {
			c, cancel := ctx()
			defer cancel()
			ex := extract.New(extract.Options{Transport: guard.Transport})
			_, _ = ex.Extract(c, extract.Target{URL: start, AllowPrivate: true, InsecureTLS: true, FeedHost: "127.0.0.1"})
		}},
		{"favicon", func(t *testing.T, start string) {
			c, cancel := ctx()
			defer cancel()
			_, _ = favicon.Lookup(c, favicon.Request{SiteURL: start, Transport: fetch.ScopedTransport(guard.Transport, "127.0.0.1", true, true, false), UserAgent: "ua"})
		}},
		{"imgproxy", func(t *testing.T, start string) {
			h := imgproxy.New(imgproxy.Options{Secret: redirectSecret, Transport: func(allowPrivate, insecure bool) http.RoundTripper {
				return guard.Transport(allowPrivate, insecure, false)
			}})
			t.Cleanup(h.Close)
			mux := http.NewServeMux()
			mux.Handle("GET /img/{sig}/{flags}/{u}", h)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, imgproxy.Path(redirectSecret, imgproxy.FlagPrivateNet|imgproxy.FlagInsecureTLS, start), nil)
			mux.ServeHTTP(rec, req)
		}},
	}
}

// A page served over https never sends a client on to plain http.
func TestClientsRefuseARedirectFromHTTPSToHTTP(t *testing.T) {
	for _, mc := range matrixClients() {
		t.Run(mc.name, func(t *testing.T) {
			plain := &redirectSink{}
			ps := httptest.NewServer(http.HandlerFunc(plain.handler))
			defer ps.Close()
			ss := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, ps.URL+"/next", http.StatusFound)
			}))
			defer ss.Close()
			mc.run(t, ss.URL+"/start")
			require.Empty(t, plain.got(), "the plain-http server must not be asked")
		})
	}
}

// A move from http to https is still followed.
func TestClientsFollowARedirectFromHTTPToHTTPS(t *testing.T) {
	for _, mc := range matrixClients() {
		t.Run(mc.name, func(t *testing.T) {
			secure := &redirectSink{}
			ss := httptest.NewTLSServer(http.HandlerFunc(secure.handler))
			defer ss.Close()
			ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, ss.URL+"/next", http.StatusFound)
			}))
			defer ps.Close()
			mc.run(t, ps.URL+"/start")
			require.NotEmpty(t, secure.got(), "the https server is asked")
		})
	}
}

// Nothing about the first server travels to the second: no Referer, and the
// user name and password of a Location never become an Authorization header.
func TestClientsCarryNothingAcrossARedirect(t *testing.T) {
	for _, mc := range matrixClients() {
		t.Run(mc.name, func(t *testing.T) {
			next := &redirectSink{}
			ns := httptest.NewServer(http.HandlerFunc(next.handler))
			defer ns.Close()
			ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, strings.Replace(ns.URL, "http://", "http://bob:secret@", 1)+"/next?k=1", http.StatusFound)
			}))
			defer ps.Close()
			mc.run(t, ps.URL+"/start?token=abc")
			reqs := next.got()
			require.NotEmpty(t, reqs, "the redirect is followed")
			for _, r := range reqs {
				require.Empty(t, r.Header.Get("Referer"), "no Referer")
				require.Empty(t, r.Header.Get("Authorization"), "no credentials")
			}
		})
	}
}
