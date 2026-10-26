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

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Normalize reads an address as a person types it (see clean: white space, a
// missing scheme, feed: and similar wrappers), lowercases the scheme and host,
// strips the default port and the fragment, and keeps path and query verbatim.
// The result is an absolute http(s) URL.
func Normalize(raw string) (string, error) {
	u, norm, err := parse(raw)
	if err != nil {
		return "", err
	}
	if norm == "" {
		norm = u.String()
	}
	return norm, nil
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
	u, _, err := parse(raw)
	if err != nil {
		return "", err
	}
	return keyOf(u), nil
}

// StoredKey is Key for an address that is already a feed's: it skips the host-name length rule, which came after
// feeds could be saved, so a feed saved with an over-long host name can still be found by its address (to
// unsubscribe it or edit it) though no new one can be added. Every other rule still applies.
func StoredKey(raw string) (string, error) {
	u, _, err := parseHost(raw, false)
	if err != nil {
		return "", err
	}
	return keyOf(u), nil
}

// Host returns the lowercase hostname (no port) of a URL.
func Host(raw string) (string, error) {
	u, _, err := parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

// ErrUserinfo is returned for a URL with a user name or password in it
// (https://user:pass@host/). Credentials would be stored, logged and exported
// with the URL; the feed's HTTP authentication setting is the one place for them.
var ErrUserinfo = errors.New("feedurl: the URL contains a user name or password; set them as the feed's HTTP authentication instead")

// MaxLen is the longest feed URL Kipple keeps, in bytes (the image proxy's cap). It is measured on the
// normalized form, the one that is stored: a fragment and white space around the address are gone, and
// a character outside ASCII is written percent-encoded (up to three bytes per byte). Every function here
// that reads an address (Normalize, Key, KeyAndNormalize, Host) refuses a longer one with ErrTooLong, and a
// host name DNS cannot carry with ErrHostTooLong, so no way in (add, edit, import, sync API, redirect)
// stores one and they all agree on which addresses fit.
const MaxLen = 4096

// maxCleanLen is a loose bound that only spares work on huge input: percent-encoding at most triples
// a byte, so an address longer than this after white space and a fragment are cut cannot fit. The real
// limit is MaxLen, enforced on the normalized form.
const maxCleanLen = 3 * MaxLen

// plainlyShort is the longest cleaned input that cannot reach MaxLen once normalized (three times
// its length plus room for a scheme), so parse need not build the string to measure it.
const plainlyShort = (MaxLen - 16) / 3

// TooLongMessage is the one wording for a refused length, shown to the person (the add dialog, the
// OPML report). The number comes first; the hint is there because an address that looks shorter can be
// over the limit (spaces and letters outside A to Z are stored spelled out).
const TooLongMessage = "the address is longer than 4,096 characters (spaces and letters outside A to Z take more room when it is stored)"

// ErrTooLong is returned for a URL longer than MaxLen once normalized. Its text is for logs; a
// person is shown TooLongMessage.
var ErrTooLong = errors.New("feedurl: address too long")

// Host names are held to what DNS can carry (RFC 1035): at most 253 characters, and 63 between two dots,
// counted in the ASCII form a name outside A to Z is looked up by. A longer one can never be fetched (the
// resolver refuses it), so every function here refuses it with ErrHostTooLong instead of storing it.
const (
	maxHostLen  = 253
	maxLabelLen = 63
)

// HostTooLongMessage is the one wording for a refused host name, shown to the person (the add dialog, the OPML report).
const HostTooLongMessage = "the server name in the address is too long to be real (at most 253 characters, and 63 between dots)"

// ErrHostTooLong is returned for a host name longer than DNS allows. Its text is for logs; a person is shown
// HostTooLongMessage.
var ErrHostTooLong = errors.New("feedurl: host name too long")

// hostFits reports whether host (lowercase, without a port or brackets) is an IP address or a name DNS can carry.
func hostFits(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	// DNS counts the ASCII (punycode) form. A name with no ASCII form cannot be looked up at all, so the fetch says
	// so; measuring its UTF-8 bytes instead would refuse names whose ASCII form fits.
	ascii, err := idna.Punycode.ToASCII(host)
	if err != nil {
		return true
	}
	ascii = strings.TrimSuffix(ascii, ".") // a fully qualified name's root dot is not a character of the name
	if len(ascii) > maxHostLen {
		return false
	}
	for l := range strings.SplitSeq(ascii, ".") {
		if len(l) > maxLabelLen {
			return false
		}
	}
	return true
}

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
	for j := range i {
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

// parse returns the cleaned-up URL and, when it had to build it to measure the length, its string
// form ("" when the input is plainly short; Normalize then builds it once). The length cap is
// measured on that string.
func parse(raw string) (*url.URL, string, error) { return parseHost(raw, true) }

// parseHost is parse; checkHost false leaves out the host-name length rule (StoredKey).
func parseHost(raw string, checkHost bool) (*url.URL, string, error) {
	// Measure what survives cleaning (white space and the fragment are dropped), not the input.
	kept := strings.TrimSpace(raw)
	if i := strings.IndexByte(kept, '#'); i >= 0 {
		kept = kept[:i]
	}
	if len(kept) > maxCleanLen {
		return nil, "", ErrTooLong
	}
	u, err := url.Parse(clean(raw))
	if err != nil {
		return nil, "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, "", errors.New("feedurl: not an absolute http(s) URL")
	}
	if u.User != nil {
		return nil, "", ErrUserinfo
	}
	host := strings.ToLower(u.Hostname())
	if checkHost && !hostFits(host) {
		return nil, "", ErrHostTooLong
	}
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
	if len(kept) <= plainlyShort {
		return u, "", nil
	}
	norm := u.String()
	if len(norm) > MaxLen {
		return nil, "", ErrTooLong
	}
	return u, norm, nil
}

// IsHTTP reports whether u is an absolute http or https URL with a host.
func IsHTTP(u *url.URL) bool {
	return u != nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) && u.Host != ""
}
