// Package extract fetches an article page and pulls out its readable content
// (design §4.3, §7.5): a guarded GET, charset decoding, go-readability v2, then
// the feed content pipeline (absolutize against the article URL, sanitize with
// the feed policy) so extracted HTML is stored under the same rules as feed HTML.
package extract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codeberg.org/readeck/go-readability/v2"
	"golang.org/x/net/html/charset"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sanitize"
)

const (
	defaultTimeout = 15 * time.Second
	defaultMaxBody = 10 << 20
	maxHops        = 5
	maxElems       = 60000 // readability's own guard against pathological DOMs
	maxErrLen      = 300
)

var errTooManyRedirects = errors.New("too many redirects")

// Options configures New.
type Options struct {
	// Transport returns the SSRF-guarded transport for a variant
	// (fetch.Client.Transport).
	Transport func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper
	// UserAgent is the default request User-Agent.
	UserAgent string
	Timeout   time.Duration // default 15 s, the whole exchange
	MaxBody   int64         // default 10 MiB of raw page bytes
}

// Extractor extracts articles. It is safe for concurrent use.
type Extractor struct{ opt Options }

// New builds an Extractor.
func New(opt Options) *Extractor {
	if opt.Timeout <= 0 {
		opt.Timeout = defaultTimeout
	}
	if opt.MaxBody <= 0 {
		opt.MaxBody = defaultMaxBody
	}
	if opt.UserAgent == "" {
		opt.UserAgent = "Mozilla/5.0 (compatible; Kipple)"
	}
	return &Extractor{opt: opt}
}

// Target is one page to extract, with the owning feed's network switches.
type Target struct {
	URL       string
	UserAgent string // resolved for the feed (override, mode, remembered fallback); "" = the extractor default
	// RetryUserAgent, when set, is tried once after a 403/406 or a Cloudflare
	// challenge served as a 503 (fetch.user_agent_mode = browser_on_failure).
	RetryUserAgent string
	AllowPrivate   bool
	InsecureTLS    bool
	NoHTTP2        bool
}

// Result is a successful extraction.
type Result struct {
	HTML      string // sanitized, absolute URLs
	Text      string
	WordCount int
	ImageURL  string // absolute, "" when none
	SourceURL string // the URL after redirects
}

// Error is an extraction failure with a message safe to show the user.
type Error struct {
	Msg string
	// Transient marks a failure worth retrying later (timeout, reset or
	// refused connection, HTTP 5xx, 429 or 408). Everything else (404, 403,
	// not readable, a blocked address, a bad certificate, an unknown host, a
	// redirect loop) is permanent for the page as it stands.
	Transient bool
}

func (e *Error) Error() string { return e.Msg }

func fail(format string, a ...any) error { return failClass(false, format, a...) }

// failTransient is fail for an error that a later attempt may not repeat.
func failTransient(format string, a ...any) error { return failClass(true, format, a...) }

func failClass(transient bool, format string, a ...any) error {
	m := fmt.Sprintf(format, a...)
	if len(m) > maxErrLen {
		m = m[:maxErrLen]
	}
	return &Error{Msg: m, Transient: transient}
}

// Extract fetches t.URL and returns its readable content.
func (e *Extractor) Extract(ctx context.Context, t Target) (Result, error) {
	u, err := url.Parse(strings.TrimSpace(t.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Result{}, fail("the item has no fetchable article URL")
	}
	ctx, cancel := context.WithTimeout(ctx, e.opt.Timeout)
	defer cancel()
	ua := t.UserAgent
	if ua == "" {
		ua = e.opt.UserAgent
	}
	client := &http.Client{
		Transport: e.opt.Transport(t.AllowPrivate, t.InsecureTLS, t.NoHTTP2),
		Timeout:   e.opt.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxHops {
				return errTooManyRedirects
			}
			return nil
		},
	}
	resp, err := e.get(ctx, client, u.String(), ua)
	if err == nil && t.RetryUserAgent != "" && t.RetryUserAgent != ua && fetch.UARefused(resp) {
		resp.Body.Close()
		resp, err = e.get(ctx, client, u.String(), t.RetryUserAgent)
	}
	if err != nil {
		return Result{}, failClass(transientTransport(err), "could not fetch the page: %s", cleanErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
			return Result{}, failTransient("the page answered HTTP %d", resp.StatusCode)
		}
		return Result{}, fail("the page answered HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if mt, _, perr := mime.ParseMediaType(ct); ct != "" && perr == nil && mt != "text/html" && mt != "application/xhtml+xml" {
		return Result{}, fail("the page is %s, not HTML", mt)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, e.opt.MaxBody+1))
	if err != nil {
		return Result{}, failTransient("could not read the page: %s", cleanErr(err))
	}
	if int64(len(raw)) > e.opt.MaxBody {
		return Result{}, fail("the page is larger than %d MiB", e.opt.MaxBody>>20)
	}
	body, err := charset.NewReader(bytes.NewReader(raw), ct)
	if err != nil {
		body = bytes.NewReader(raw)
	}
	final := resp.Request.URL
	p := readability.NewParser()
	p.MaxElemsToParse = maxElems
	art, err := p.Parse(body, final)
	if err != nil {
		return Result{}, fail("no readable content found: %s", cleanErr(err))
	}
	if art.Node == nil {
		return Result{}, fail("no readable content found")
	}
	var buf bytes.Buffer
	if err := art.RenderHTML(&buf); err != nil {
		return Result{}, fail("could not render the article: %s", cleanErr(err))
	}
	// The same pipeline as feed content: absolutize against the article URL
	// (the item link first, then where redirects ended), then the feed policy.
	htmlOut, text := sanitize.Content(buf.String(), u.String(), final.String())
	wc := sanitize.WordCount(text)
	if wc == 0 {
		return Result{}, fail("no readable content found")
	}
	img := sanitize.LeadImage(htmlOut, u.String(), final.String())
	if img == "" {
		img = sanitize.ResolveURL(art.ImageURL(), u.String(), final.String())
	}
	return Result{HTML: htmlOut, Text: text, WordCount: wc, ImageURL: img, SourceURL: final.String()}, nil
}

// transientTransport reports whether a client.Do error may go away on its own.
// It mirrors fetch.Classify: a blocked address (SSRF guard), a certificate that
// does not verify, a host that does not exist and a redirect loop repeat every
// time, so they are permanent; timeouts, resets, refused connections and DNS
// failures other than not-found are transient.
func transientTransport(err error) bool {
	if errors.Is(err, errTooManyRedirects) {
		return false
	}
	switch class, _ := fetch.Classify(err); class {
	case fetch.ClassSSRF, fetch.ClassTLS, fetch.ClassRedirectLoop:
		return false
	case fetch.ClassDNS:
		var dnsErr *net.DNSError
		return !(errors.As(err, &dnsErr) && dnsErr.IsNotFound)
	}
	return true
}

// cleanErr trims a transport error to something readable without file paths.
func cleanErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	m := err.Error()
	if len(m) > 200 {
		m = m[:200]
	}
	return m
}

// get sends one GET for the article with the given User-Agent.
func (e *Extractor) get(ctx context.Context, client *http.Client, u, ua string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html, application/xhtml+xml;q=0.9, */*;q=0.1")
	return client.Do(req)
}
