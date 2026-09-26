package sanitize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIframesToLinks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no iframe is byte-identical", `<p class=x>a &amp; b<br/></p>`, `<p class=x>a &amp; b<br/></p>`},
		{"other host becomes a link", `<p>a</p><iframe src="https://www.example.com/w?a=1&amp;b=2" width="1"></iframe>`,
			`<p>a</p><p><a href="https://www.example.com/w?a=1&amp;b=2">Embedded content from example.com</a></p>`},
		{"content inside is dropped", `<iframe src="https://x.example/e">Your browser <b>lacks</b> frames</iframe><p>z</p>`,
			`<p><a href="https://x.example/e">Embedded content from x.example</a></p><p>z</p>`},
		{"self-closing form", `<iframe src="https://x.example/e"/><p>z</p>`, `<p><a href="https://x.example/e">Embedded content from x.example</a></p><p>z</p>`},
		{"no source, no link", `<p>a</p><iframe></iframe><iframe srcdoc="x"></iframe><p>b</p>`, `<p>a</p><p>b</p>`},
		{"non-http source is dropped", `<iframe src="javascript:alert(1)"></iframe><iframe src="data:text/html,x"></iframe>`, ``},
		{"youtube kept for the policy", `<iframe src="https://www.youtube.com/embed/abc">x</iframe>`, `<iframe src="https://www.youtube.com/embed/abc" sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"></iframe>`},
		{"vimeo kept for the policy", `<iframe src="https://player.vimeo.com/video/123?dnt=1"></iframe>`, `<iframe src="https://player.vimeo.com/video/123?dnt=1" sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"></iframe>`},
		{"a lookalike host is a link", `<iframe src="https://www.youtube.com.evil.example/embed/abc"></iframe>`,
			`<p><a href="https://www.youtube.com.evil.example/embed/abc">Embedded content from youtube.com.evil.example</a></p>`},
		{"host is escaped", `<iframe src="https://a.example/&quot;&gt;&lt;script&gt;"></iframe>`,
			`<p><a href="https://a.example/&#34;&gt;&lt;script&gt;">Embedded content from a.example</a></p>`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, IframesToLinks(c.in), c.name)
	}
}

func TestContentKeepsVimeoAndYouTubeOnly(t *testing.T) {
	h, _ := Content(`<iframe src="https://player.vimeo.com/video/123?dnt=1"></iframe>`+
		`<iframe src="https://www.youtube-nocookie.com/embed/abc"></iframe>`+
		`<iframe src="https://player.vimeo.com.evil.example/video/1"></iframe>`, "https://a.example/")
	require.Contains(t, h, `src="https://player.vimeo.com/video/123?dnt=1"`)
	require.Contains(t, h, `src="https://www.youtube-nocookie.com/embed/abc"`)
	require.Equal(t, 2, strings.Count(h, "<iframe"))
	require.Contains(t, h, "Embedded content from player.vimeo.com.evil.example")
}

// Content is unchanged for anything that has no iframe: the Reader API output of
// existing items never moves.
func TestContentWithoutIframeIsUnchanged(t *testing.T) {
	in := `<p>Hello <a href="https://a.example/x">there</a> <img src="https://a.example/i.png"></p>`
	got, _ := Content(in, "https://a.example/")
	require.Equal(t, FeedPolicy().Sanitize(in), got)
}

// bluemonday alone writes sandbox="" (a blank player in Reader clients); the
// stored form carries the four tokens, whatever the feed sent.
func TestStoredEmbedSandboxIsUsable(t *testing.T) {
	for _, in := range []string{
		`<iframe src="https://www.youtube.com/embed/abc123"></iframe>`,
		`<iframe src="https://player.vimeo.com/video/123" sandbox="allow-top-navigation allow-forms"></iframe>`,
		`<iframe src="https://www.youtube-nocookie.com/embed/abc123" sandbox=""></iframe>`,
	} {
		out, _ := Content(in)
		require.Contains(t, out, `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"`, in)
		require.NotContains(t, out, "allow-top-navigation")
		require.NotContains(t, out, "allow-forms")
	}
}
