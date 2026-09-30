package favicon

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const (
	maxLinks = 32 // icon links collected from one page

	// The preferred band for an icon's largest side, in CSS pixels: big enough
	// for a sharp 16 px slot on a 2x screen and for Reader apps' larger tiles,
	// small enough to stay a few KiB.
	prefMin    = 32
	prefMax    = 180
	prefTarget = 64
	appleSize  = 180 // an apple-touch-icon without sizes is 180x180 by convention
)

// candidate is one icon a page advertises.
type candidate struct {
	URL   string
	Size  int // largest side from sizes="WxH", 0 when unknown
	order int // document order, the last tie-breaker
}

// iconLinks extracts the <link rel="icon" | "shortcut icon" | "apple-touch-icon">
// candidates from the head of an HTML page, resolved against base (and a
// <base href> in the head), and returns them best first. Only absolute http(s)
// URLs survive; SVG (by type or extension) and data: URLs are dropped. It never
// fetches anything, so it is safe to fuzz.
func iconLinks(body []byte, base string) []candidate {
	b, err := url.Parse(base)
	if err != nil || !httpURL(b) {
		return nil
	}
	seen := map[string]bool{}
	var out []candidate
	baseSet := false
	z := html.NewTokenizer(strings.NewReader(string(body)))
	for len(out) < maxLinks {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		tag := string(name)
		if tag == "body" {
			break // icon links live in <head>
		}
		if (tag != "link" && tag != "base") || !hasAttr {
			continue
		}
		attrs := map[string]string{}
		for {
			k, v, more := z.TagAttr()
			key := strings.ToLower(string(k))
			if _, dup := attrs[key]; !dup { // the first of a repeated attribute wins, as in browsers
				attrs[key] = string(v)
			}
			if !more {
				break
			}
		}
		if tag == "base" {
			// Only the first <base href> counts, and only an http(s) one.
			if h := strings.TrimSpace(attrs["href"]); !baseSet && h != "" {
				baseSet = true
				if ref, err := url.Parse(h); err == nil {
					if nb := b.ResolveReference(ref); httpURL(nb) {
						b = nb
					}
				}
			}
			continue
		}
		apple, ok := iconRel(attrs["rel"])
		if !ok || svgType(attrs["type"]) {
			continue
		}
		href := strings.TrimSpace(attrs["href"])
		if href == "" {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		target := b.ResolveReference(ref)
		target.Fragment, target.RawFragment = "", ""
		target.User = nil // a link's credentials are never sent (nor stored)
		if !httpURL(target) || strings.HasSuffix(strings.ToLower(target.Path), ".svg") {
			continue
		}
		u := target.String()
		if seen[u] {
			continue
		}
		seen[u] = true
		size := parseSizes(attrs["sizes"])
		if size == 0 && apple {
			size = appleSize
		}
		out = append(out, candidate{URL: u, Size: size, order: len(out)})
	}
	rank(out)
	return out
}

// iconRel reports whether a rel list names an icon, and whether it is an
// apple-touch-icon. "mask-icon" (always SVG) and "fluid-icon" are not icons here.
func iconRel(rel string) (apple, ok bool) {
	for _, t := range strings.Fields(strings.ToLower(rel)) {
		switch t {
		case "icon":
			ok = true
		case "apple-touch-icon", "apple-touch-icon-precomposed":
			ok, apple = true, true
		}
	}
	return apple, ok
}

// svgType is true for a type attribute that is SVG or not an image at all.
func svgType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(strings.SplitN(t, ";", 2)[0]))
	if t == "" {
		return false
	}
	return strings.Contains(t, "svg") || !strings.HasPrefix(t, "image/")
}

// parseSizes returns the largest side named in a sizes attribute ("16x16
// 32x32", case-insensitive x), or 0 for "any", an empty value or nothing valid.
func parseSizes(s string) int {
	best := 0
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		w, h, ok := strings.Cut(tok, "x")
		if !ok {
			continue
		}
		wi, err1 := strconv.Atoi(w)
		hi, err2 := strconv.Atoi(h)
		if err1 != nil || err2 != nil || wi <= 0 || hi <= 0 || wi > 10000 || hi > 10000 {
			continue
		}
		best = max(best, wi, hi)
	}
	return best
}

// rank sorts candidates best first: a known size inside the preferred band
// (closest to prefTarget), then unknown sizes, then known sizes outside the band
// (closest to it); document order breaks ties.
func rank(cs []candidate) {
	key := func(c candidate) (tier, dist int) {
		switch {
		case c.Size == 0:
			return 1, 0
		case c.Size >= prefMin && c.Size <= prefMax:
			return 0, abs(c.Size - prefTarget)
		case c.Size < prefMin:
			return 2, prefMin - c.Size
		default:
			return 2, c.Size - prefMax
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		ti, di := key(cs[i])
		tj, dj := key(cs[j])
		if ti != tj {
			return ti < tj
		}
		if di != dj {
			return di < dj
		}
		return cs[i].order < cs[j].order
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// httpURL reports whether u is an absolute http(s) URL with a host, and still
// is once written out and parsed back: ResolveReference (and a lenient Parse of
// a scheme-relative href) can yield a URL whose string does not parse, such as
// the unbracketed host in "http://::" (#157), and a candidate is fetched from
// its string.
func httpURL(u *url.URL) bool {
	if !isHTTP(u) {
		return false
	}
	back, err := url.Parse(u.String())
	return err == nil && isHTTP(back)
}

func isHTTP(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
