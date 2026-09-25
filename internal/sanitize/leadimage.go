package sanitize

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// trackerHints marks image URLs that are beacons, not artwork.
var trackerHints = []string{
	"feeds.feedburner.com/~r/", "feeds.feedburner.com/~/", "/pixel.", "/pixel/",
	"doubleclick.net", "/wp-includes/images/smilies/", "s.w.org/images/core/emoji",
	"stats.wordpress.com", "/beacon", "/tracking",
}

// lazyAttrs are checked in order when src is missing or a data: placeholder.
var lazyAttrs = []string{"data-src", "data-lazy-src", "data-original", "data-lazy"}

// LeadImage returns the absolute URL of the first real image in an HTML
// fragment, or "". Tracking pixels (width or height <= 2, known beacon URLs)
// and data: URIs are skipped; lazy-load attributes are honoured. Relative URLs
// resolve against the base chain (design decision 31).
func LeadImage(src string, bases ...string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "img" || !hasAttr {
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
			if tinyDim(attrs["width"]) || tinyDim(attrs["height"]) {
				continue
			}
			cands := append([]string{attrs["src"]}, func() []string {
				var l []string
				for _, a := range lazyAttrs {
					l = append(l, attrs[a])
				}
				return l
			}()...)
			for _, c := range cands {
				c = strings.TrimSpace(c)
				if c == "" || strings.HasPrefix(strings.ToLower(c), "data:") {
					continue
				}
				abs := ResolveURL(c, bases...)
				if abs == "" || isTracker(abs) {
					continue
				}
				return abs
			}
		}
	}
}

func tinyDim(v string) bool {
	v = strings.TrimSuffix(strings.TrimSpace(v), "px")
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && n <= 2
}

func isTracker(u string) bool {
	l := strings.ToLower(u)
	for _, h := range trackerHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}
