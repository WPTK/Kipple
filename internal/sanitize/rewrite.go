package sanitize

import (
	"strings"

	"golang.org/x/net/html"
)

// RewriteImages applies fn to every image URL of an HTML fragment: img[src],
// every img[srcset] and source[srcset] candidate (picture sources) and
// video[poster]. It is a serve-time transform for the signed image proxy
// (design §7.4): stored HTML and the Reader API never see it. A value fn maps
// to "" is dropped (srcset candidates) or left empty. Everything else in the
// markup is emitted exactly as the tokenizer saw it.
func RewriteImages(src string, fn func(string) string) string {
	if src == "" || fn == nil {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + 64)
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := append([]byte(nil), z.Raw()...)
			t := z.Token()
			if rewriteImageTag(&t, fn) {
				b.WriteString(t.String())
			} else {
				b.Write(raw)
			}
		default:
			b.Write(z.Raw())
		}
	}
}

func rewriteImageTag(t *html.Token, fn func(string) string) bool {
	changed := false
	for i, a := range t.Attr {
		key := strings.ToLower(a.Key)
		var nv string
		switch {
		case t.Data == "img" && key == "src", t.Data == "video" && key == "poster":
			nv = fn(a.Val)
		case (t.Data == "img" || t.Data == "source") && key == "srcset":
			nv = mapSrcset(a.Val, fn)
		default:
			continue
		}
		if nv != a.Val {
			t.Attr[i].Val = nv
			changed = true
		}
	}
	return changed
}
