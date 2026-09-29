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
	// A small featured-image crop ahead of the real photo (WordPress "Bones" theme style, issue #71): the
	// larger one wins even though it comes second, so cards don't stretch a 300x100 thumbnail.
	require.Equal(t, "https://a.com/post/full.jpg",
		LeadImage(`<img width="300" height="100" src="crop-300x100.jpg"><p>text</p><img width="1024" height="683" src="full.jpg">`, base))
	// srcset's widest candidate counts too, even with no width attribute of its own.
	require.Equal(t, "https://a.com/post/full.jpg",
		LeadImage(`<img width="150" src="crop.jpg"><img srcset="small.jpg 300w, full.jpg 1200w">`, base))
	// No image names a size anywhere: the first one found still wins, as before.
	require.Equal(t, "https://a.com/post/first.jpg", LeadImage(`<img src="first.jpg"><img src="second.jpg">`, base))
	// An unsized real photo is not beaten by a later, small, sized image (an avatar or share icon):
	// scoring by declared size must not undo more than it fixes.
	require.Equal(t, "https://a.com/post/hero.jpg",
		LeadImage(`<img src="hero.jpg"><p>by</p><img src="avatar.png" width="48" height="48">`, base))
	// A wide, short strip (a leaderboard ad, a divider) does not beat the article's picture, sized or not, but is
	// still used when it is all there is.
	require.Equal(t, "https://a.com/post/hero.jpg",
		LeadImage(`<img src="hero.jpg"><img src="ad.gif" width="728" height="90">`, base))
	require.Equal(t, "https://a.com/post/hero.jpg",
		LeadImage(`<img src="hero.jpg" width="640" height="427"><img src="ad.gif" width="728" height="90">`, base))
	require.Equal(t, "https://a.com/post/ad.gif", LeadImage(`<img src="ad.gif" width="728" height="90">`, base))
	// A real panorama, a little wider than 4:1, still beats a tiny icon; and an absurdly tall declared height
	// cannot overflow the shape check into calling a tall image a banner.
	require.Equal(t, "https://a.com/post/pano.jpg",
		LeadImage(`<img src="pano.jpg" width="2000" height="450"><img src="icon.png" width="16" height="16">`, base))
	require.Equal(t, "https://a.com/post/tall.jpg",
		LeadImage(`<img src="tall.jpg" width="1000" height="3000000000000000000"><img src="small.png" width="50" height="50">`, base))
	// Known trade-off of treating an unsized image as an average one: a large sized image later in the post (a
	// "related article" thumbnail) still wins over an unsized hero.
	require.Equal(t, "https://a.com/post/related.jpg",
		LeadImage(`<img src="hero.jpg"><img src="related.jpg" width="300" height="169">`, base))
	// A srcset URL whose CDN transform contains a comma (Cloudinary/imgix style) is not split apart into a
	// broken URL; the widest real candidate is still picked correctly, in full, with its comma intact.
	require.Equal(t, "https://res.cloudinary.com/x/image/upload/w_1200,c_fill/a.jpg",
		LeadImage(`<img srcset="https://res.cloudinary.com/x/image/upload/w_300,c_fill/a.jpg 300w, https://res.cloudinary.com/x/image/upload/w_1200,c_fill/a.jpg 1200w">`, base))
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
