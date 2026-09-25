// Package sanitize holds the content pipeline helpers (design §4.4, §9).
//
// Step 3 of phase 1 only needs the pieces the feed parser depends on: URL
// resolution, plain-text extraction and the lead-image pick. The bluemonday
// feed policy and the full absolutize pass land with the content pipeline.
package sanitize

import (
	"strings"

	"golang.org/x/net/html"
)

var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "ul": true, "ol": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "tr": true, "table": true, "hr": true,
	"figure": true, "figcaption": true, "section": true, "article": true,
}

// PlainText returns the visible text of an HTML fragment with entities
// decoded, script/style content dropped and whitespace collapsed. Block-level
// tags separate words so "<p>a</p><p>b</p>" reads "a b".
func PlainText(src string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(src))
	skip := 0
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			n := string(name)
			if n == "script" || n == "style" {
				if tt == html.StartTagToken {
					skip++
				}
			} else if blockTags[n] {
				b.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			n := string(name)
			if (n == "script" || n == "style") && skip > 0 {
				skip--
			} else if blockTags[n] {
				b.WriteByte(' ')
			}
		}
	}
}
