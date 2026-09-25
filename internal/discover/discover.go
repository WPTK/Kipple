// Package discover finds the feed behind a URL the user typed (design §4.9,
// "Add feed in the web UI"): the URL itself when it is a feed, otherwise the
// <link rel="alternate"> feed candidates of the page. It fetches through a
// caller-supplied guarded transport, so the dial-time SSRF check applies.
package discover

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

const (
	maxBody       = 10 << 20 // the fetcher's own response limit
	maxRedirects  = 5
	maxCandidates = 20
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

// ErrNoFeed means the page is reachable but advertises no feed.
var ErrNoFeed = errors.New("no feed found at that address")

// ErrTooLarge means the response exceeded the fetcher's size limit.
var ErrTooLarge = fmt.Errorf("the response is larger than %d MiB", maxBody>>20)

// Find fetches raw through rt and reports whether it is a feed or which feeds
// it links to. ctx bounds the whole attempt.
func Find(ctx context.Context, rt http.RoundTripper, userAgent, raw string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml;q=0.9, text/xml;q=0.9, text/html;q=0.8, */*;q=0.5")
	hc := &http.Client{Transport: rt, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		return nil
	}}
	resp, err := hc.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, fmt.Errorf("the server answered HTTP %d", resp.StatusCode)
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

	// Decide by the body, not the Content-Type: plenty of servers label a feed text/html.
	if !looksHTML(body) {
		if _, perr := fetch.ParseFeed(body, fetch.ParseOptions{FeedURL: final, HTTPCharset: charsetOf(ct)}); perr == nil {
			return Result{IsFeed: true, Candidates: []Candidate{{URL: raw, Type: kindOf(ct, body)}}}, nil
		}
	}
	cands := links(body, final, raw)
	if len(cands) == 0 {
		return Result{}, ErrNoFeed
	}
	return Result{Candidates: cands}, nil
}

func charsetOf(ct string) string {
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	return params["charset"]
}

func looksHTML(b []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(b[:min(len(b), 512)])))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
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

// links extracts <link rel="alternate" type=feed> candidates, resolved against
// the page URL and validated like any feed URL. self (the typed URL) is never a candidate.
func links(body []byte, base, self string) []Candidate {
	b, err := url.Parse(base)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Candidate
	z := html.NewTokenizer(strings.NewReader(string(body)))
	for len(out) < maxCandidates {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) == "body" {
			break // feed links live in <head>
		}
		if string(name) != "link" || !hasAttr {
			continue
		}
		attrs := map[string]string{}
		for {
			k, v, more := z.TagAttr()
			attrs[strings.ToLower(string(k))] = string(v)
			if !more {
				break
			}
		}
		if !hasToken(attrs["rel"], "alternate") {
			continue
		}
		kind := ""
		switch strings.ToLower(strings.TrimSpace(strings.SplitN(attrs["type"], ";", 2)[0])) {
		case "application/rss+xml":
			kind = "rss"
		case "application/atom+xml":
			kind = "atom"
		case "application/feed+json", "application/json":
			kind = "json"
		default:
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(attrs["href"]))
		if err != nil || attrs["href"] == "" {
			continue
		}
		abs := b.ResolveReference(ref).String()
		norm, _, _, err := store.ValidateFeedURL(abs, false)
		if err != nil || seen[norm] || norm == self {
			continue
		}
		seen[norm] = true
		out = append(out, Candidate{URL: norm, Title: strings.TrimSpace(attrs["title"]), Type: kind})
	}
	return out
}

func hasToken(list, want string) bool {
	for _, t := range strings.Fields(strings.ToLower(list)) {
		if t == want {
			return true
		}
	}
	return false
}
