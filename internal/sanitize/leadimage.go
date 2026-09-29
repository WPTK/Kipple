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

// assumedWidth stands in for an image's score when nothing names its size. It sits above icon/avatar/share-button
// widths (typically well under 100px) and below a real content photo, so a merely small-but-sized image (a 48px
// avatar) does not automatically outrank an unsized image that is very likely the actual lead photo; two unsized
// images tie, so the first one found wins between them, matching the pre-scoring behavior.
const assumedWidth = 200

// bannerScore ranks a wide, short image below an unsized one (assumedWidth) and below any real photo, but above
// icons and avatars (well under 100px). A declared shape much wider than tall (a 728x90 leaderboard ad, a divider
// strip) is almost never the article's picture, but a genuine panorama is not worth losing to a 16px icon either.
const bannerScore = 100

// bannerRatio is how many times wider than tall a declared size must be to count as a banner.
const bannerRatio = 4

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
	// outranks reports whether a new candidate should replace best, treating an unknown size (0) as
	// assumedWidth rather than 0 so it isn't beaten by any image that merely happens to declare a size.
	outranks := func(score int) bool {
		if !haveBest {
			return true
		}
		effective := func(s int) int {
			if s == 0 {
				return assumedWidth
			}
			return s
		}
		return effective(score) > effective(best.score)
	}
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
				if isBanner(attrs) {
					w = bannerScore
				}
				if outranks(w) {
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
				if isBanner(attrs) {
					score = bannerScore
				}
				if outranks(score) {
					best = candidate{url: abs, score: score}
					haveBest = true
				}
				break
			}
		}
	}
}

// isBanner reports whether both dimensions are declared and the image is more than bannerRatio times wider than
// it is tall (compared as w/ratio > h so a huge height cannot overflow).
func isBanner(attrs map[string]string) bool {
	w, errW := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(attrs["width"]), "px"))
	h, errH := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(attrs["height"]), "px"))
	return errW == nil && errH == nil && h > 0 && w/bannerRatio > h
}

// attrWidth is the declared pixel width of an <img>, from its `width` attribute, else its `height`, else 0
// (unknown; LeadImage treats that as assumedWidth rather than 0 when ranking candidates).
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
// Uses eachSrcsetCandidate (shared with Absolutize's own srcset rewriting) so a comma inside a URL, as in a
// CDN transform like ".../w_300,h_200/a.jpg 300w", is not mistaken for a candidate separator.
func bestSrcset(srcset string, bases []string) (string, int) {
	bestURL, bestW := "", 0
	eachSrcsetCandidate(srcset, func(u, desc string) {
		if !strings.HasSuffix(desc, "w") {
			return
		}
		n, err := strconv.Atoi(strings.TrimSuffix(desc, "w"))
		if err != nil || n <= bestW {
			return
		}
		abs := ResolveURL(u, bases...)
		if abs == "" || isTracker(abs) {
			return
		}
		bestURL, bestW = abs, n
	})
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
