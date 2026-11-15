package main

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerRefusesOversizedRequestHeaders(t *testing.T) {
	srv := newServer(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	status := func(header string) string {
		c, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nX-Pad: " + header + "\r\nConnection: close\r\n\r\n"))
		line, _ := bufio.NewReader(c).ReadString('\n')
		return strings.TrimSpace(line)
	}
	require.Equal(t, "HTTP/1.1 200 OK", status(strings.Repeat("a", 32<<10)))
	require.Equal(t, "HTTP/1.1 431 Request Header Fields Too Large", status(strings.Repeat("a", 100<<10)))
}

func TestListenerServesAtMostTheCapAtOnce(t *testing.T) {
	ln, err := listenLimited("127.0.0.1:0", 2)
	require.NoError(t, err)
	srv := newServer(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	firstLine := func(c net.Conn) <-chan string {
		_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
		out := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(c).ReadString('\n')
			out <- strings.TrimSpace(line)
		}()
		return out
	}
	dial := func() net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		require.NoError(t, err)
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	a, b := dial(), dial()
	require.Equal(t, "HTTP/1.1 200 OK", <-firstLine(a))
	require.Equal(t, "HTTP/1.1 200 OK", <-firstLine(b))

	third := firstLine(dial())
	select {
	case line := <-third:
		t.Fatalf("a third connection was served while two were open: %q", line)
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, a.Close())
	select {
	case line := <-third:
		require.Equal(t, "HTTP/1.1 200 OK", line)
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting connection was not served after a slot freed")
	}
}
