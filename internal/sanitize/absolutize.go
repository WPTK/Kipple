package sanitize

import (
	"strings"

	"golang.org/x/net/html"
)

// urlAttrs lists the URL-bearing attributes per element that Absolutize
// resolves (design §4.4 step 2). srcset is handled separately.
var urlAttrs = map[string][]string{
	"a":      {"href"},
	"img":    {"src"},
	"video":  {"poster", "src"},
	"audio":  {"src"},
	"source": {"src"},
	"iframe": {"src"},
}

// Absolutize resolves every URL attribute in an HTML fragment against the
// base chain (first usable base wins, see ResolveURL). A value that cannot be
// resolved to an absolute http(s) URL is dropped, except fragment-only hrefs
// ("#fn1") and mailto:/tel: hrefs, which are kept verbatim. Markup that is not
// touched is emitted by the tokenizer unchanged.
func Absolutize(src string, bases ...string) string {
	if src == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(src) + 64)
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := append([]byte(nil), z.Raw()...)
			t := z.Token()
			if rewriteTag(&t, bases) {
				b.WriteString(t.String())
			} else {
				b.Write(raw)
			}
		default:
			b.Write(z.Raw())
		}
	}
}

// rewriteTag mutates t's attributes; it reports whether anything changed.
func rewriteTag(t *html.Token, bases []string) bool {
	names := urlAttrs[t.Data]
	isSrcset := t.Data == "img" || t.Data == "source"
	if len(names) == 0 && !isSrcset {
		return false
	}
	changed := false
	out := t.Attr[:0:0]
	for _, a := range t.Attr {
		key := strings.ToLower(a.Key)
		switch {
		case contains(names, key):
			nv, keep := resolveAttr(t.Data, key, a.Val, bases)
			if !keep {
				changed = true
				continue
			}
			if nv != a.Val {
				changed = true
			}
			a.Val = nv
		case isSrcset && key == "srcset":
			nv := resolveSrcset(a.Val, bases)
			if nv == "" {
				changed = true
				continue
			}
			if nv != a.Val {
				changed = true
			}
			a.Val = nv
		}
		out = append(out, a)
	}
	t.Attr = out
	return changed
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func resolveAttr(tag, key, val string, bases []string) (string, bool) {
	v := strings.TrimSpace(val)
	if tag == "a" && key == "href" {
		l := strings.ToLower(v)
		if strings.HasPrefix(v, "#") || strings.HasPrefix(l, "mailto:") || strings.HasPrefix(l, "tel:") {
			return v, true
		}
	}
	abs := ResolveURL(v, bases...)
	return abs, abs != ""
}

// resolveSrcset resolves each candidate URL of a srcset value and drops the
// candidates that do not resolve. Descriptors ("2x", "480w") are preserved.
func resolveSrcset(v string, bases []string) string {
	return mapSrcset(v, func(u string) string { return ResolveURL(u, bases...) })
}

// mapSrcset applies fn to each candidate URL of a srcset value and drops the
// candidates for which it returns "". Descriptors ("2x", "480w") are preserved.
func mapSrcset(v string, fn func(string) string) string {
	var out []string
	eachSrcsetCandidate(v, func(u, desc string) {
		abs := fn(u)
		if abs == "" {
			return
		}
		if desc != "" {
			abs += " " + desc
		}
		out = append(out, abs)
	})
	return strings.Join(out, ", ")
}

// eachSrcsetCandidate calls fn(url, descriptor) for each candidate in a srcset
// value. A comma inside a URL is tolerated: only a comma after whitespace, or
// a trailing comma on the URL token, ends a candidate (so a CDN transform URL
// like ".../w_300,h_200/a.jpg 300w" is not split apart).
func eachSrcsetCandidate(v string, fn func(url, desc string)) {
	i := 0
	for i < len(v) {
		for i < len(v) && (v[i] == ',' || isSpace(v[i])) {
			i++
		}
		start := i
		for i < len(v) && !isSpace(v[i]) {
			i++
		}
		u := v[start:i]
		desc := ""
		if strings.HasSuffix(u, ",") {
			u = strings.TrimRight(u, ",")
		} else {
			ds := i
			for i < len(v) && v[i] != ',' {
				i++
			}
			desc = strings.TrimSpace(v[ds:i])
		}
		if u == "" {
			continue
		}
		fn(u, desc)
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
