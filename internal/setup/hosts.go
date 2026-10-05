package setup

import (
	"fmt"
	"net/netip"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// privateHostSuffixes are the name suffixes the Host gate accepts without
// configuration, in setup mode and open mode alike (design 5.2): names no one
// can hold in public DNS. A DNS-rebinding attack from the internet needs a name
// its author controls there. .localhost never leaves the machine (RFC 6761),
// .local is mDNS (RFC 6762), .home.arpa is for home networks (RFC 8375),
// .internal is reserved for private use (ICANN, 2024), .lan is the common
// router default ICANN has never delegated, and a .ts.net name is handed out by
// Tailscale, not registered by the public.
//
// A device on the local network can answer some of these (mDNS, LLMNR or a
// router's DHCP names) and so rebind one to this computer, but open mode
// already admits every device on the local network directly (Gate.OpenRefusal),
// so such a name gives that device nothing it does not have.
var privateHostSuffixes = []string{".localhost", ".local", ".lan", ".home.arpa", ".internal", ".ts.net"}

const maxHostLen = 253

// privateZones are single-label zones used on private networks that are not
// (and are not going to be) public top-level domains: the reserved names of
// RFC 2606 and RFC 6761 plus the long-standing private-use ones ICANN has
// declined to delegate. "*.home" or "*.corp" may be allowed; "*.com" may not.
var privateZones = map[string]bool{
	"home": true, "corp": true, "mail": true, "lan": true, "internal": true, "intranet": true, "private": true,
	"local": true, "localdomain": true, "localhost": true, "test": true, "example": true, "invalid": true,
}

// NormalizeHost turns a Host header value into the bare host the gate judges:
// the port is dropped, brackets come off an IPv6 literal (which is returned in
// canonical form), letters are lowercased and one trailing dot is removed. ok
// is false for an empty or malformed value, including any byte outside
// letters, digits, '.', '-' and '_' in a name (so no percent-encoding, no
// raw Unicode: an internationalized name arrives in its xn-- form).
func NormalizeHost(h string) (string, bool) {
	if h == "" || len(h) > maxHostLen+8 {
		return "", false
	}
	if h[0] == '[' {
		end := strings.IndexByte(h, ']')
		if end < 0 || !validPort(h[end+1:]) {
			return "", false
		}
		a, err := netip.ParseAddr(h[1:end])
		if err != nil || !a.Is6() || a.Zone() != "" { // no browser sends a zone
			return "", false
		}
		return strings.ToLower(a.String()), true
	}
	if strings.Count(h, ":") > 1 {
		// A bare IPv6 literal (not valid in a Host header, but unambiguous).
		a, err := netip.ParseAddr(h)
		if err != nil || a.Zone() != "" {
			return "", false
		}
		return strings.ToLower(a.String()), true
	}
	host := h
	if i := strings.IndexByte(h, ':'); i >= 0 {
		if !validPort(h[i:]) {
			return "", false
		}
		host = h[:i]
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Is4() {
			return "", false // an IPv6 literal needs brackets once a port may follow
		}
		return a.String(), true
	}
	if !validName(host) {
		return "", false
	}
	return host, true
}

// validPort accepts "" or ":" followed by 1 to 5 digits.
func validPort(s string) bool {
	if s == "" {
		return true
	}
	if s[0] != ':' || len(s) < 2 || len(s) > 6 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validName reports a lowercase DNS name: dot-separated labels of 1 to 63
// characters from a-z, 0-9, '-' and '_', at most 253 characters in all.
func validName(s string) bool {
	if s == "" || len(s) > maxHostLen {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// HostAllowed reports whether a normalized host (NormalizeHost) passes the Host
// gate of setup mode and open mode: a name that cannot be resolved from public
// DNS, or one listed in extra. That is an IP literal (a rebinding attack always
// carries a name), a single-label name (localhost, nas), a name under one of
// privateHostSuffixes, or a match for one of extra (exact names, or "*.suffix"
// for any name under suffix; see CheckHostEntry). Every other name is public
// and refused unless listed.
func HostAllowed(host string, extra []string) bool {
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range privateHostSuffixes {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	for _, e := range extra {
		if suf, ok := strings.CutPrefix(e, "*."); ok {
			if strings.HasSuffix(host, "."+suf) {
				return true
			}
		} else if host == e {
			return true
		}
	}
	return false
}

// CheckHostEntry validates one allowed-host entry (KIPPLE_ALLOWED_HOSTS or the
// security.allowed_hosts setting) and returns it normalized: an exact host
// name or IP address, or "*." followed by a name that is not itself a public
// suffix (so "*.example.com" or "*.home", but not "*.com", "*.co.uk" or
// "*.github.io", where anyone can register a name) and does not end in a
// number. No scheme, port, path or bare "*".
func CheckHostEntry(e string) (string, error) {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(e)), ".")
	wild := false
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		wild, s = true, rest
	}
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil && !wild && a.Zone() == "" {
		return a.String(), nil
	}
	if !validName(s) {
		return "", fmt.Errorf("%q is not a host name (use a name such as rss.example.com or *.example.com, without a scheme or port)", e)
	}
	if wild {
		labels := strings.Split(s, ".")
		if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
			return "", fmt.Errorf("%q: a wildcard needs a domain name after *., not an address", e)
		}
		// A public suffix is where anyone can register a name. A single label is
		// accepted only when it is a known private LAN zone: an unlisted one may
		// just be a top-level domain newer than the embedded suffix list.
		if !strings.Contains(s, ".") {
			if !privateZones[s] {
				return "", fmt.Errorf("%q covers a whole top-level domain: list your own domain (*.example.com), or a private zone such as *.home", e)
			}
		} else if ps, _ := publicsuffix.PublicSuffix(s); ps == s {
			return "", fmt.Errorf("%q covers a whole public suffix, where anyone can register a name: list your own domain (*.example.com)", e)
		}
		return "*." + s, nil
	}
	return s, nil
}

// ParseAllowedHosts parses a comma-separated KIPPLE_ALLOWED_HOSTS value.
func ParseAllowedHosts(v string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		e, err := CheckHostEntry(part)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
