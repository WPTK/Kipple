package main

import (
	"io"
	"net"
	"net/http"
	"sync"
)

const (
	// maxConns is the most connections served at once. Each holds a goroutine and two 4 KiB
	// buffers, so the worst case is about 10 MiB. A reverse proxy holds up to a couple of
	// hundred, a person's browsers and their sync apps a few dozen more, and the 64 event
	// streams fit inside.
	maxConns = 512
	// connReserve is the extra room only loopback peers may use, so the container's own health
	// probe still gets through when every ordinary slot is taken.
	connReserve = 8
)

// connLimiter caps the connections a listener hands to the server. A connection that arrives when
// every slot is taken evicts the oldest idle keep-alive connection; with none idle it is closed at
// once. A connection that turns idle while the limiter is full is closed too, so idle connections
// never hold slots against new ones. Active requests keep their slots until they end or hit the
// server's read timeouts. Call trackConns so the limiter sees which connections are idle.
type connLimiter struct {
	net.Listener
	max, reserve int
	reserveOK    func(net.Addr) bool // who may use the reserve (loopback peers)

	mu   sync.Mutex
	open int
	seq  uint64
	idle map[*limConn]struct{}
}

func newConnLimiter(ln net.Listener, limit, reserve int) *connLimiter {
	return &connLimiter{Listener: ln, max: limit, reserve: reserve, reserveOK: isLoopback, idle: map[*limConn]struct{}{}}
}

func isLoopback(a net.Addr) bool {
	t, ok := a.(*net.TCPAddr)
	return ok && t.IP.IsLoopback()
}

func (l *connLimiter) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		limit := l.max
		if l.reserveOK(c.RemoteAddr()) {
			limit += l.reserve
		}
		l.mu.Lock()
		var victim *limConn
		if l.open >= limit {
			if victim = l.oldestIdleLocked(); victim == nil {
				l.mu.Unlock()
				_ = c.Close() // every slot is busy: refused
				continue
			}
			l.dropLocked(victim)
		}
		l.open++
		lc := &limConn{Conn: c, l: l}
		l.mu.Unlock()
		if victim != nil {
			_ = victim.Conn.Close()
		}
		return lc, nil
	}
}

func (l *connLimiter) oldestIdleLocked() (oldest *limConn) {
	for c := range l.idle {
		if oldest == nil || c.idleSeq < oldest.idleSeq {
			oldest = c
		}
	}
	return oldest
}

// dropLocked takes c out of the count; its Close then changes nothing.
func (l *connLimiter) dropLocked(c *limConn) {
	if !c.gone {
		c.gone = true
		l.open--
	}
	delete(l.idle, c)
}

// connState is the http.Server.ConnState hook that tells the limiter which connections are idle.
func (l *connLimiter) connState(c net.Conn, st http.ConnState) {
	lc, ok := c.(*limConn)
	if !ok {
		return
	}
	l.mu.Lock()
	full := false
	switch st {
	case http.StateIdle:
		if full = l.open >= l.max; !full {
			l.seq++
			lc.idleSeq = l.seq
			l.idle[lc] = struct{}{}
		}
	default:
		delete(l.idle, lc)
	}
	l.mu.Unlock()
	if full {
		_ = lc.Close()
	}
}

// trackConns connects srv to ln when ln is a connLimiter.
func trackConns(srv *http.Server, ln net.Listener) {
	if l, ok := ln.(*connLimiter); ok {
		srv.ConnState = l.connState
	}
}

// limConn is an accepted connection counted by its limiter. It forwards CloseWrite (net/http sends a
// FIN after refusing a request) and ReadFrom (sendfile for cached images and the backup download).
type limConn struct {
	net.Conn
	l       *connLimiter
	idleSeq uint64
	gone    bool
}

func (c *limConn) Close() error {
	err := c.Conn.Close()
	c.l.mu.Lock()
	c.l.dropLocked(c)
	c.l.mu.Unlock()
	return err
}

func (c *limConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *limConn) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := c.Conn.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(struct{ io.Writer }{c.Conn}, r)
}
