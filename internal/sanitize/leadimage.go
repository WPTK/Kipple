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

// LeadImage returns the absolute URL of the best real image in an HTML
// fragment, or "". Tracking pixels (width or height <= 2, known beacon URLs)
// and data: URIs are skipped; lazy-load attributes are honoured. Relative URLs
// resolve against the base chain (design decision 31).
//
// A feed's content often leads with a small fixed-size crop (a WordPress
// featured-image thumbnail, say 300x100) before the real photo appears later
// in the same markup at full size. Picking "the first image" then stores the
// crop, which cards later stretch to fill a much larger box and looks blurry.
// So every candidate image is scored by the largest size named for it (a
// `srcset` width descriptor, else the `width`/`height` attribute) and the
// highest-scoring one wins; when no candidate names a size, the first one
// found is kept, exactly as before.
func LeadImage(src string, bases ...string) string {
	type candidate struct {
		url   string
		score int
	}
	var best candidate
	haveBest := false
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if haveBest {
				return best.url
			}
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
			// srcset's widest candidate, if any, is preferred over plain src/lazy attrs: src is often the
			// small default a JS-less client would get, while srcset lists the full range up to the original.
			if u, w := bestSrcset(attrs["srcset"], bases); u != "" {
				if !haveBest || w > best.score {
					best = candidate{url: u, score: w}
					haveBest = true
				}
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
				score := attrWidth(attrs)
				if !haveBest || score > best.score {
					best = candidate{url: abs, score: score}
					haveBest = true
				}
				break
			}
		}
	}
}

// attrWidth is the declared pixel width of an <img>, from its `width` attribute, else its `height`, else 0
// (unknown, so it never outranks a candidate whose size is actually known).
func attrWidth(attrs map[string]string) int {
	if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(attrs["width"]), "px")); err == nil {
		return n
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(attrs["height"]), "px")); err == nil {
		return n
	}
	return 0
}

// bestSrcset resolves the widest usable candidate in a `srcset` attribute (e.g. "a.jpg 300w, b.jpg 1024w"
// -> b.jpg, 1024), skipping trackers and unresolvable URLs. Density descriptors ("2x") say nothing about
// absolute pixel size and are ignored. Returns ("", 0) if srcset is empty or names no usable, sized candidate.
func bestSrcset(srcset string, bases []string) (string, int) {
	bestURL, bestW := "", 0
	for _, part := range strings.Split(srcset, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) != 2 || !strings.HasSuffix(fields[1], "w") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(fields[1], "w"))
		if err != nil || n <= bestW {
			continue
		}
		abs := ResolveURL(fields[0], bases...)
		if abs == "" || isTracker(abs) {
			continue
		}
		bestURL, bestW = abs, n
	}
	return bestURL, bestW
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
