package sanitize

import (
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

var (
	embedWatch   = regexp.MustCompile(`^https://www\.youtube\.com/watch\?v=[A-Za-z0-9_-]{1,64}(&list=[A-Za-z0-9_-]{1,64})?(&t=[0-9]{1,6}s)?$`)
	embedPlaylst = regexp.MustCompile(`^https://www\.youtube\.com/playlist\?list=[A-Za-z0-9_-]{1,64}$`)
	embedVimeo   = regexp.MustCompile(`^https://vimeo\.com/[0-9]{1,20}(/[0-9a-f]{1,32})?$`)
)

// FuzzEmbedPlaceholder: whatever an iframe's src says, the click-to-load figure is either nothing or exactly
// one figure of the fixed shape: the provider is youtube or vimeo, every data-* value matches its tight
// pattern, the only link is the provider's own watch, playlist or video page built from those values, and
// the only image is the thumbnail the caller supplied. The player parameters from the src (list, start, h)
// never appear unless they match, so nothing from a hostile src reaches the markup unescaped.
func FuzzEmbedPlaceholder(f *testing.F) {
	for _, s := range []string{
		"https://www.youtube.com/embed/dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/abc_-123?rel=0",
		"https://www.youtube.com/embed/videoseries?list=PLx_Y-9&rel=0", "https://www.youtube.com/embed/videoseries",
		"https://www.youtube-nocookie.com/embed/abc?list=PL1&start=90&autoplay=1",
		"https://www.youtube.com/embed/abc?list=PL%22%3E%3Cscript%3E&start=1e3",
		"https://player.vimeo.com/video/76979871?h=8a1b2c3d4e&badge=0#t=5", "https://player.vimeo.com/video/5?h=../x",
		"https://player.vimeo.com/video/123456789012345678901", "https://www.youtube.com/embed/abc/../x",
		"https://www.youtube.com.evil.example/embed/abc", "https://youtube.com/embed/a?list=a&list=%22", "HTTPS://www.youtube.com/embed/abc",
		"https://www.youtube.com/embed/abc?start=%0a1", "//www.youtube.com/embed/abc", "https://www.youtube.com/embed/",
		"https://www.youtube.com/embed/abc?list=" + strings.Repeat("a", 65), "https://player.vimeo.com/video/5?h=" + strings.Repeat("f", 33),
	} {
		f.Add(s, true)
		f.Add(s, false)
	}
	f.Fuzz(func(t *testing.T, src string, withThumb bool) {
		opt := ServeOptions{}
		if withThumb {
			opt.Thumb = func(u string) string { return "/thumb?u=" + u }
		}
		tok := html.Token{Type: html.StartTagToken, Data: "iframe", Attr: []html.Attribute{{Key: "src", Val: src}}}
		out := embedPlaceholder(tok, opt)
		if out == "" {
			return
		}
		z := html.NewTokenizer(strings.NewReader(out))
		var figures, links, imgs int
		var provider string
		for {
			tt := z.Next()
			if tt == html.ErrorToken {
				break
			}
			tk := z.Token()
			switch tk.Type {
			case html.StartTagToken, html.SelfClosingTagToken:
				switch tk.Data {
				case "figure":
					figures++
					for _, a := range tk.Attr {
						switch a.Key {
						case "class":
							if a.Val != "kp-embed" {
								t.Fatalf("src %q: class %q in %q", src, a.Val, out)
							}
						case "data-provider":
							provider = a.Val
							if a.Val != "youtube" && a.Val != "vimeo" {
								t.Fatalf("src %q: provider %q in %q", src, a.Val, out)
							}
						case "data-id":
							if provider == "youtube" && !ytIDValue.MatchString(a.Val) || provider == "vimeo" && !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(a.Val) {
								t.Fatalf("src %q: data-id %q in %q", src, a.Val, out)
							}
						case "data-list":
							if !ytListValue.MatchString(a.Val) {
								t.Fatalf("src %q: data-list %q in %q", src, a.Val, out)
							}
						case "data-start":
							if !startValue.MatchString(a.Val) {
								t.Fatalf("src %q: data-start %q in %q", src, a.Val, out)
							}
						case "data-h":
							if !vimeoHashValue.MatchString(a.Val) {
								t.Fatalf("src %q: data-h %q in %q", src, a.Val, out)
							}
						default:
							t.Fatalf("src %q: unexpected figure attribute %q in %q", src, a.Key, out)
						}
					}
				case "a":
					links++
					for _, a := range tk.Attr {
						switch a.Key {
						case "href":
							if !embedWatch.MatchString(a.Val) && !embedPlaylst.MatchString(a.Val) && !embedVimeo.MatchString(a.Val) {
								t.Fatalf("src %q: link %q in %q", src, a.Val, out)
							}
						case "target", "rel":
						default:
							t.Fatalf("src %q: unexpected link attribute %q in %q", src, a.Key, out)
						}
					}
				case "img":
					imgs++
					for _, a := range tk.Attr {
						if a.Key == "src" && !strings.HasPrefix(a.Val, "/thumb?u=https://i.ytimg.com/vi/") {
							t.Fatalf("src %q: image %q in %q", src, a.Val, out)
						}
					}
				default:
					t.Fatalf("src %q: unexpected element <%s> in %q", src, tk.Data, out)
				}
			}
		}
		if figures != 1 || links != 1 || imgs > 1 || (imgs == 1 && !withThumb) {
			t.Fatalf("src %q: %d figures, %d links, %d images (thumb %v) in %q", src, figures, links, imgs, withThumb, out)
		}
		if !strings.HasPrefix(out, "<figure ") || !strings.HasSuffix(out, "</figure>") {
			t.Fatalf("src %q: not one figure: %q", src, out)
		}
	})
}
