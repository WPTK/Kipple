package fetch

import "net/http"

// Guard returns the guarded transport for a set of per-feed network exceptions
// (Client.Transport).
type Guard func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper

// ScopedTransport is the transport of one feed's request chain that starts at
// an address the owner chose (discovery, the feed fetch). The feed's "allow
// private network" and "allow insecure TLS" exceptions cover requests to
// feedHost and the rest of its site (SameSite, or a subdomain or bare/www.
// twin); every other host, a redirect hop included, goes through the guarded
// default transport. http.Client calls RoundTrip once per hop, so each redirect
// is scoped on its own. With no exception set, or no feed host, it is the
// guarded default alone.
func ScopedTransport(guard Guard, feedHost string, allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
	return scoped(guard, feedHost, allowPrivate, insecureTLS, noHTTP2, func(host string) bool {
		return SameSite(feedHost, host) || FeedHostVariant(feedHost, host)
	})
}

// ContentScopedTransport is ScopedTransport for addresses that come out of the
// feed's own content (article pages, images, icon links): the exceptions cover
// feedHost, its subdomains and its bare/www. twin only (FeedHostVariant), since
// the feed decides what it links to.
func ContentScopedTransport(guard Guard, feedHost string, allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
	return scoped(guard, feedHost, allowPrivate, insecureTLS, noHTTP2, func(host string) bool {
		return FeedHostVariant(feedHost, host)
	})
}

// HasScope reports whether a feed's exceptions give its site a transport of its own: some exception is on and
// there is a host to limit it to. Without one every request gets the plain guarded transport.
func HasScope(feedHost string, allowPrivate, insecureTLS bool) bool {
	return (allowPrivate || insecureTLS) && feedHost != ""
}

func scoped(guard Guard, feedHost string, allowPrivate, insecureTLS, noHTTP2 bool, in func(host string) bool) http.RoundTripper {
	def := guard(false, false, noHTTP2)
	if !HasScope(feedHost, allowPrivate, insecureTLS) {
		return def
	}
	return &hostScoped{in: in, feed: guard(allowPrivate, insecureTLS, noHTTP2), other: def}
}

type hostScoped struct {
	in          func(host string) bool
	feed, other http.RoundTripper
}

func (h *hostScoped) RoundTrip(req *http.Request) (*http.Response, error) {
	if h.in(req.URL.Hostname()) {
		return h.feed.RoundTrip(req)
	}
	return h.other.RoundTrip(req)
}
