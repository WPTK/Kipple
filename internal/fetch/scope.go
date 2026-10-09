package fetch

import "net/http"

// Guard returns the guarded transport for a set of per-feed network exceptions
// (Client.Transport).
type Guard func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper

// ScopedTransport is the transport of one feed's request chain outside the
// fetcher (discovery, icons). The feed's "allow private network" and "allow
// insecure TLS" exceptions cover only requests to feedHost itself or a variant
// of it (a subdomain, or the bare/www. twin: FeedHostVariant), the rule
// full-text extraction and the image proxy use; every other host (a site_url
// elsewhere, a CDN icon link, a redirect hop) goes through the guarded default
// transport. http.Client calls RoundTrip once per hop, so each redirect is
// scoped on its own. With no exception set, or no feed host, it is the guarded
// default alone.
func ScopedTransport(guard Guard, feedHost string, allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
	def := guard(false, false, noHTTP2)
	if (!allowPrivate && !insecureTLS) || feedHost == "" {
		return def
	}
	return &hostScoped{host: feedHost, feed: guard(allowPrivate, insecureTLS, noHTTP2), other: def}
}

type hostScoped struct {
	host        string
	feed, other http.RoundTripper
}

func (h *hostScoped) RoundTrip(req *http.Request) (*http.Response, error) {
	if FeedHostVariant(h.host, req.URL.Hostname()) {
		return h.feed.RoundTrip(req)
	}
	return h.other.RoundTrip(req)
}
