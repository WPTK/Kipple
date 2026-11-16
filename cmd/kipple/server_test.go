package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// limConn keeps the TCP behaviour net/http relies on.
var _ interface {
	CloseWrite() error
	ReadFrom(io.Reader) (int64, error)
} = (*limConn)(nil)

func TestServerRefusesOversizedRequestHeaders(t *testing.T) {
	srv := newServer(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ln, err := listen("127.0.0.1:0")
	require.NoError(t, err)
	trackConns(srv, ln)
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

func TestListenAppliesTheConnectionCap(t *testing.T) {
	ln, err := listen("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	l, ok := ln.(*connLimiter)
	require.True(t, ok)
	require.Equal(t, maxConns, l.max)
	require.Equal(t, connReserve, l.reserve)
}

// limitedServer serves behind a connLimiter on loopback. A request for /hold waits until release is
// closed and signals started when it begins; any other path answers at once.
type limitedServer struct {
	t       *testing.T
	l       *connLimiter
	release chan struct{}
	started chan struct{}
}

func newLimitedServer(t *testing.T, limit, reserve int, reserveOK func(net.Addr) bool) *limitedServer {
	t.Helper()
	s := &limitedServer{t: t, release: make(chan struct{}), started: make(chan struct{}, 16)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s.l = newConnLimiter(ln, limit, reserve)
	if reserveOK != nil {
		s.l.reserveOK = reserveOK
	}
	srv := newServer(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			s.started <- struct{}{}
			<-s.release
		}
	}))
	trackConns(srv, s.l)
	go func() { _ = srv.Serve(s.l) }()
	t.Cleanup(func() {
		select {
		case <-s.release:
		default:
			close(s.release)
		}
		_ = srv.Close()
	})
	return s
}

type testConn struct {
	c net.Conn
	r *bufio.Reader
}

func (s *limitedServer) dial() *testConn {
	s.t.Helper()
	c, err := net.Dial("tcp", s.l.Addr().String())
	require.NoError(s.t, err)
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	s.t.Cleanup(func() { _ = c.Close() })
	return &testConn{c: c, r: bufio.NewReader(c)}
}

func (c *testConn) send(path string) {
	_, _ = c.c.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: x\r\n\r\n"))
}

// status reads the next status line; "" means the connection was closed first.
func (c *testConn) status() string {
	line, _ := c.r.ReadString('\n')
	for { // the rest of the headers; the handlers send no body
		h, err := c.r.ReadString('\n')
		if err != nil || strings.TrimSpace(h) == "" {
			break
		}
	}
	return strings.TrimSpace(line)
}

func (s *limitedServer) idleCount() int {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	return len(s.l.idle)
}

func (s *limitedServer) waitStarted(n int) {
	s.t.Helper()
	for range n {
		select {
		case <-s.started:
		case <-time.After(5 * time.Second):
			s.t.Fatal("a request did not reach the handler")
		}
	}
}

func never(net.Addr) bool { return false }

const ok200 = "HTTP/1.1 200 OK"

// A new connection that finds every slot taken evicts the oldest idle one.
func TestFullLimiterEvictsTheOldestIdleConnection(t *testing.T) {
	s := newLimitedServer(t, 3, 0, never)
	a, b := s.dial(), s.dial()
	a.send("/")
	require.Equal(t, ok200, a.status())
	require.Eventually(t, func() bool { return s.idleCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	b.send("/")
	require.Equal(t, ok200, b.status())
	require.Eventually(t, func() bool { return s.idleCount() == 2 }, 5*time.Second, 5*time.Millisecond)
	x := s.dial()
	x.send("/hold")
	s.waitStarted(1) // all three slots are taken: a and b idle, x active

	c := s.dial()
	c.send("/")
	require.Equal(t, ok200, c.status(), "the new connection is served")
	require.Equal(t, "", a.status(), "the oldest idle connection was closed")
	b.send("/")
	require.Equal(t, ok200, b.status(), "the newer idle connection is kept")
}

// Connections with a request in progress are never evicted: a newcomer is refused instead.
func TestFullLimiterNeverEvictsActiveConnections(t *testing.T) {
	s := newLimitedServer(t, 2, 0, never)
	a, b := s.dial(), s.dial()
	a.send("/hold")
	b.send("/hold")
	s.waitStarted(2)

	c := s.dial()
	c.send("/")
	require.Equal(t, "", c.status(), "no slot and nothing idle: refused")
	close(s.release)
	require.Equal(t, ok200, a.status())
	require.Equal(t, ok200, b.status())
}

// A loopback peer (the container's health probe) can use the reserve when ordinary slots are taken.
func TestLoopbackPeerCanUseTheReserve(t *testing.T) {
	s := newLimitedServer(t, 2, 1, nil)
	a, b := s.dial(), s.dial()
	a.send("/hold")
	b.send("/hold")
	s.waitStarted(2)

	c := s.dial()
	c.send("/hold") // the reserve slot
	s.waitStarted(1)
	d := s.dial()
	d.send("/")
	require.Equal(t, "", d.status(), "the reserve is used up too")

	// A peer that may not use the reserve is refused while the ordinary slots are taken.
	s2 := newLimitedServer(t, 2, 1, never)
	e, f := s2.dial(), s2.dial()
	e.send("/hold")
	f.send("/hold")
	s2.waitStarted(2)
	g := s2.dial()
	g.send("/")
	require.Equal(t, "", g.status())
}
