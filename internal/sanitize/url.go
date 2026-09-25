package sanitize

import (
	"net/url"
	"strings"
)

// ResolveURL resolves ref against the first usable base and returns an
// absolute http(s) URL, or "" when ref is empty, unparseable, or not http(s)
// (javascript:, data:, mailto: ...). Fragments are kept. A ref that is already
// absolute needs no base.
func ResolveURL(ref string, bases ...string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if !r.IsAbs() {
		var resolved *url.URL
		for _, b := range bases {
			bu, err := url.Parse(strings.TrimSpace(b))
			if err != nil || !bu.IsAbs() || bu.Host == "" {
				continue
			}
			resolved = bu.ResolveReference(r)
			break
		}
		if resolved == nil {
			return ""
		}
		r = resolved
	}
	if (r.Scheme != "http" && r.Scheme != "https") || r.Host == "" {
		return ""
	}
	return r.String()
}
