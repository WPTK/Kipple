package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		"":               "http://127.0.0.1:7080/healthz",
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
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/healthz", r.URL.Path)
		w.WriteHeader(status)
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()

	require.NoError(t, probeHealth(addr, time.Second))
	status = http.StatusInternalServerError
	require.ErrorContains(t, probeHealth(addr, time.Second), "500")

	srv.Close()
	require.ErrorContains(t, probeHealth(addr, time.Second), "unhealthy")
}

func TestProbeHealthTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // accepts, never answers
	require.NoError(t, err)
	defer ln.Close()
	start := time.Now()
	require.Error(t, probeHealth(ln.Addr().String(), 300*time.Millisecond))
	require.Less(t, time.Since(start), 2*time.Second)
}
