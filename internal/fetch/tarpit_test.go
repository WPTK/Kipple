package fetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgproxy"
)

// A source that stalls (never answers, or answers one byte at a time) holds a
// client for its time limit and no longer. Each client is given a short limit
// and must be back well inside five times that.

const tarpitLimit = 300 * time.Millisecond

type tarpit struct {
	name  string
	serve http.HandlerFunc
}

// tarpits are the two ways to stall: hold the headers back, or send the body a
// byte at a time without end.
func tarpits() []tarpit {
	return []tarpit{
		{"headers never come", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(30 * time.Second):
			}
		}},
		{"body trickles", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			for {
				if _, err := w.Write([]byte(" ")); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}},
	}
}

func tarpitClients(guard *fetch.Client) []stallClient {
	return []stallClient{
		{"fetch", func(t *testing.T, start string) {
			c := fetch.NewClient(fetch.ClientOptions{ClientTimeout: tarpitLimit, HeaderTimeout: tarpitLimit})
			c.Fetch(context.Background(), fetch.Snapshot{ID: 1, URL: start, AllowPrivateNet: true, IntervalS: 1800, DedupMode: fetch.DedupAuto, HonorTTL: true}, time.Now())
		}},
		{"extract", func(t *testing.T, start string) {
			ex := extract.New(extract.Options{Transport: guard.Transport, Timeout: tarpitLimit})
			_, _ = ex.Extract(context.Background(), extract.Target{URL: start, AllowPrivate: true, FeedHost: "127.0.0.1"})
		}},
		{"imgproxy", func(t *testing.T, start string) {
			h := imgproxy.New(imgproxy.Options{Secret: stallSecret, Timeout: tarpitLimit, Transport: func(allowPrivate, insecure bool) http.RoundTripper {
				return guard.Transport(allowPrivate, insecure, false)
			}})
			t.Cleanup(h.Close)
			mux := http.NewServeMux()
			mux.Handle("GET /img/{sig}/{flags}/{u}", h)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, imgproxy.Path(stallSecret, imgproxy.FlagPrivateNet, start), nil))
		}},
	}
}

func TestClientsGiveUpOnAStalledSource(t *testing.T) {
	guard := fetch.NewClient(fetch.ClientOptions{})
	for _, tp := range tarpits() {
		srv := httptest.NewServer(tp.serve)
		t.Cleanup(srv.Close)
		for _, mc := range tarpitClients(guard) {
			t.Run(tp.name+"/"+mc.name, func(t *testing.T) {
				begin := time.Now()
				mc.run(t, srv.URL+"/x")
				require.Less(t, time.Since(begin), 5*tarpitLimit)
			})
		}
	}
}

// The image proxy walks a ladder of request shapes when a source answers 403:
// the time limit covers the whole ladder, not each rung.
func TestImageProxyTimeLimitCoversEveryRetry(t *testing.T) {
	const limit = 600 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(limit / 2) // each rung answers in time; together they do not
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	guard := fetch.NewClient(fetch.ClientOptions{})
	h := imgproxy.New(imgproxy.Options{Secret: stallSecret, Timeout: limit, Transport: func(allowPrivate, insecure bool) http.RoundTripper {
		return guard.Transport(allowPrivate, insecure, false)
	}})
	defer h.Close()
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	rec := httptest.NewRecorder()
	begin := time.Now()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, imgproxy.Path(stallSecret, imgproxy.FlagPrivateNet, srv.URL+"/x.png"), nil))
	require.Less(t, time.Since(begin), limit+limit/4, "three rungs of half the limit would take one and a half limits")
}

var stallSecret = []byte("stall-secret-0123456789abcdefghij")

// A stallClient drives one request chain that starts at start.
type stallClient struct {
	name string
	run  func(t *testing.T, start string)
}
