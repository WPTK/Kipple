// Package discover finds the feed behind a URL the user typed (design §4.9,
// "Add feed in the web UI"): the URL itself when it is a feed, otherwise the
// <link rel="alternate"> feed candidates of the page. It fetches through a
// caller-supplied guarded transport, so the dial-time SSRF check applies. The
// scheduler finds the feed of a page URL stored without this step (a Reader API
// subscribe, an OPML import) with the same link extraction (fetch.FeedLinks).
package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

const (
	maxBody = 10 << 20 // the fetcher's own response limit
)

// Candidate is one feed a page advertises.
type Candidate struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Type  string `json:"type"` // rss | atom | json
}

// Result is what Find learned about a URL.
type Result struct {
	// IsFeed is true when the URL itself parsed as a feed; Candidates then holds just it.
	IsFeed     bool
	Candidates []Candidate
}

// findTimeout bounds one Find, headers and body together, when its caller sets
// no sooner deadline. A variable so tests can shorten it.
var findTimeout = 20 * time.Second

// ErrNoFeed means the address is a web page that links no feed.
var ErrNoFeed = errors.New("that address is a web page, and the page does not link to a feed; look on the site for its feed address (often /feed or /rss.xml)")

// ErrNotFeed means the address answered with something that is neither a feed nor a web page.
var ErrNotFeed = errors.New("that address does not answer with a feed or a web page")

// ErrTooLarge means the response exceeded the fetcher's size limit.
var ErrTooLarge = fmt.Errorf("the response is larger than %d MiB", maxBody>>20)

// ErrUnaskedCoding is a response compressed in a way Kipple did not ask for, so its bytes cannot be read.
var ErrUnaskedCoding = errors.New("the site answered with a compression Kipple did not ask for")

// StatusError is a response with a status other than 2xx.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("the site answered HTTP %d %s", e.Code, http.StatusText(e.Code))
}

// Find fetches raw through rt and reports whether it is a feed or which feeds
// it links to. ctx bounds the whole attempt. When the site refuses userAgent
// (403, 406, or a Cloudflare challenge served as a 503) and retryUA is set and
// different, it asks once more with retryUA, as the feed fetcher does.
// allowPrivate keeps candidates on private addresses (the caller's rt must then
// allow them too).
func Find(ctx context.Context, rt http.RoundTripper, userAgent, retryUA, raw string, allowPrivate bool) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, findTimeout)
	defer cancel()
	hc := &http.Client{Transport: rt, CheckRedirect: fetch.CheckRedirect}
	get := func(ua string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml;q=0.9, text/xml;q=0.9, text/html;q=0.8, */*;q=0.5")
		return hc.Do(req)
	}
	resp, err := get(userAgent)
	if err == nil && retryUA != "" && retryUA != userAgent && fetch.UARefused(resp) {
		resp.Body.Close()
		resp, err = get(retryUA)
	}
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, &StatusError{Code: resp.StatusCode}
	}
	if fetch.UnaskedCoding(resp.Header) {
		return Result{}, ErrUnaskedCoding
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Result{}, err
	}
	if len(body) > maxBody {
		return Result{}, ErrTooLarge
	}
	ct := resp.Header.Get("Content-Type")
	final := resp.Request.URL.String()

	html := fetch.LooksHTML(body)
	if !html {
		if _, perr := fetch.ParseFeed(body, fetch.ParseOptions{FeedURL: final, HTTPCharset: charsetOf(ct)}); perr == nil {
			return Result{IsFeed: true, Candidates: []Candidate{{URL: raw, Type: kindOf(ct, body)}}}, nil
		}
	}
	cands := candidates(body, final, raw, allowPrivate)
	switch {
	case len(cands) > 0:
		return Result{Candidates: cands}, nil
	case html:
		return Result{}, ErrNoFeed
	}
	return Result{}, ErrNotFeed
}

func charsetOf(ct string) string {
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return params["charset"]
}

func kindOf(ct string, body []byte) string {
	l := strings.ToLower(ct)
	head := strings.ToLower(string(body[:min(len(body), 1024)]))
	switch {
	case strings.Contains(l, "json") || strings.HasPrefix(strings.TrimSpace(head), "{"):
		return "json"
	case strings.Contains(l, "atom") || strings.Contains(head, "<feed"):
		return "atom"
	}
	return "rss"
}

// candidates are the page's feed links (fetch.FeedLinks), validated like any feed URL and without
// duplicates. self (the typed URL) is never a candidate.
func candidates(body []byte, base, self string, allowPrivate bool) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, l := range fetch.FeedLinks(body, base) {
		norm, _, _, err := store.ValidateFeedURL(l.URL, allowPrivate)
		if err != nil || seen[norm] || norm == self {
			continue
		}
		seen[norm] = true
		out = append(out, Candidate{URL: norm, Title: l.Title, Type: l.Type})
	}
	return out
}
