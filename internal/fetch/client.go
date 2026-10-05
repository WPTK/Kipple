package fetch

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxBodyBytes = 10 << 20
	maxHops      = 5

	acceptHeader = "application/rss+xml, application/atom+xml, application/feed+json, application/json;q=0.9, " +
		"application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8"
)

// ClientOptions configure NewClient. Zero timeouts take the design values.
type ClientOptions struct {
	Version   string        // for the default User-Agent
	PublicURL func() string // the "+url" part of the default User-Agent (the public URL in force); nil or "" leaves it out

	DialTimeout     time.Duration // 10 s
	TLSTimeout      time.Duration // 10 s
	HeaderTimeout   time.Duration // 15 s
	ClientTimeout   time.Duration // 20 s total, including the body
	MaxResponseBody int64         // 10 MiB, decoded stream
}

// variant selects one of the cached transports.
type variant struct{ noHTTP2, insecureTLS, allowPrivate bool }

// Client is the guarded feed HTTP client: one lazily-built transport per
// variant (design §4.4).
type Client struct {
	opt ClientOptions
	ua  string // the default User-Agent up to the public URL

	mu         sync.Mutex
	transports map[variant]*http.Transport
}

// NewClient builds a Client.
func NewClient(opt ClientOptions) *Client {
	if opt.DialTimeout == 0 {
		opt.DialTimeout = 10 * time.Second
	}
	if opt.TLSTimeout == 0 {
		opt.TLSTimeout = 10 * time.Second
	}
	if opt.HeaderTimeout == 0 {
		opt.HeaderTimeout = 15 * time.Second
	}
	if opt.ClientTimeout == 0 {
		opt.ClientTimeout = 20 * time.Second
	}
	if opt.MaxResponseBody == 0 {
		opt.MaxResponseBody = maxBodyBytes
	}
	ver := opt.Version
	if ver == "" {
		ver = "dev"
	}
	return &Client{opt: opt, ua: "Mozilla/5.0 (compatible; Kipple/" + ver, transports: map[variant]*http.Transport{}}
}

// BrowserUserAgent is the common desktop-browser string used for feeds that
// refuse Kipple's own User-Agent (fetch.user_agent_mode), unless a custom
// fetch.user_agent replaces it.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// DefaultUserAgent is the User-Agent used when neither the feed nor the
// user-agent mode overrides it. It names the public URL in force, so it is read
// per request (cheap) rather than kept.
func (c *Client) DefaultUserAgent() string {
	if c.opt.PublicURL != nil {
		if u := c.opt.PublicURL(); u != "" {
			return c.ua + "; +" + u + ")"
		}
	}
	return c.ua + ")"
}

func (c *Client) transport(v variant) *http.Transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.transports[v]; ok {
		return t
	}
	d := &net.Dialer{Timeout: c.opt.DialTimeout, Control: ssrfControl(v.allowPrivate)}
	t := &http.Transport{
		// No Proxy: feed fetches never go through an environment proxy, which
		// would defeat the dial-time address guard.
		DialContext:            d.DialContext,
		TLSHandshakeTimeout:    c.opt.TLSTimeout,
		ResponseHeaderTimeout:  c.opt.HeaderTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      !v.noHTTP2,
		// Connections are reused (an image grid or a run of articles from one host shares one),
		// but never held long: the pool is small and idle ones close after 30 s. A reused
		// connection keeps the address the dial guard already approved.
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 2,
	}
	if v.noHTTP2 {
		t.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	if v.insecureTLS {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- deliberate per-feed opt-in for self-signed feeds (feeds.insecure_tls)
	}
	c.transports[v] = t
	return t
}

// CloseIdle releases idle connections of every transport.
func (c *Client) CloseIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.transports {
		t.CloseIdleConnections()
	}
}

// httpClient returns a client for one attempt; hops collects the redirects.
// feed is the feed's own URL: the Authorization header (feeds.http_auth) is
// dropped from every hop authAllowed refuses. net/http strips it only on a move
// to an unrelated host; it keeps it for a subdomain (so does authAllowed) and
// for an https -> http downgrade, which would send the password in clear text
// (authAllowed does not).
func (c *Client) httpClient(v variant, hops *[]Hop, feed *url.URL) *http.Client {
	var tr http.RoundTripper = c.transport(v)
	if v.allowPrivate || v.insecureTLS {
		// The feed's network exceptions cover its own site only (the hosts a
		// permanent redirect may migrate it to with its settings kept): a hop to
		// any other host goes through the guarded transport.
		guarded := c.transport(variant{noHTTP2: v.noHTTP2})
		if feed == nil {
			tr = guarded
		} else {
			tr = &siteScoped{host: feed.Hostname(), granted: tr, guarded: guarded}
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   c.opt.ClientTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errTooManyHops
			}
			if !authAllowed(feed, req.URL) {
				req.Header.Del("Authorization")
			}
			prev := via[len(via)-1]
			status := 0
			if req.Response != nil {
				status = req.Response.StatusCode
			}
			*hops = append(*hops, Hop{Status: status, From: prev.URL.String(), To: req.URL.String()})
			return nil
		},
	}
}

// siteScoped sends a request for a host on the feed's own site (SameSite, or a
// FeedHostVariant) through granted and every other request through guarded.
// http.Client calls RoundTrip once per hop, redirects included.
type siteScoped struct {
	host             string
	granted, guarded http.RoundTripper
}

func (s *siteScoped) RoundTrip(req *http.Request) (*http.Response, error) {
	h := req.URL.Hostname()
	if SameSite(s.host, h) || FeedHostVariant(s.host, h) {
		return s.granted.RoundTrip(req)
	}
	return s.guarded.RoundTrip(req)
}

// authAllowed reports whether a request to target may carry the feed's HTTP
// credentials: only to the feed's own host or a subdomain of it (the net/http
// rule, so example.com -> www.example.com keeps working), and only over https
// unless the feed URL itself is plain http (the user chose clear text).
func authAllowed(feed, target *url.URL) bool {
	if feed == nil || target == nil || !SameOrSubdomain(feed.Hostname(), target.Hostname()) {
		return false
	}
	return strings.EqualFold(target.Scheme, "https") || strings.EqualFold(feed.Scheme, "http")
}

// Transport returns the cached guarded transport for a variant, for callers
// outside the feed fetcher that need the same dial-time SSRF guard: the image
// proxy (keyed by its signed flags, design §7.4) and full-text extraction.
// The result is shared and must not be modified.
func (c *Client) Transport(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
	return c.transport(variant{noHTTP2: noHTTP2, insecureTLS: insecureTLS, allowPrivate: allowPrivate})
}
