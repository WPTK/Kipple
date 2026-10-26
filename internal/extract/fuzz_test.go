package extract

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/WPTK/kipple/internal/sanitize"
)

// FuzzReadable: whatever markup a page holds, readable neither panics nor takes more than a second, and when it
// finds an article the result is already clean: sanitizing the HTML again changes nothing, the page has words, and
// the source address is where the fetch ended.
func FuzzReadable(f *testing.F) {
	for _, s := range []string{
		article(longBody()),
		article(`<p><img src="/a.png" onerror="alert(1)"> <a href="javascript:alert(1)">x</a></p><script>evil()</script>` + longBody()),
		"<html><body><p>" + para + "</p></body></html>",
		"<div><div><div>" + strings.Repeat("<p>"+para, 20),
		"<!--" + para, "<table><tr><td>" + para + "<svg><foreignObject><p>" + para, "<iframe src=//evil.example></iframe>",
		"", "<", "\x00\xff<p>" + para, strings.Repeat("<b>", 500) + para,
	} {
		f.Add(s)
	}
	pageURL := "https://example.org/posts/story.html"
	final, err := url.Parse("https://example.org/final/story.html")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body string) {
		start := time.Now()
		res, err := readable(strings.NewReader(body), pageURL, final)
		if d := time.Since(start); d > time.Second {
			t.Fatalf("took %v for %q", d, body)
		}
		if err != nil {
			return
		}
		if again, _ := sanitize.Content(res.HTML, pageURL, final.String()); again != res.HTML {
			t.Fatalf("sanitize changed the result for %q:\n%q\n%q", body, res.HTML, again)
		}
		if res.WordCount <= 0 {
			t.Fatalf("word count %d without an error for %q", res.WordCount, body)
		}
		if res.SourceURL != final.String() {
			t.Fatalf("source %q, want %q", res.SourceURL, final.String())
		}
	})
}
