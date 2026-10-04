package sanitize

import (
	"strings"

	"golang.org/x/net/html"
)

// rewriteImageTag applies fn to the image URLs of one start tag: img[src],
// img[srcset] and source[srcset] candidates, video[poster]. A value fn maps to
// "" is dropped (srcset candidates) or left empty. It reports whether t changed.
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
