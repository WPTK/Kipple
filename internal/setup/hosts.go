package setup

import (
	"fmt"
	"net/netip"
	"strings"
)

// defaultHostSuffixes are the private-use and tailnet name suffixes the Host gate
// accepts without configuration (design 5.2). A DNS-rebinding attack needs a
// name its author controls in public DNS; none of these can be one.
var defaultHostSuffixes = []string{".localhost", ".local", ".lan", ".home.arpa", ".internal", ".ts.net"}

const maxHostLen = 253

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
		if err != nil || !a.Is6() {
			return "", false
		}
		return strings.ToLower(a.String()), true
	}
	if strings.Count(h, ":") > 1 {
		// A bare IPv6 literal (not valid in a Host header, but unambiguous).
		a, err := netip.ParseAddr(h)
		if err != nil {
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

// HostAllowed reports whether a normalized host passes the Host gate: an IP
// literal, localhost and *.localhost, a single-label name, a name under one of
// the private-use or tailnet suffixes, or a match for one of extra (exact
// names, or "*.suffix" for any name under suffix; see CheckHostEntry).
func HostAllowed(host string, extra []string) bool {
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range defaultHostSuffixes {
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
// name or IP address, or "*." followed by a name. No scheme, port, path or
// bare "*".
func CheckHostEntry(e string) (string, error) {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(e)), ".")
	wild := false
	if rest, ok := strings.CutPrefix(s, "*."); ok {
		wild, s = true, rest
	}
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil && !wild {
		return a.String(), nil
	}
	if !validName(s) {
		return "", fmt.Errorf("%q is not a host name (use a name such as rss.example.com or *.example.com, without a scheme or port)", e)
	}
	if wild {
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
