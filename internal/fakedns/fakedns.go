// Package fakedns replaces the process resolver with an in-memory one for
// tests, so a test decides what every hostname resolves to without a network.
// It is imported only from _test files.
//
// The SSRF guard checks the address a dial is about to connect to, after DNS.
// To test that, a test needs a hostname that resolves to a chosen address (a
// private one, a mix, one that changes between lookups); this gives it that
// through net.DefaultResolver, which every net.Dialer without its own Resolver
// uses, including the guarded transports.
package fakedns

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Install makes net.DefaultResolver answer from resolve until the test ends.
// resolve receives the lower-case name without its trailing dot and returns
// the addresses to answer with; none means NXDOMAIN. It runs once per query
// type, and the Go resolver also asks for A and AAAA separately, so a function
// that counts calls should count names, not calls. Tests that call Install must
// not run in parallel: the resolver is process-wide.
func Install(t testing.TB, resolve func(name string) []netip.Addr) {
	t.Helper()
	old := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			c, s := net.Pipe()
			go serve(s, resolve)
			return c, nil
		},
	}
	t.Cleanup(func() { net.DefaultResolver = old })
}

// serve answers length-prefixed (TCP framing) DNS queries on c until it closes.
func serve(c net.Conn, resolve func(string) []netip.Addr) {
	defer c.Close()
	for {
		var n [2]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		buf := make([]byte, binary.BigEndian.Uint16(n[:]))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		var q dnsmessage.Message
		if q.Unpack(buf) != nil || len(q.Questions) != 1 {
			return
		}
		resp := answer(q, resolve)
		out, err := resp.Pack()
		if err != nil {
			return
		}
		framed := binary.BigEndian.AppendUint16(nil, uint16(len(out)))
		if _, err := c.Write(append(framed, out...)); err != nil {
			return
		}
	}
}

func answer(q dnsmessage.Message, resolve func(string) []netip.Addr) dnsmessage.Message {
	question := q.Questions[0]
	name := strings.TrimSuffix(strings.ToLower(question.Name.String()), ".")
	addrs := resolve(name)
	r := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true, RecursionAvailable: true},
		Questions: q.Questions,
	}
	if len(addrs) == 0 {
		r.RCode = dnsmessage.RCodeNameError
		return r
	}
	for _, a := range addrs {
		h := dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 0}
		switch {
		case a.Is4() && question.Type == dnsmessage.TypeA:
			h.Type = dnsmessage.TypeA
			r.Answers = append(r.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AResource{A: a.As4()}})
		case a.Is6() && question.Type == dnsmessage.TypeAAAA:
			h.Type = dnsmessage.TypeAAAA
			r.Answers = append(r.Answers, dnsmessage.Resource{Header: h, Body: &dnsmessage.AAAAResource{AAAA: a.As16()}})
		}
	}
	return r
}
