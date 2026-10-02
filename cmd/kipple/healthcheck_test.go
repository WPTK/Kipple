package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		"":               "http://127.0.0.1:1919/healthz",
		":7080":          "http://127.0.0.1:7080/healthz",
		"0.0.0.0:9090":   "http://127.0.0.1:9090/healthz",
		"[::]:9090":      "http://127.0.0.1:9090/healthz",
		"127.0.0.1:7090": "http://127.0.0.1:7090/healthz",
	} {
		got, err := healthURL(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := healthURL("nonsense")
	require.Error(t, err)
}

func TestProbeHealth(t *testing.T) {
	quiesceHTTP(t)
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	require.NoError(t, probeHealth(addr, time.Second))
	status = http.StatusInternalServerError
	require.ErrorContains(t, probeHealth(addr, time.Second), "500")

	srv.Close()
	require.ErrorContains(t, probeHealth(addr, time.Second), "unhealthy")
}

// The probe keeps no connection open after it returns, even against a server
// that would keep it alive forever: a pooled keep-alive connection's read loop
// calls time.Now, and one left behind raced a later test's change of time.Local
// (issue #165).
func TestProbeHealthLeavesNoConnection(t *testing.T) {
	quiesceHTTP(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	for i := 0; i < 3; i++ {
		require.NoError(t, probeHealth(srv.Listener.Addr().String(), time.Second))
	}
	// The server is still up (an httptest server never times an idle connection
	// out), so only the probe itself can have closed its connections.
	requireNoGoroutines(t, httpClientGoroutine, "the probe left a client connection open")
}

func TestProbeHealthTimeout(t *testing.T) {
	quiesceHTTP(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0") // accepts, never answers
	require.NoError(t, err)
	defer ln.Close()
	start := time.Now()
	require.Error(t, probeHealth(ln.Addr().String(), 300*time.Millisecond))
	require.Less(t, time.Since(start), 2*time.Second)
}

// The probe asks the address the server would listen on: KIPPLE_ADDR, else the
// default (no list of ports to try). Only a Kipple answering "ok" is healthy.
func TestProbeHealthIsOneProbe(t *testing.T) {
	quiesceHTTP(t)
	var hits int32
	mk := func(body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.Listener.Addr().String()
	}
	require.NoError(t, probeHealth(mk("ok"), 2*time.Second))
	require.Equal(t, int32(1), atomic.LoadInt32(&hits))
	require.Error(t, probeHealth(mk("hello"), 2*time.Second), "another program answering 200 is not Kipple")
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	down := closed.Addr().String()
	require.NoError(t, closed.Close())
	require.Error(t, probeHealth(down, time.Second))
}

// A KIPPLE_ADDR naming a host is probed at that host, with Host: 127.0.0.1 so
// the setup- and open-mode Host gate (which may refuse the name with 421) lets
// the probe through. An address given as an IP keeps it as the Host. (Built,
// not sent: a name would need resolving, and localhost stalls on some hosts.)
func TestHealthRequestSendsAnIPHostForANamedAddress(t *testing.T) {
	ctx := context.Background()
	req, err := healthRequest(ctx, "kipple-box:1919")
	require.NoError(t, err)
	require.Equal(t, "kipple-box:1919", req.URL.Host, "the name is dialled")
	require.Equal(t, "127.0.0.1:1919", req.Host)
	for addr, host := range map[string]string{"": "127.0.0.1:1919", "192.168.1.10:9090": "192.168.1.10:9090", "[::1]:9090": "[::1]:9090"} {
		req, err := healthRequest(ctx, addr)
		require.NoError(t, err, addr)
		require.Equal(t, host, req.URL.Host, addr)
		require.Equal(t, host, req.Host, "an IP host is sent as is: %s", addr)
	}
}
