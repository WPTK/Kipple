package sanitize

import (
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
	require.NotContains(t, out, `href="#x y"`)
	require.NotContains(t, out, "javascript:")
}
