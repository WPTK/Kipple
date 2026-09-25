package sanitize

import (
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
)

var (
	policyOnce sync.Once
	policy     *bluemonday.Policy
)

var youtubeEmbed = regexp.MustCompile(`^https://(www\.)?(youtube\.com|youtube-nocookie\.com)/embed/[A-Za-z0-9_-]+([?&/#][^\s"'<>]*)?$`)

// FeedPolicy is the bluemonday policy for feed content (design §4.4 step 3):
// UGCPolicy plus media elements and YouTube embeds, absolute URLs only.
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

		p.AllowAttrs("src").Matching(youtubeEmbed).OnElements("iframe")
		p.AllowAttrs("width", "height", "allowfullscreen", "frameborder").OnElements("iframe")
		p.RequireSandboxOnIFrame(bluemonday.SandboxAllowScripts, bluemonday.SandboxAllowSameOrigin, bluemonday.SandboxAllowPresentation)
		policy = p
	})
	return policy
}

// Content is the per-item content pipeline (design §4.4): resolve URLs against
// the base chain, sanitize, then derive plain text. It matches fetch.ContentFunc.
func Content(rawHTML string, bases ...string) (htmlOut, text string) {
	htmlOut = FeedPolicy().Sanitize(Absolutize(rawHTML, bases...))
	htmlOut = strings.TrimSpace(htmlOut)
	return htmlOut, PlainText(htmlOut)
}

// WordCount counts whitespace-separated words in plain text.
func WordCount(text string) int { return len(strings.Fields(text)) }
