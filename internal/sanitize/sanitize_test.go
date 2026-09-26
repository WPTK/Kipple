package sanitize

import (
	"html"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveURL(t *testing.T) {
	require.Equal(t, "https://a.com/x/y", ResolveURL("y", "https://a.com/x/z"))
	require.Equal(t, "https://a.com/y", ResolveURL("/y", "", "https://a.com/x/"))
	require.Equal(t, "https://b.com/", ResolveURL("https://b.com/"))
	require.Equal(t, "https://a.com/p", ResolveURL("//a.com/p", "https://z.com/"))
	require.Equal(t, "", ResolveURL("y"))
	require.Equal(t, "", ResolveURL("javascript:alert(1)", "https://a.com/"))
	require.Equal(t, "", ResolveURL("data:image/png;base64,AAA", "https://a.com/"))
	require.Equal(t, "", ResolveURL("", "https://a.com/"))
}

func TestPlainText(t *testing.T) {
	require.Equal(t, "a b & c", PlainText("<p>a</p><p>b &amp; c</p>"))
	require.Equal(t, "keep", PlainText("<script>var x=1</script><style>p{}</style>keep"))
	require.Equal(t, "one two", PlainText("one<br>two"))
	require.Equal(t, "", PlainText(""))
}

func TestLeadImage(t *testing.T) {
	base := "https://a.com/post/"
	require.Equal(t, "https://a.com/post/x.jpg", LeadImage(`<p><img src="x.jpg"></p>`, base))
	require.Equal(t, "https://a.com/big.jpg", LeadImage(`<img src="p.gif" width=1 height=1><img src="/big.jpg">`, base))
	require.Equal(t, "https://a.com/lazy.jpg", LeadImage(`<img src="data:image/gif;base64,R0lG" data-src="/lazy.jpg">`, base))
	require.Equal(t, "", LeadImage(`<img src="http://feeds.feedburner.com/~r/x/~4/1">`, base))
	require.Equal(t, "", LeadImage(`<p>no images</p>`, base))
}

func TestContentKeepsFragmentHrefs(t *testing.T) {
	out, _ := Content(`<p>note<a href="#fn1" id="r1">1</a> <a href="#user-content-fn:2">2</a></p><a href="#x y">bad</a><a href="javascript:alert(1)">js</a>`, "https://a.com/post")
	require.Contains(t, out, `href="#fn1"`)
	require.Contains(t, out, `href="#user-content-fn:2"`)
	require.Contains(t, out, `href="#x y"`)
	require.NotContains(t, out, "javascript:")
}

func TestContentKeepsEveryFragmentHref(t *testing.T) {
	for _, frag := range []string{"#footnote's-1", "#a&b", "#f(1)", "#a*b+c,d;e=f@g/h?i", "#", "#00e9t00e9", "#a\"b", "#a<b>"} {
		src := `<p><a href="` + html.EscapeString(frag) + `">n</a></p>`
		out, _ := Content(src, "https://a.com/post")
		require.Contains(t, out, `href="`+html.EscapeString(frag)+`"`, frag)
		// stored form is stable: sanitizing it again changes nothing
		again, _ := Content(out, "https://a.com/post")
		require.Equal(t, out, again, frag)
		// serve time prefixes the target, the stored form does not
		if frag != "#" { // a bare "#" (top of page) has no target to prefix
			require.Contains(t, ServeHTML(out, ServeOptions{}), `href="#kp-`, frag)
		}
	}
}

func TestContentLiteralShieldURLSurvives(t *testing.T) {
	in := `<a href="https://fragment.kipple.invalid/#evil">x</a><a href="https://fragment.kipple.invalid/0">y</a><a href="#real">z</a>`
	out, _ := Content(in, "https://a.com/post")
	require.Contains(t, out, `href="https://fragment.kipple.invalid/#evil"`)
	require.Contains(t, out, `href="https://fragment.kipple.invalid/0"`)
	require.Contains(t, out, `href="#real"`)
}
