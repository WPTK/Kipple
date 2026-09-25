// Package feedurl normalizes feed URLs and computes the scheme-less key used
// to match subscriptions (design §4.7 FindFeedByURL). It is shared by store,
// fetch and opml.
package feedurl

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// Normalize lowercases the scheme and host, strips the default port and the
// fragment, and keeps path and query verbatim. The result is an absolute
// http(s) URL.
func Normalize(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Key is host[:port] + path + ("?" + query). The scheme is dropped so http and
// https variants collide by design.
func Key(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	k := u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k, nil
}

// Host returns the lowercase hostname (no port) of a URL.
func Host(raw string) (string, error) {
	u, err := parse(raw)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}

func parse(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("feedurl: not an absolute http(s) URL")
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
