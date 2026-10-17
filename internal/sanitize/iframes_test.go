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
		{"youtube kept for the policy", `<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ">x</iframe>`, `<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ" sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"></iframe>`},
		{"vimeo kept for the policy", `<iframe src="https://player.vimeo.com/video/123?dnt=1"></iframe>`, `<iframe src="https://player.vimeo.com/video/123?dnt=1" sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"></iframe>`},
		{"a lookalike host is a link", `<iframe src="https://www.youtube.com.evil.example/embed/dQw4w9WgXcQ"></iframe>`,
			`<p><a href="https://www.youtube.com.evil.example/embed/dQw4w9WgXcQ">Embedded content from youtube.com.evil.example</a></p>`},
		{"host is escaped", `<iframe src="https://a.example/&quot;&gt;&lt;script&gt;"></iframe>`,
			`<p><a href="https://a.example/&#34;&gt;&lt;script&gt;">Embedded content from a.example</a></p>`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, IframesToLinks(c.in), c.name)
	}
}

func TestContentKeepsVimeoAndYouTubeOnly(t *testing.T) {
	h, _ := Content(`<iframe src="https://player.vimeo.com/video/123?dnt=1"></iframe>`+
		`<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"></iframe>`+
		`<iframe src="https://player.vimeo.com.evil.example/video/1"></iframe>`, "https://a.example/")
	require.Contains(t, h, `src="https://player.vimeo.com/video/123?dnt=1"`)
	require.Contains(t, h, `src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"`)
	require.Equal(t, 2, strings.Count(h, "<iframe"))
	require.Contains(t, h, "Embedded content from player.vimeo.com.evil.example")
}

// An embed is kept only as the player itself: the id ends the path, and only a
// query or fragment may follow. Anything else becomes a link (IframesToLinks)
// and never reaches the stored iframe with its sandbox grants.
func TestEmbedSourceShape(t *testing.T) {
	for _, u := range []string{
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
		"https://youtube.com/embed/dQw4w9WgXcQ?feature=oembed",
		"https://www.youtube-nocookie.com/embed/abc_-123456?rel=0&start=90",
		"https://www.youtube.com/embed/videoseries?list=PLx_Y-9",
		"https://www.youtube.com/embed/live_stream?channel=UC0123456789",
		"https://www.youtube.com/embed/dQw4w9WgXcQ?si=a1B2c3&origin=https://a.example",
		"https://www.youtube.com/embed/dQw4w9WgXcQ#t=30",
		"https://player.vimeo.com/video/76979871",
		"https://player.vimeo.com/video/76979871?h=8a1b2c3d4e&badge=0#t=5",
	} {
		require.True(t, embedSrc.MatchString(u), u)
		h, _ := Content(`<iframe src="` + u + `"></iframe>`)
		require.Contains(t, h, "<iframe", u)
	}
	for _, u := range []string{
		"https://www.youtube.com/embed/dQw4w9WgXcQ/../../redirect?q=https://evil.example",
		"https://www.youtube.com/embed/x/../../redirect?q=https://evil.example",
		"https://www.youtube.com/embed/dQw4w9WgXcQ/extra",
		"https://www.youtube.com/embed/dQw4w9WgXcQ&x=1",
		"https://www.youtube.com/embed/dQw4w9WgXcQ%2F..%2F..%2Fredirect",
		`https://www.youtube.com/embed/dQw4w9WgXcQ?\..\..\redirect`,
		"https://www.youtube.com/embed/short",
		"https://www.youtube.com/embed/dQw4w9WgXcQx",
		"https://www.youtube.com/embed/",
		"https://player.vimeo.com/video/123/../../x",
		"https://player.vimeo.com/video/123/abc",
		"https://player.vimeo.com/video/123&x=1",
		"https://player.vimeo.com/video/",
	} {
		require.False(t, embedSrc.MatchString(u), u)
		h, _ := Content(`<iframe src="` + u + `"></iframe>`)
		require.NotContains(t, h, "<iframe", u)
	}
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
		`<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe>`,
		`<iframe src="https://player.vimeo.com/video/123" sandbox="allow-top-navigation allow-forms"></iframe>`,
		`<iframe src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" sandbox=""></iframe>`,
	} {
		out, _ := Content(in)
		require.Contains(t, out, `sandbox="allow-scripts allow-same-origin allow-presentation allow-popups"`, in)
		require.NotContains(t, out, "allow-top-navigation")
		require.NotContains(t, out, "allow-forms")
	}
}
