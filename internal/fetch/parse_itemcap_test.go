package fetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sanitize"
)

// One entry's markup is bounded before the cleaner reads it: an entry over the limit is cut between tags and the
// fetch notes count it; an entry under it passes untouched and leaves no note.
func TestItemHTMLCutBeforeTheCleaner(t *testing.T) {
	big := strings.Repeat("<p>x</p>", sanitize.MaxItemHTMLBytes/8+1000) // a little over the limit
	require.Greater(t, len(big), sanitize.MaxItemHTMLBytes)
	doc := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://e.example/</link>` +
		`<item><title>big</title><guid>1</guid><description><![CDATA[` + big + `]]></description></item>` +
		`<item><title>small</title><guid>2</guid><description><![CDATA[<p>hello</p>]]></description></item>` +
		`</channel></rss>`
	seen := map[string]int{}
	content := func(raw string, _ ...string) (string, string) {
		switch {
		case strings.Contains(raw, "hello"):
			seen["small"] = len(raw)
		default:
			seen["big"] = len(raw)
		}
		return raw, ""
	}
	f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://e.example/feed", Content: content})
	require.NoError(t, err)
	require.Len(t, f.Items, 2)
	require.LessOrEqual(t, seen["big"], sanitize.MaxItemHTMLBytes)
	require.Greater(t, seen["big"], sanitize.MaxItemHTMLBytes-8) // cut at a tag boundary, not far below the limit
	require.Equal(t, len("<p>hello</p>"), seen["small"])
	require.Contains(t, f.Notes, "item_html_cut: 1/2")
	require.True(t, strings.HasSuffix(f.Items[0].ContentHTML, "</p>"), "the cut falls between tags")

	// A feed with nothing over the limit has no note.
	small := strings.Replace(doc, big, "<p>ok</p>", 1)
	f, err = ParseFeed([]byte(small), ParseOptions{FeedURL: "https://e.example/feed", Content: content})
	require.NoError(t, err)
	for _, n := range f.Notes {
		require.NotContains(t, n, "item_html_cut")
	}
}
