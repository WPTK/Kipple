// Package feedurl normalizes feed URLs and computes the scheme-less key used
// to match subscriptions (design §4.7 FindFeedByURL). It is shared by store,
// fetch and opml.
package feedurl

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/publicsuffix"
)

// Normalize reads an address as a person types it (see clean: white space, a
// missing scheme, feed: and similar wrappers), lowercases the scheme and host,
// strips the default port and the fragment, and keeps path and query verbatim.
// The result is an absolute http(s) URL.
func Normalize(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// KeyAndNormalize returns Normalize(raw) and Key(Normalize(raw)): the key is taken from the
// normalized string, not from the first parse, because the two can differ (a query that ends in
// whitespace before a fragment, or an IPv6 zone that only survives the first parse) and the stored
// key must be the one a lookup of the stored URL computes. It errors when either step does.
func KeyAndNormalize(raw string) (key, norm string, err error) {
	norm, err = Normalize(raw)
	if err != nil {
		return "", "", err
	}
	key, err = Key(norm)
	if err != nil {
		return "", "", err
	}
	return key, norm, nil
}

func keyOf(u *url.URL) string {
	k := u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// Key is host[:port] + path + ("?" + query). The scheme is dropped so http and
// https variants collide by design.
func Key(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	return keyOf(u), nil
}

// Host returns the lowercase hostname (no port) of a URL.
func Host(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

// ErrUserinfo is returned for a URL with a user name or password in it
// (https://user:pass@host/). Credentials would be stored, logged and exported
// with the URL; the feed's HTTP authentication setting is the one place for them.
var ErrUserinfo = errors.New("feedurl: the URL contains a user name or password; set them as the feed's HTTP authentication instead")

// feedSchemes are the pseudo-schemes that wrap a feed address in links meant for feed readers and
// podcast apps: feed://host/path, feed:https://host/path, pcast://, itpc://, podcast://, rss://.
var feedSchemes = map[string]bool{"feed": true, "rss": true, "pcast": true, "itpc": true, "podcast": true}

// clean turns an address as a person types or pastes it into one url.Parse reads as absolute. It
// trims surrounding white space; unwraps a feed pseudo-scheme (feed:https://x is https://x); and
// gives https to an address without a scheme (//host/p, example.com, example.com/feed, a feed
// pseudo-scheme without its own) and http to localhost or an IP address typed without one; and
// repairs http:/host and https:host.
// An address with neither a scheme nor // counts as one only when its first segment is shaped like
// a host (hostShape: localhost, an IP address, or a name under a real top-level domain), so a
// relative path (../feed.xml, index.php?x=1), a file name (feed.xml) or a lone word stays invalid.
// Everything else is left to parse. Every entry point reaches it through parse: subscribe, the web
// dialog, a URL edit, OPML import and the lookups.
func clean(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, ':'); i > 0 && feedSchemes[strings.ToLower(s[:i])] {
		s = s[i+1:]
	}
	switch {
	case strings.HasPrefix(s, "//"):
		return "https:" + s
	case hasScheme(s):
		return s
	}
	// A mistyped http:/host or https:host keeps its scheme.
	if i := strings.IndexByte(s, ':'); i > 0 {
		if sch := strings.ToLower(s[:i]); sch == "http" || sch == "https" {
			return sch + "://" + strings.TrimLeft(s[i+1:], "/")
		}
	}
	first := s
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		first = s[:i]
	}
	switch hostShape(first) {
	case shapeName:
		return "https://" + s
	case shapeLocal:
		// localhost or an IP address cannot have a publicly trusted certificate: plain http.
		return "http://" + s
	}
	return s
}

// The shapes hostShape tells apart.
const (
	shapeNone  = iota // not plainly a host
	shapeName         // a name under a real top-level domain: https
	shapeLocal        // localhost or an IP address: http
)

// hostShape reports whether seg (host[:port], no scheme) is plainly a host: localhost or an IP
// address (IPv6 in brackets), shapeLocal; or dot-separated letter-digit-hyphen labels whose last
// label is a top-level domain in the ICANN section of the public suffix list (com, io, app, uk;
// so foo.github.io and a.co.uk count, nas.lan, index.php and feed.xml do not), shapeName. A
// numeric port may follow.
func hostShape(seg string) int {
	host := seg
	if h, port, err := net.SplitHostPort(seg); err == nil {
		if port == "" || strings.Trim(port, "0123456789") != "" {
			return shapeNone
		}
		host = h
	} else if strings.Contains(seg, ":") && !strings.HasPrefix(seg, "[") {
		return shapeNone
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return shapeLocal
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return shapeLocal
	}
	labels := strings.Split(strings.ToLower(host), ".")
	if len(labels) < 2 {
		return shapeNone
	}
	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return shapeNone
		}
		for _, r := range l {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-') {
				return shapeNone
			}
		}
	}
	// The last label alone: a private-section suffix (github.io, blogspot.com) would otherwise
	// report icann false for the whole name.
	if _, icann := publicsuffix.PublicSuffix(labels[len(labels)-1]); icann {
		return shapeName
	}
	return shapeNone
}

// hasScheme reports whether s starts with scheme "://" (RFC 3986 scheme characters).
func hasScheme(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case j > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

func parse(raw string) (*url.URL, error) {
	u, err := url.Parse(clean(raw))
	if err != nil {
		return nil, err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("feedurl: not an absolute http(s) URL")
	}
	if u.User != nil {
		return nil, ErrUserinfo
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	u.Host = host
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}
