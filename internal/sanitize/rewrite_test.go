package sanitize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestServeHTMLImages covers the image-URL rewrite of ServeHTML (design §7.4):
// img[src], img/source[srcset] candidates and video[poster].
func TestServeHTMLImages(t *testing.T) {
	fn := func(u string) string { return "/p?" + u }
	cases := []struct{ name, in, want string }{
		{"img src", `<p><img src="http://a/1.png" alt="x"></p>`, `<p><img src="/p?http://a/1.png" alt="x"></p>`},
		{"srcset candidates keep descriptors", `<img src="http://a/1.png" srcset="http://a/1.png 1x, http://a/2.png 2x">`,
			`<img src="/p?http://a/1.png" srcset="/p?http://a/1.png 1x, /p?http://a/2.png 2x">`},
		{"picture source", `<picture><source srcset="http://a/x.avif 480w, http://a/y.avif 800w" type="image/avif"><img src="http://a/f.jpg"></picture>`,
			`<picture><source srcset="/p?http://a/x.avif 480w, /p?http://a/y.avif 800w" type="image/avif"><img src="/p?http://a/f.jpg"></picture>`},
		{"video poster only", `<video src="https://a/v.mp4" poster="http://a/p.jpg"><source src="https://a/v.webm"></video>`,
			`<video src="https://a/v.mp4" poster="/p?http://a/p.jpg" preload="none" controls=""><source src="https://a/v.webm"></video>`},
		{"other URLs untouched", `<p><img src="http://a/i.png"><a href="#top">t</a></p>`,
			`<p><img src="/p?http://a/i.png"><a href="#kp-top">t</a></p>`},
		{"untouched markup is byte-identical", `<p class=x>a &amp; b<br/></p>`, `<p class=x>a &amp; b<br/></p>`},
		{"uppercase attribute", `<IMG SRC="http://a/1.png">`, `<img src="/p?http://a/1.png">`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, ServeHTML(c.in, ServeOptions{Image: fn}), c.name)
	}
	// A candidate fn rejects is dropped from a srcset.
	drop := func(u string) string {
		if u == "http://a/2.png" {
			return ""
		}
		return u
	}
	require.Equal(t, `<img srcset="http://a/1.png 1x">`, ServeHTML(`<img srcset="http://a/1.png 1x, http://a/2.png 2x">`, ServeOptions{Image: drop}))
	require.Equal(t, "", ServeHTML("", ServeOptions{Image: fn}))
	require.Equal(t, "<p>x</p>", ServeHTML("<p>x</p>", ServeOptions{}))
}
