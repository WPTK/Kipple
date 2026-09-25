package sanitize

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var vimeoEmbed = regexp.MustCompile(`^https://player\.vimeo\.com/video/[0-9]+([?&/#][^\s"'<>]*)?$`)

// IframesToLinks is the ingest pre-pass that runs before bluemonday (design
// 4.4): a YouTube or Vimeo iframe is kept for the policy to vet, any other
// iframe with an absolute http(s) source becomes a plain link paragraph
// ("Embedded content from host"), and an iframe without a usable source goes.
// Before this the policy dropped every other iframe silently. Markup without an
// iframe comes back byte for byte.
func IframesToLinks(src string) string {
	if !strings.Contains(strings.ToLower(src), "<iframe") {
		return src
	}
	var b strings.Builder
	b.Grow(len(src))
	z := html.NewTokenizer(strings.NewReader(src))
	skip := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		raw := append([]byte(nil), z.Raw()...)
		if skip > 0 {
			switch tt {
			case html.StartTagToken:
				if z.Token().Data == "iframe" {
					skip++
				}
			case html.EndTagToken:
				if z.Token().Data == "iframe" {
					skip--
				}
			}
			continue
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			b.Write(raw)
			continue
		}
		t := z.Token()
		if t.Data != "iframe" {
			b.Write(raw)
			continue
		}
		if tt == html.StartTagToken {
			skip = 1
		}
		u := attrVal(t, "src")
		if youtubeEmbed.MatchString(u) || vimeoEmbed.MatchString(u) {
			// Re-emit the opening tag and the end tag the skip would swallow.
			b.Write(raw)
			if tt == html.StartTagToken {
				b.WriteString("</iframe>")
			}
			continue
		}
		if p, err := url.Parse(u); err == nil && isHTTP(u) && p.Hostname() != "" {
			host := strings.TrimPrefix(strings.ToLower(p.Hostname()), "www.")
			b.WriteString(`<p><a href="` + html.EscapeString(u) + `">Embedded content from ` + html.EscapeString(host) + `</a></p>`)
		}
	}
}
