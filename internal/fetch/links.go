package fetch

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// maxFeedLinks bounds how many feed links one page may offer.
const maxFeedLinks = 20

// FeedLink is one feed a web page advertises with <link rel="alternate">.
type FeedLink struct {
	URL   string // absolute, resolved against the page's base URL
	Title string
	Type  string // rss | atom | json
}

// LooksHTML reports whether a response body is a web page rather than a feed. It decides by the
// body, not the Content-Type: plenty of servers label a feed text/html. After a byte order mark,
// white space, comments and an XML declaration (XHTML), a page starts with <!doctype html>,
// <html> or <head>.
func LooksHTML(b []byte) bool {
	s := strings.ToLower(string(b[:min(len(b), 4096)]))
	s = strings.TrimPrefix(s, "\ufeff")
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "<!--"):
			i := strings.Index(s, "-->")
			if i < 0 {
				return false
			}
			s = s[i+3:]
		case strings.HasPrefix(s, "<?xml"):
			i := strings.Index(s, "?>")
			if i < 0 {
				return false
			}
			s = s[i+2:]
		default:
			return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html") || strings.HasPrefix(s, "<head")
		}
	}
}

// FeedLinks returns the feeds a page links in its <head>, in document order, resolved against the
// page's base URL: the first <base href> (itself resolved against base, the page's final URL), as
// in a browser, else base. The order is the page's own: sites list their main feed first and
// comment or category feeds after it, which is why the scheduler's discovery takes the first one.
// Only the types the feed parser reads count: RSS, Atom and JSON Feed (application/feed+json; a
// plain application/json alternate is an API, not a feed). Callers validate each URL for their own
// use.
func FeedLinks(body []byte, base string) []FeedLink {
	b, err := url.Parse(base)
	if err != nil {
		return nil
	}
	baseSet := false
	var out []FeedLink
	z := html.NewTokenizer(strings.NewReader(string(body)))
	for len(out) < maxFeedLinks {
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
		if (string(name) != "link" && string(name) != "base") || !hasAttr {
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
		if string(name) == "base" {
			if href := strings.TrimSpace(attrs["href"]); href != "" && !baseSet {
				if ref, err := url.Parse(href); err == nil {
					b, baseSet = b.ResolveReference(ref), true
				}
			}
			continue
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
		case "application/feed+json":
			kind = "json"
		default:
			continue
		}
		href := strings.TrimSpace(attrs["href"])
		ref, err := url.Parse(href)
		if err != nil || href == "" {
			continue
		}
		out = append(out, FeedLink{URL: b.ResolveReference(ref).String(), Title: strings.TrimSpace(attrs["title"]), Type: kind})
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
