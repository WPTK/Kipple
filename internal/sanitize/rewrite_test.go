package sanitize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteImages(t *testing.T) {
	fn := func(u string) string { return "/p?" + u }
	cases := []struct{ name, in, want string }{
		{"img src", `<p><img src="http://a/1.png" alt="x"></p>`, `<p><img src="/p?http://a/1.png" alt="x"></p>`},
		{"srcset candidates keep descriptors", `<img src="http://a/1.png" srcset="http://a/1.png 1x, http://a/2.png 2x">`,
			`<img src="/p?http://a/1.png" srcset="/p?http://a/1.png 1x, /p?http://a/2.png 2x">`},
		{"picture source", `<picture><source srcset="http://a/x.avif 480w, http://a/y.avif 800w" type="image/avif"><img src="http://a/f.jpg"></picture>`,
			`<picture><source srcset="/p?http://a/x.avif 480w, /p?http://a/y.avif 800w" type="image/avif"><img src="/p?http://a/f.jpg"></picture>`},
		{"video poster only", `<video src="http://a/v.mp4" poster="http://a/p.jpg"><source src="http://a/v.webm"></video>`,
			`<video src="http://a/v.mp4" poster="/p?http://a/p.jpg"><source src="http://a/v.webm"></video>`},
		{"links and audio untouched", `<a href="http://a/x"><img src="http://a/i.png"></a><audio src="http://a/s.mp3"></audio>`,
			`<a href="http://a/x"><img src="/p?http://a/i.png"></a><audio src="http://a/s.mp3"></audio>`},
		{"untouched markup is byte-identical", `<p class=x>a &amp; b<br/></p>`, `<p class=x>a &amp; b<br/></p>`},
		{"uppercase attribute", `<IMG SRC="http://a/1.png">`, `<img src="/p?http://a/1.png">`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, RewriteImages(c.in, fn), c.name)
	}
	// A candidate fn rejects is dropped from a srcset.
	drop := func(u string) string {
		if u == "http://a/2.png" {
			return ""
		}
		return u
	}
	require.Equal(t, `<img srcset="http://a/1.png 1x">`, RewriteImages(`<img srcset="http://a/1.png 1x, http://a/2.png 2x">`, drop))
	require.Equal(t, "", RewriteImages("", fn))
	require.Equal(t, "<p>x</p>", RewriteImages("<p>x</p>", nil))
}
