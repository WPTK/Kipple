package sanitize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// proxy stands in for the signed image proxy; like the real one it leaves its
// own output alone.
func proxy(u string) string {
	if strings.HasPrefix(u, "/img/") || !strings.HasPrefix(u, "http") {
		return u
	}
	return "/img/" + strings.NewReplacer("://", "_", "/", "_").Replace(u)
}

var testOpts = ServeOptions{Image: proxy, Thumb: proxy, StripTracking: true}

const relNew = ` target="_blank" rel="noopener noreferrer"`

func TestServeHTMLGolden(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"external link", `<p><a href="https://a.example/x">x</a></p>`,
			`<p><a href="https://a.example/x"` + relNew + `>x</a></p>`},
		{"existing target and rel are replaced in place", `<a href="http://a.example/" target="_self" rel="nofollow">x</a>`,
			`<a href="http://a.example/" target="_blank" rel="noopener noreferrer">x</a>`},
		{"mailto and tel untouched", `<a href="mailto:a@b.c">m</a><a href="tel:+1555">t</a>`,
			`<a href="mailto:a@b.c">m</a><a href="tel:+1555">t</a>`},
		{"footnote ids and links are namespaced", `<p>See<a href="#fn1" id="fnref1">1</a></p><ol><li id="fn1">Note <a href="#fnref1">back</a></li></ol>`,
			`<p>See<a href="#kp-fn1" id="kp-fnref1">1</a></p><ol><li id="kp-fn1">Note <a href="#kp-fnref1">back</a></li></ol>`},
		{"name anchors", `<a name="top"></a><a href="#top">up</a><a href="#">bare</a>`,
			`<a name="kp-top"></a><a href="#kp-top">up</a><a href="#">bare</a>`},
		{"images through the proxy", `<img src="http://a.example/1.png" srcset="http://a.example/1.png 1x, http://a.example/2.png 2x">`,
			`<img src="/img/http_a.example_1.png" srcset="/img/http_a.example_1.png 1x, /img/http_a.example_2.png 2x">`},
		{"youtube iframe becomes a placeholder",
			`<p>Before</p><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ" sandbox="" width="560"></iframe><p>After</p>`,
			`<p>Before</p><figure class="kp-embed" data-provider="youtube" data-id="dQw4w9WgXcQ">` +
				`<img src="/img/https_i.ytimg.com_vi_dQw4w9WgXcQ_hqdefault.jpg" alt="">` +
				`<a href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"` + relNew + `>Watch on YouTube</a></figure><p>After</p>`},
		{"nocookie host too", `<iframe src="https://www.youtube-nocookie.com/embed/abc_-123?rel=0"></iframe>`,
			`<figure class="kp-embed" data-provider="youtube" data-id="abc_-123">` +
				`<img src="/img/https_i.ytimg.com_vi_abc_-123_hqdefault.jpg" alt="">` +
				`<a href="https://www.youtube.com/watch?v=abc_-123"` + relNew + `>Watch on YouTube</a></figure>`},
		{"vimeo iframe", `<iframe src="https://player.vimeo.com/video/76979871?h=abc"></iframe>`,
			`<figure class="kp-embed" data-provider="vimeo" data-id="76979871">` +
				`<a href="https://vimeo.com/76979871"` + relNew + `>Watch on Vimeo</a></figure>`},
		{"other iframe vanishes with its content", `<p>a</p><iframe src="https://evil.example/x">fallback <b>text</b></iframe><p>b</p>`, `<p>a</p><p>b</p>`},
		{"video and audio get controls and preload none", `<video src="https://a.example/v.mp4" autoplay poster="http://a.example/p.jpg"></video><audio src="https://a.example/a.mp3" preload="auto" controls></audio>`,
			`<video src="https://a.example/v.mp4" poster="/img/http_a.example_p.jpg" preload="none" controls=""></video><audio src="https://a.example/a.mp3" preload="none" controls=""></audio>`},
		{"http video source becomes a link", `<p>x</p><video src="http://a.example/v.mp4" controls><source src="http://a.example/v.webm">Fallback</video><p>y</p>`,
			`<p>x</p><p><a href="http://a.example/v.mp4"` + relNew + `>Open video</a></p><p>y</p>`},
		{"all http sources dropped leaves a link", `<audio controls><source src="http://a.example/a.ogg"><source src="http://a.example/a.mp3"></audio>`,
			`<audio controls="" preload="none"></audio><p><a href="http://a.example/a.ogg"` + relNew + `>Open audio</a></p>`},
		{"an https source keeps the element playable", `<video><source src="http://a.example/a.webm"><source src="https://a.example/a.mp4" type="video/mp4"></video>`,
			`<video preload="none" controls=""><source src="https://a.example/a.mp4" type="video/mp4"></video>`},
		{"untouched markup is byte-identical", `<p class=x>a &amp; b<br/><b>c</b></p>`, `<p class=x>a &amp; b<br/><b>c</b></p>`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, ServeHTML(c.in, testOpts), c.name)
	}
	require.Equal(t, "", ServeHTML("", testOpts))
}

func TestServeHTMLNoThumbWithoutProxy(t *testing.T) {
	in := `<iframe src="https://www.youtube.com/embed/abc"></iframe>`
	for _, o := range []ServeOptions{{}, {Thumb: func(u string) string { return u }}} {
		out := ServeHTML(in, o)
		require.NotContains(t, out, "<img", "the browser must not contact ytimg before the tap")
		require.Contains(t, out, `data-id="abc"`)
	}
}

func TestServeHTMLTrackingStripToggle(t *testing.T) {
	in := `<a href="https://a.example/p?utm_source=x&amp;id=7">x</a>`
	on := ServeHTML(in, ServeOptions{StripTracking: true})
	require.Contains(t, on, `href="https://a.example/p?id=7"`)
	off := ServeHTML(in, ServeOptions{})
	require.Contains(t, off, `href="https://a.example/p?utm_source=x&amp;id=7"`)
	require.Contains(t, off, relNew, "links still open safely")
}

func TestStripTracking(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.example/p?utm_source=x&id=7&utm_medium=y", "https://a.example/p?id=7"},
		{"https://a.example/p?b=2&fbclid=Z&a=1", "https://a.example/p?b=2&a=1"},
		{"https://a.example/p?gclid=1", "https://a.example/p"},
		{"https://a.example/p?gclid=1#frag", "https://a.example/p#frag"},
		{"https://a.example/p?q=a%20b&UTM_Campaign=c&x=%2F", "https://a.example/p?q=a%20b&x=%2F"},
		{"https://a.example/p?utm%5Fsource=1&k=v", "https://a.example/p?k=v"},
		{"https://a.example/p?mc_cid=1&mc_eid=2&igshid=3&_hsenc=4&_hsmi=5&mkt_tok=6&oly_anon_id=7&oly_enc_id=8&vero_id=9&ref_src=10&dclid=11&yclid=12", "https://a.example/p"},
		{"https://a.example/p?keep=1&&x=2", "https://a.example/p?keep=1&&x=2"},
		{"https://a.example/p?utm=1&utmx=2&ref=3&source=4", "https://a.example/p?utm=1&utmx=2&ref=3&source=4"},
		{"https://a.example/p", "https://a.example/p"},
		{"mailto:a@b.c?utm_source=x", "mailto:a@b.c?utm_source=x"},
		{"#frag?utm_source=x", "#frag?utm_source=x"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, StripTracking(c.in), c.in)
	}
}

func TestServeHTMLIsIdempotent(t *testing.T) {
	in := `<p>Intro <a href="https://a.example/x?utm_source=z&amp;k=1">link</a> and a note<a href="#fn1" id="r1">1</a>.</p>` +
		`<iframe src="https://www.youtube.com/embed/abc123"></iframe><iframe src="https://player.vimeo.com/video/5"></iframe>` +
		`<img src="http://a.example/1.png" srcset="http://a.example/1.png 1x"><video autoplay><source src="http://a.example/v.webm"></video>` +
		`<audio src="http://a.example/a.mp3"></audio><audio src="https://a.example/a.mp3"></audio>` +
		`<ol><li id="fn1">n <a href="#r1">back</a></li></ol><a name="top"></a><a href="javascript:alert(1)">bad</a>`
	once := ServeHTML(in, testOpts)
	require.NotEqual(t, in, once)
	require.Equal(t, once, ServeHTML(once, testOpts))
	require.Equal(t, once, ServeHTML(ServeHTML(once, testOpts), testOpts))
}

func TestServeHTMLXSSRegression(t *testing.T) {
	// Not reachable through Content (bluemonday goes first), but the serve path
	// must not depend on that.
	hostile := []struct{ name, in string }{
		{"script element", `<p>a</p><script>alert(1)</script><p>b</p>`},
		{"script with attributes and nesting", `<script src="https://evil.example/x.js"></script><script>document.write("<script>x()<\/script>")</script>`},
		{"style element", `<style>body{background:url(https://evil.example/t)}</style>`},
		{"inline handlers", `<img src="https://a.example/x.png" onerror="alert(1)" ONLOAD="x()"><p onclick="x()" onmouseover=y>t</p>`},
		{"javascript href", `<a href="javascript:alert(1)">x</a>`},
		{"obfuscated javascript href", `<a href="  jav&#x09;ascript:alert(1)">x</a><a href="JaVaScRiPt:alert(1)">y</a><a href="java&#10;script:alert(1)">z</a>`},
		{"data and vbscript href", `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a><a href="vbscript:msgbox(1)">y</a>`},
		{"object embed base meta link", `<object data="https://evil.example/x.swf"><param name="a" value="b"></object><embed src="https://evil.example/x"><base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example/"><link rel="stylesheet" href="https://evil.example/s.css">`},
		{"srcdoc and style attributes", `<iframe srcdoc="<script>alert(1)</script>"></iframe><p style="background:url(https://evil.example/t)">x</p>`},
		{"form action", `<form action="https://evil.example/"><button formaction="https://evil.example/">go</button></form>`},
		{"unknown iframe with script content", `<iframe src="https://evil.example/">x<script>alert(1)</script></iframe>`},
		{"clobbering ids", `<img id="location" name="cookie"><a id="document" name="body" href="#x">a</a>`},
	}
	for _, h := range hostile {
		out := ServeHTML(h.in, testOpts)
		low := strings.ToLower(out)
		for _, bad := range []string{"<script", "<style", "<iframe", "<object", "<embed", "<base", "<meta", "<link", "onerror", "onload", "onclick", "onmouseover", "javascript:", "vbscript:", "data:text", "srcdoc", "style=", "formaction", "action=", "evil.example/s.css"} {
			require.NotContains(t, low, bad, h.name)
		}
		require.Equal(t, out, ServeHTML(out, testOpts), h.name+": idempotent")
	}
	out := ServeHTML(`<img id="location" name="cookie"><a id="document" name="body" href="#x">a</a>`, testOpts)
	require.Equal(t, `<img id="kp-location" name="kp-cookie"><a id="kp-document" name="kp-body" href="#kp-x">a</a>`, out)
}

// The whole ingest chain: whatever a hostile feed sends, serving it is safe and
// stable.
func TestContentThenServe(t *testing.T) {
	raw := `<p>Read <a href="/more?utm_source=rss&id=3">more</a><script>x()</script></p>` +
		`<iframe src="https://www.youtube.com/embed/abc123"></iframe><iframe src="https://evil.example/w">t</iframe>` +
		`<video src="http://cdn.example/v.mp4" autoplay controls></video>`
	stored, _ := Content(raw, "https://a.example/post/")
	served := ServeHTML(stored, testOpts)
	require.Contains(t, served, `href="https://a.example/more?id=3"`)
	require.Contains(t, served, `data-provider="youtube"`)
	require.Contains(t, served, `Embedded content from evil.example`)
	require.NotContains(t, served, "<iframe")
	require.NotContains(t, served, "<script")
	require.Contains(t, served, "Open video")
	require.Equal(t, served, ServeHTML(served, testOpts))
	// The stored form is not touched by serving.
	require.Contains(t, stored, "utm_source=rss")
}

// An unterminated <video src="http://..."> still gets its "Open video" link.
func TestServeHTMLUnclosedMixedContentMediaKeepsLink(t *testing.T) {
	for _, tag := range []string{"video", "audio"} {
		out := ServeHTML(`<p>a</p><`+tag+` src="http://a.example/m.mp4">no end tag`, testOpts)
		require.Contains(t, out, `<a href="http://a.example/m.mp4" target="_blank" rel="noopener noreferrer">Open `+tag+`</a>`, tag)
		require.NotContains(t, out, "<"+tag)
	}
	// A closed one is unchanged: exactly one link.
	out := ServeHTML(`<video src="http://a.example/m.mp4"></video>`, testOpts)
	require.Equal(t, 1, strings.Count(out, "Open video"))
}

// References to ids follow the "kp-" prefix the ids get, and stay idempotent.
func TestServeHTMLPrefixesIDReferences(t *testing.T) {
	in := `<table><tr><th id="h1">a</th><th id="h2">b</th></tr><tr><td headers="h1 h2">x</td></tr></table>` +
		`<label for="f">L</label><input id="f" aria-describedby="d1  d2" aria-labelledby="l" aria-controls="c">` +
		`<img usemap="#m" src="/x.png"><map name="m"></map>`
	out := ServeHTML(in, ServeOptions{})
	for _, want := range []string{`headers="kp-h1 kp-h2"`, `for="kp-f"`, `aria-describedby="kp-d1 kp-d2"`,
		`aria-labelledby="kp-l"`, `aria-controls="kp-c"`, `usemap="#kp-m"`, `id="kp-h1"`, `name="kp-m"`} {
		require.Contains(t, out, want)
	}
	require.Equal(t, out, ServeHTML(out, ServeOptions{}))
}
