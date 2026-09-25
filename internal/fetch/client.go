package fetch

import (
	"crypto/tls"
	"net"
	"net/http"
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
	Version   string // for the default User-Agent
	PublicURL string // "+url" part of the default User-Agent, omitted when empty

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
	ua  string

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
	ua := "Mozilla/5.0 (compatible; Kipple/" + ver
	if opt.PublicURL != "" {
		ua += "; +" + opt.PublicURL
	}
	ua += ")"
	return &Client{opt: opt, ua: ua, transports: map[variant]*http.Transport{}}
}

// BrowserUserAgent is the common desktop-browser string used for feeds that
// refuse Kipple's own User-Agent (fetch.user_agent_mode), unless a custom
// fetch.user_agent replaces it.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// DefaultUserAgent is the User-Agent used when neither the feed nor the
// user-agent mode overrides it.
func (c *Client) DefaultUserAgent() string { return c.ua }

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
		DisableKeepAlives:      true,
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
func (c *Client) httpClient(v variant, hops *[]Hop) *http.Client {
	return &http.Client{
		Transport: c.transport(v),
		Timeout:   c.opt.ClientTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errTooManyHops
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

// Transport returns the cached guarded transport for a variant, for callers
// outside the feed fetcher that need the same dial-time SSRF guard: the image
// proxy (keyed by its signed flags, design §7.4) and full-text extraction.
// The result is shared and must not be modified.
func (c *Client) Transport(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper {
	return c.transport(variant{noHTTP2: noHTTP2, insecureTLS: insecureTLS, allowPrivate: allowPrivate})
}
