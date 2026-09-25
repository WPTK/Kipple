package sanitize

import (
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var (
	policyOnce sync.Once
	policy     *bluemonday.Policy
)

var youtubeEmbed = regexp.MustCompile(`^https://(www\.)?(youtube\.com|youtube-nocookie\.com)/embed/[A-Za-z0-9_-]+([?&/#][^\s"'<>]*)?$`)

// embedSrc is every iframe source the policy keeps: YouTube and Vimeo players.
var embedSrc = regexp.MustCompile("(?:" + youtubeEmbed.String() + ")|(?:" + vimeoEmbed.String() + ")")

// FeedPolicy is the bluemonday policy for feed content (design §4.4 step 3):
// UGCPolicy plus media elements and YouTube and Vimeo embeds, absolute URLs only.
func FeedPolicy() *bluemonday.Policy {
	policyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		p.RequireParseableURLs(true)
		p.AllowRelativeURLs(false)

		p.AllowElements("picture", "figure", "figcaption")
		p.AllowAttrs("srcset", "sizes", "media", "type").OnElements("source", "img")
		p.AllowAttrs("src").OnElements("source")
		p.AllowAttrs("width", "height", "loading", "decoding").OnElements("img")

		p.AllowElements("video", "audio")
		p.AllowAttrs("src", "poster", "controls", "preload", "loop", "muted", "playsinline", "width", "height").OnElements("video")
		p.AllowAttrs("src", "controls", "preload", "loop").OnElements("audio")
		p.AllowURLSchemes("http", "https", "mailto", "tel")

		p.AllowAttrs("src").Matching(embedSrc).OnElements("iframe")
		p.AllowAttrs("width", "height", "allowfullscreen", "frameborder").OnElements("iframe")
		p.RequireSandboxOnIFrame(bluemonday.SandboxAllowScripts, bluemonday.SandboxAllowSameOrigin, bluemonday.SandboxAllowPresentation)
		policy = p
	})
	return policy
}

// Content is the per-item content pipeline (design §4.4): resolve URLs against
// the base chain, sanitize, then derive plain text. It matches fetch.ContentFunc.
func Content(rawHTML string, bases ...string) (htmlOut, text string) {
	htmlOut = FeedPolicy().Sanitize(shieldFragments(IframesToLinks(Absolutize(rawHTML, bases...))))
	htmlOut = strings.TrimSpace(unshieldFragments(htmlOut))
	return htmlOut, PlainText(htmlOut)
}

// fragmentShield stands in for the document while bluemonday runs. Its URL
// validation drops scheme-less values, and relative URLs cannot be allowed
// without weakening every other URL attribute, so in-page anchors ("#fn1",
// footnotes) travel as absolute URLs on this reserved host and are turned back
// into bare fragments afterwards.
const fragmentShield = "https://fragment.kipple.invalid/"

var fragmentHref = regexp.MustCompile(`^#[\w:.%-]*$`)

// shieldFragments rewrites a[href="#frag"] to the shield URL; every other
// href is left exactly as it was.
func shieldFragments(src string) string {
	if !strings.Contains(src, "#") {
		return src
	}
	var b strings.Builder
	b.Grow(len(src) + 32)
	z := html.NewTokenizer(strings.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken:
			raw := append([]byte(nil), z.Raw()...)
			t := z.Token()
			changed := false
			if t.Data == "a" {
				for i, a := range t.Attr {
					if strings.EqualFold(a.Key, "href") && fragmentHref.MatchString(strings.TrimSpace(a.Val)) {
						t.Attr[i].Val = fragmentShield + strings.TrimSpace(a.Val)
						changed = true
					}
				}
			}
			if changed {
				b.WriteString(t.String())
			} else {
				b.Write(raw)
			}
		default:
			b.Write(z.Raw())
		}
	}
}

func unshieldFragments(out string) string {
	return strings.ReplaceAll(out, `href="`+fragmentShield+`#`, `href="#`)
}

// WordCount counts whitespace-separated words in plain text.
func WordCount(text string) int { return len(strings.Fields(text)) }
