package sanitize

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAbsolutize(t *testing.T) {
	bases := []string{"https://a.com/post/1", "https://a.com/", "https://feed.example/f.xml"}
	in := `<p><a href="x">x</a> <a href="#fn1">fn</a> <a href="mailto:a@b.c">m</a> <a href="javascript:alert(1)">j</a>` +
		`<img src="/wp-content/x.jpg" srcset="/a.jpg 1x, //cdn.example/b.jpg 2x, data:image/gif;base64,AAA 3x"> ` +
		`<img src="data:image/png;base64,AAA">` +
		`<video poster="p.png" src="v.mp4"></video><audio src="/a.mp3"></audio>` +
		`<picture><source srcset="/s1.webp 480w, /s2.webp 800w" type="image/webp"></picture></p>`
	got := Absolutize(in, bases...)
	require.Contains(t, got, `href="https://a.com/post/x"`)
	require.Contains(t, got, `href="#fn1"`)
	require.Contains(t, got, `href="mailto:a@b.c"`)
	require.NotContains(t, got, "javascript:")
	require.Contains(t, got, `src="https://a.com/wp-content/x.jpg"`)
	require.Contains(t, got, `srcset="https://a.com/a.jpg 1x, https://cdn.example/b.jpg 2x"`)
	require.NotContains(t, got, "data:")
	require.Contains(t, got, `poster="https://a.com/post/p.png"`)
	require.Contains(t, got, `src="https://a.com/post/v.mp4"`)
	require.Contains(t, got, `src="https://a.com/a.mp3"`)
	require.Contains(t, got, `srcset="https://a.com/s1.webp 480w, https://a.com/s2.webp 800w"`)
	// untouched markup passes through byte for byte
	require.Equal(t, "<p>plain <b>text</b> &amp; more</p>", Absolutize("<p>plain <b>text</b> &amp; more</p>", bases...))
}

func TestContentPolicy(t *testing.T) {
	bases := []string{"https://a.com/post/"}
	h, txt := Content(`<p onclick="x()">Hi <script>evil()</script><img src="/i.png"></p>`+
		`<iframe src="https://www.youtube.com/embed/abc123"></iframe><iframe src="https://evil.example/x"></iframe>`, bases...)
	require.NotContains(t, h, "<script")
	require.NotContains(t, h, "onclick")
	require.Contains(t, h, `<a href="https://evil.example/x" rel="nofollow">Embedded content from evil.example</a>`)
	require.NotContains(t, h, "<iframe src=\"https://evil")
	require.Contains(t, h, `src="https://a.com/i.png"`)
	require.Contains(t, h, "youtube.com/embed/abc123")
	require.Equal(t, "Hi Embedded content from evil.example", txt)
	require.Equal(t, 2, WordCount("two words"))
}
