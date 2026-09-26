package sanitize

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// ServeOptions configures ServeHTML.
type ServeOptions struct {
	// Image maps an image URL to what the browser should load (the signed proxy,
	// design 7.4); nil leaves images alone.
	Image func(string) string
	// Thumb is Image for embed thumbnails: it must always proxy, because the
	// point of click-to-load is that the browser contacts no third party before
	// the tap. nil means an embed placeholder has no picture.
	Thumb func(string) string
	// StripTracking removes tracking parameters from links (links.strip_tracking).
	StripTracking bool
}

var (
	ytEmbedID    = regexp.MustCompile(`^https://(?:www\.)?(?:youtube\.com|youtube-nocookie\.com)/embed/([A-Za-z0-9_-]+)`)
	vimeoEmbedID = regexp.MustCompile(`^https://player\.vimeo\.com/video/([0-9]+)`)
)

// Elements ServeHTML never emits. Stored content has been through bluemonday, so
// none of these can be there; this is defense in depth for the serve path.
var (
	dropWithContent = map[string]bool{"script": true, "style": true, "template": true, "object": true, "applet": true, "frameset": true}
	dropVoid        = map[string]bool{"embed": true, "base": true, "meta": true, "link": true, "frame": true, "param": true}
)

type mediaState struct {
	name    string
	hasSrc  bool
	kept    int
	dropped []string
}

// ServeHTML is the one serve-time pass over stored article HTML (design 7.8):
//
//   - http(s) links open in a new tab with rel="noopener noreferrer", and lose
//     tracking parameters when StripTracking is set; links with any other scheme
//     than http, https, mailto and tel lose their href;
//   - every id and name gets a "kp-" prefix and in-page links "#x" become "#kp-x"
//     (no DOM clobbering, footnotes keep working); the references to ids follow
//     (headers, for, aria-describedby, aria-labelledby, aria-controls, usemap="#map");
//   - iframes become click-to-load placeholders (YouTube, Vimeo) or vanish;
//   - audio and video get controls and preload="none", lose autoplay, and an
//     http:// source becomes a link (it would be mixed content);
//   - image URLs go through Image.
//
// Stored HTML and Reader API output are never passed through it. It is
// idempotent: its own output comes back unchanged. Markup it does not touch is
// emitted exactly as the tokenizer saw it.
func ServeHTML(src string, opt ServeOptions) string {
	if src == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(src) + 128)
	z := html.NewTokenizer(strings.NewReader(src))

	var (
		skipName  string // element whose content is being dropped
		skipDepth int
		after     string // markup emitted when the skipped element ends
		media     []*mediaState
	)
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// A skipped element that never closed (an unterminated <video>) still
			// owes its fallback link.
			if skipName != "" {
				b.WriteString(after)
			}
			return b.String()
		}
		raw := append([]byte(nil), z.Raw()...)

		if skipName != "" {
			switch tt {
			case html.StartTagToken:
				if z.Token().Data == skipName {
					skipDepth++
				}
			case html.EndTagToken:
				if z.Token().Data == skipName {
					if skipDepth--; skipDepth == 0 {
						skipName = ""
						b.WriteString(after)
						after = ""
					}
				}
			}
			continue
		}

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			switch {
			case t.Data == "iframe":
				b.WriteString(embedPlaceholder(t, opt))
				if tt == html.StartTagToken {
					skipName, skipDepth, after = "iframe", 1, ""
				}
				continue
			case dropVoid[t.Data]:
				continue
			case dropWithContent[t.Data]:
				if tt == html.StartTagToken {
					skipName, skipDepth, after = t.Data, 1, ""
				}
				continue
			case t.Data == "video" || t.Data == "audio":
				if u := attrVal(t, "src"); isHTTP(u) && !isHTTPS(u) {
					if tt == html.StartTagToken {
						skipName, skipDepth = t.Data, 1
						after = mixedContentLink(t.Data, u)
					} else {
						b.WriteString(mixedContentLink(t.Data, u))
					}
					continue
				}
				if tt == html.StartTagToken {
					media = append(media, &mediaState{name: t.Data, hasSrc: attrVal(t, "src") != ""})
				}
			case t.Data == "source" && len(media) > 0:
				m := media[len(media)-1]
				if u := attrVal(t, "src"); isHTTP(u) && !isHTTPS(u) {
					m.dropped = append(m.dropped, u)
					continue
				}
				m.kept++
			}
			if serveTag(&t, opt) {
				b.WriteString(t.String())
			} else {
				b.Write(raw)
			}
		case html.EndTagToken:
			b.Write(raw)
			if n := len(media); n > 0 {
				if z.Token().Data == media[n-1].name {
					m := media[n-1]
					media = media[:n-1]
					if !m.hasSrc && m.kept == 0 && len(m.dropped) > 0 {
						b.WriteString(mixedContentLink(m.name, m.dropped[0]))
					}
				}
			}
		default:
			b.Write(raw)
		}
	}
}

func mixedContentLink(kind, u string) string {
	return `<p><a href="` + html.EscapeString(u) + `" target="_blank" rel="noopener noreferrer">Open ` + kind + `</a></p>`
}

// embedPlaceholder is the click-to-load figure for an iframe, or "" for one that
// is neither YouTube nor Vimeo.
func embedPlaceholder(t html.Token, opt ServeOptions) string {
	src := attrVal(t, "src")
	if m := ytEmbedID.FindStringSubmatch(src); m != nil {
		id := m[1]
		img := ""
		if opt.Thumb != nil {
			orig := "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
			if p := opt.Thumb(orig); p != "" && p != orig {
				img = `<img src="` + html.EscapeString(p) + `" alt="">`
			}
		}
		return `<figure class="kp-embed" data-provider="youtube" data-id="` + id + `">` + img +
			`<a href="https://www.youtube.com/watch?v=` + id + `" target="_blank" rel="noopener noreferrer">Watch on YouTube</a></figure>`
	}
	if m := vimeoEmbedID.FindStringSubmatch(src); m != nil {
		id := m[1]
		return `<figure class="kp-embed" data-provider="vimeo" data-id="` + id + `">` +
			`<a href="https://vimeo.com/` + id + `" target="_blank" rel="noopener noreferrer">Watch on Vimeo</a></figure>`
	}
	return ""
}

// serveTag applies the per-element rules to t and reports whether it changed.
func serveTag(t *html.Token, opt ServeOptions) bool {
	changed := false
	out := t.Attr[:0:0]
	for _, a := range t.Attr {
		key := a.Key
		switch {
		case isEventHandlerAttr(key), key == "style", key == "srcdoc", key == "formaction", key == "action":
			changed = true
			continue
		case key == "id" || key == "name":
			if a.Val != "" && !strings.HasPrefix(a.Val, "kp-") {
				a.Val = "kp-" + a.Val
				changed = true
			}
		case idRefAttrs[key]:
			if nv := prefixIDRefs(a.Val); nv != a.Val {
				a.Val = nv
				changed = true
			}
		case key == "usemap":
			if v := strings.TrimSpace(a.Val); strings.HasPrefix(v, "#") && len(v) > 1 && !strings.HasPrefix(v, "#kp-") {
				a.Val = "#kp-" + v[1:]
				changed = true
			}
		}
		out = append(out, a)
	}
	t.Attr = out

	switch t.Data {
	case "a":
		if serveLink(t, opt) {
			changed = true
		}
	case "video", "audio":
		if setAttr(t, "preload", "none") {
			changed = true
		}
		if setAttr(t, "controls", "") {
			changed = true
		}
		if delAttr(t, "autoplay") {
			changed = true
		}
	}
	if opt.Image != nil && rewriteImageTag(t, opt.Image) {
		changed = true
	}
	return changed
}

// idRefAttrs are the attributes whose value is a space-separated list of ids.
// Ids get a "kp-" prefix, so the references must too or they point at nothing.
var idRefAttrs = map[string]bool{
	"headers": true, "for": true,
	"aria-describedby": true, "aria-labelledby": true, "aria-controls": true,
}

// prefixIDRefs prefixes every token of an id-reference list that lacks "kp-".
// Whitespace between tokens is kept; an unchanged value comes back as is.
func prefixIDRefs(v string) string {
	fields := strings.Fields(v)
	changed := false
	for i, f := range fields {
		if !strings.HasPrefix(f, "kp-") {
			fields[i] = "kp-" + f
			changed = true
		}
	}
	if !changed {
		return v
	}
	return strings.Join(fields, " ")
}

func serveLink(t *html.Token, opt ServeOptions) bool {
	i := attrIndex(*t, "href")
	if i < 0 {
		return false
	}
	v := strings.TrimSpace(t.Attr[i].Val)
	switch {
	case strings.HasPrefix(v, "#"):
		if len(v) > 1 && !strings.HasPrefix(v, "#kp-") {
			t.Attr[i].Val = "#kp-" + v[1:]
			return true
		}
		return false
	case isHTTP(v):
		changed := false
		if opt.StripTracking {
			if nv := StripTracking(v); nv != t.Attr[i].Val {
				t.Attr[i].Val = nv
				changed = true
			}
		}
		if setAttr(t, "target", "_blank") {
			changed = true
		}
		if setAttr(t, "rel", "noopener noreferrer") {
			changed = true
		}
		return changed
	}
	switch scheme(v) {
	case "mailto", "tel":
		return false
	}
	t.Attr = append(t.Attr[:i:i], t.Attr[i+1:]...) // javascript:, data:, vbscript:, relative
	return true
}

// scheme returns the lower-case URL scheme of v the way a browser reads it
// (tabs, newlines and other control characters inside are ignored), or "".
func scheme(v string) string {
	var sb strings.Builder
	for _, r := range v {
		switch {
		case r <= ' ':
			continue
		case r == ':':
			return strings.ToLower(sb.String())
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '+', r == '-', r == '.':
			sb.WriteRune(r)
		default:
			return ""
		}
	}
	return ""
}

func isHTTP(v string) bool  { s := scheme(v); return s == "http" || s == "https" }
func isHTTPS(v string) bool { return scheme(v) == "https" }

func attrIndex(t html.Token, key string) int {
	for i, a := range t.Attr {
		if a.Key == key {
			return i
		}
	}
	return -1
}

func attrVal(t html.Token, key string) string {
	if i := attrIndex(t, key); i >= 0 {
		return strings.TrimSpace(t.Attr[i].Val)
	}
	return ""
}

// setAttr sets key=val, keeping the attribute's place when it exists. It
// reports whether anything changed.
func setAttr(t *html.Token, key, val string) bool {
	if i := attrIndex(*t, key); i >= 0 {
		if t.Attr[i].Val == val {
			return false
		}
		t.Attr[i].Val = val
		return true
	}
	t.Attr = append(t.Attr, html.Attribute{Key: key, Val: val})
	return true
}

func delAttr(t *html.Token, key string) bool {
	i := attrIndex(*t, key)
	if i < 0 {
		return false
	}
	t.Attr = append(t.Attr[:i:i], t.Attr[i+1:]...)
	return true
}

var trackingParams = map[string]bool{
	"fbclid": true, "gclid": true, "dclid": true, "yclid": true, "mc_cid": true, "mc_eid": true,
	"igshid": true, "_hsenc": true, "_hsmi": true, "mkt_tok": true, "oly_anon_id": true,
	"oly_enc_id": true, "vero_id": true, "ref_src": true,
}

func isTracking(key string) bool {
	if u, err := url.QueryUnescape(key); err == nil {
		key = u
	}
	key = strings.ToLower(key)
	return strings.HasPrefix(key, "utm_") || trackingParams[key]
}

// StripTracking removes tracking parameters (utm_*, fbclid, gclid and the rest
// of design 7.8's list) from an http(s) URL. The other parameters keep their
// order and exact spelling, and so does the fragment. Anything else comes back
// unchanged. It is for serving only: stored URLs and item ids are never touched.
func StripTracking(raw string) string {
	if !isHTTP(raw) {
		return raw
	}
	base, frag := raw, ""
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		base, frag = raw[:i], raw[i:]
	}
	q := strings.IndexByte(base, '?')
	if q < 0 {
		return raw
	}
	parts := strings.Split(base[q+1:], "&")
	keep := make([]string, 0, len(parts))
	for _, p := range parts {
		key, _, _ := strings.Cut(p, "=")
		if p != "" && isTracking(key) {
			continue
		}
		keep = append(keep, p)
	}
	if len(keep) == len(parts) {
		return raw
	}
	out := base[:q]
	if rest := strings.Join(keep, "&"); rest != "" {
		out += "?" + rest
	}
	return out + frag
}

// isEventHandlerAttr reports whether an (already lower-cased) attribute name is
// an inline event handler: "on" followed by at least two letters (onclick,
// onerror, onpointerdown). Attributes that merely look similar, like <details
// open>, are not.
func isEventHandlerAttr(key string) bool {
	if len(key) < 4 || !strings.HasPrefix(key, "on") {
		return false
	}
	for _, c := range key[2:] {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
