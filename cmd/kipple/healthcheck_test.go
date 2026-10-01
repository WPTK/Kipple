package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
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

// With KIPPLE_ADDR unset the probe tries 1919, then the legacy 7080, then the
// 1138 fallback; a set KIPPLE_ADDR is the only address tried.
func TestHealthAddrsOrder(t *testing.T) {
	require.Equal(t, []string{":1919", ":7080", ":1138"}, healthAddrs(""))
	require.Equal(t, []string{"127.0.0.1:9090"}, healthAddrs("127.0.0.1:9090"))
}

func TestProbeHealthAnyTakesTheFirstHealthy(t *testing.T) {
	quiesceHTTP(t)
	var mu sync.Mutex
	var hits []string
	mk := func(name, body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits = append(hits, name)
			mu.Unlock()
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.Listener.Addr().String()
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hits...)
	}
	other := mk("other", "hello") // another program answering 200 is not Kipple
	kipple := mk("kipple", "ok")
	never := mk("never", "ok")
	// Take the dead port last: a port freed before the servers start can be
	// handed straight back to one of them, making "down" answer as Kipple.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	down := closed.Addr().String()
	require.NoError(t, closed.Close())
	require.NoError(t, probeHealthAny([]string{down, other, kipple, never}, 2*time.Second))
	require.Equal(t, []string{"other", "kipple"}, seen(), "in order, stopping at the first healthy one")
	require.Error(t, probeHealthAny([]string{down, other}, time.Second))
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
