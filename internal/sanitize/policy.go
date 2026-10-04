package sanitize

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

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
		p.AllowAttrs("width", "height", "allowfullscreen", "frameborder", "sandbox").OnElements("iframe")
		// sandbox has to be an allowed attribute or bluemonday drops it and writes
		// sandbox="" (a blank player in Reader clients); IframesToLinks sets the
		// value and this filters it to the tokens Kipple grants.
		p.RequireSandboxOnIFrame(bluemonday.SandboxAllowScripts, bluemonday.SandboxAllowSameOrigin,
			bluemonday.SandboxAllowPresentation, bluemonday.SandboxAllowPopups)
		policy = p
	})
	return policy
}

// Content is the per-item content pipeline (design §4.4): resolve URLs against
// the base chain, sanitize, then derive plain text. It matches fetch.ContentFunc.
func Content(rawHTML string, bases ...string) (htmlOut, text string) {
	sh := newShielded()
	htmlOut = FeedPolicy().Sanitize(sh.shield(IframesToLinks(Absolutize(rawHTML, bases...))))
	htmlOut = strings.TrimSpace(sh.unshield(htmlOut))
	return htmlOut, PlainText(htmlOut)
}

// fragmentShield stands in for the document while bluemonday runs. Its URL
// validation drops scheme-less values, and relative URLs cannot be allowed
// without weakening every other URL attribute, so in-page anchors ("#fn1",
// footnotes) travel as absolute URLs on this reserved host and are turned back
// into bare fragments afterwards. Every a[href] beginning with "#" is shielded,
// whatever characters follow (Absolutize keeps all of them).
//
// Each shielded href becomes fragmentShield + <nonce>/<index>; the original
// fragment is kept out of band, so it needs no escaping to survive the policy,
// and the nonce is random per call, so a literal shield URL in feed HTML is
// never mistaken for one of ours.
const fragmentShield = "https://fragment.kipple.invalid/"

type shielded struct {
	nonce string
	frags []string
}

func newShielded() *shielded {
	var n [12]byte
	if _, err := rand.Read(n[:]); err != nil {
		panic("sanitize: crypto/rand: " + err.Error())
	}
	return &shielded{nonce: hex.EncodeToString(n[:])}
}

func (s *shielded) prefix() string { return fragmentShield + s.nonce + "/" }

// shield rewrites a[href="#..."] to a placeholder URL; every other href is left
// exactly as it was.
func (s *shielded) shield(src string) string {
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
					if v := strings.TrimSpace(a.Val); strings.EqualFold(a.Key, "href") && strings.HasPrefix(v, "#") {
						t.Attr[i].Val = s.prefix() + strconv.Itoa(len(s.frags))
						s.frags = append(s.frags, v)
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

// unshield restores the original fragments of the hrefs shield produced.
func (s *shielded) unshield(out string) string {
	pre := `href="` + s.prefix()
	if !strings.Contains(out, pre) {
		return out
	}
	var b strings.Builder
	for {
		i := strings.Index(out, pre)
		if i < 0 {
			b.WriteString(out)
			return b.String()
		}
		b.WriteString(out[:i])
		rest := out[i+len(pre):]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		n, err := strconv.Atoi(rest[:j])
		if j == 0 || j >= len(rest) || rest[j] != '"' || err != nil || n >= len(s.frags) {
			// not ours after all (cannot happen with a random nonce): keep as is
			b.WriteString(pre)
			out = rest
			continue
		}
		b.WriteString(`href="` + html.EscapeString(s.frags[n]) + `"`)
		out = rest[j+1:]
	}
}

// WordCount counts whitespace-separated words in plain text. It equals
// len(strings.Fields(text)) (unicode.IsSpace) without building the slice.
func WordCount(text string) int {
	n, inWord := 0, false
	for _, r := range text {
		if unicode.IsSpace(r) {
			inWord = false
		} else if !inWord {
			inWord = true
			n++
		}
	}
	return n
}
