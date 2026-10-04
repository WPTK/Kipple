package fetch_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// TestConnectionsAreReused: sequential requests to one host share a connection
// (an image grid, a run of articles), and CloseIdle drops it again.
func TestConnectionsAreReused(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	c := fetch.NewClient(fetch.ClientOptions{})
	rt := c.Transport(true, false, false) // loopback needs the private-net exception
	for range 5 {
		req, err := http.NewRequest(http.MethodGet, srv.URL, http.NoBody)
		require.NoError(t, err)
		resp, err := rt.RoundTrip(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		require.NoError(t, resp.Body.Close())
	}
	require.EqualValues(t, 1, conns.Load(), "five requests, one connection")

	c.CloseIdle()
	req, err := http.NewRequest(http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	require.EqualValues(t, 2, conns.Load(), "CloseIdle dropped the pooled connection")
}

// TestPoolsDoNotCrossTheGuard: a pooled connection is only reused for its own host and port, and the
// guarded transport keeps its own pool, so a connection the private-network exception opened is never
// handed to a request the SSRF guard would have refused.
func TestPoolsDoNotCrossTheGuard(t *testing.T) {
	t.Parallel()
	var conns atomic.Int32
	newServer := func() *httptest.Server {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				conns.Add(1)
			}
		}
		srv.Start()
		t.Cleanup(srv.Close)
		return srv
	}
	a, b := newServer(), newServer() // same host, different ports
	get := func(rt http.RoundTripper, url string) error {
		req, err := http.NewRequest(http.MethodGet, url, http.NoBody)
		require.NoError(t, err)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}

	c := fetch.NewClient(fetch.ClientOptions{})
	private, guarded := c.Transport(true, false, false), c.Transport(false, false, false)
	require.NoError(t, get(private, a.URL))
	require.NoError(t, get(private, b.URL))
	require.EqualValues(t, 2, conns.Load(), "a second port dials a new connection")

	require.Error(t, get(guarded, a.URL), "the guard still refuses loopback with a warm private pool")
	require.EqualValues(t, 2, conns.Load(), "the refused request never reached the server")
}
