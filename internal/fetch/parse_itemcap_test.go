package fetch

import (
	stdhtml "html"
	"runtime"
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

// Every entry that was cut is counted, whatever becomes of it afterwards: an entry that repeats an earlier one is
// dropped after conversion and still counts.
func TestItemHTMLCutNoteCountsDroppedEntries(t *testing.T) {
	big := strings.Repeat("<p>x</p>", sanitize.MaxItemHTMLBytes/8+1000)
	item := func(guid, body string) string {
		return `<item><title>t</title><guid>` + guid + `</guid><description><![CDATA[` + body + `]]></description></item>`
	}
	doc := `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://e.example/</link>` +
		item("same", big) + item("same", big) + item("other", "<p>hello</p>") + `</channel></rss>`
	f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://e.example/feed"})
	require.NoError(t, err)
	require.Contains(t, f.Notes, "item_html_cut: 2/3")
}

// An entry with no guid and no link is keyed by its title and text. One that fits keeps the key it always had; one
// that is cut gets the same key on every fetch.
func TestFallbackUIDOfEntriesBeyondTheBound(t *testing.T) {
	item := func(body string) string {
		return `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://e.example/</link>` +
			`<item><title>t</title><description><![CDATA[` + body + `]]></description></item></channel></rss>`
	}
	parse := func(body string) Item {
		f, err := ParseFeed([]byte(item(body)), ParseOptions{FeedURL: "https://e.example/feed"})
		require.NoError(t, err)
		require.Len(t, f.Items, 1)
		return f.Items[0]
	}
	small := parse("<p>hello</p>")
	require.Equal(t, "h:"+H("t\x1fhello"), small.UID)

	big := strings.Repeat("<p>word</p>", sanitize.MaxItemHTMLBytes/11+1000)
	a, b := parse(big), parse(big)
	require.NotEmpty(t, a.UID)
	require.Equal(t, a.UID, b.UID)
}

// A long plain-text entry (a JSON Feed content_text) is escaped only as far as the bound: the escaped copy of a long
// text is not held whole. What fits reads exactly as the whole escape would.
func TestTextHTMLStopsAtTheBound(t *testing.T) {
	for _, in := range []string{"", "plain", "a < b & \"c\" > 'd'\nnext", "ünïcode <b>\n\n"} {
		want := "<p>" + strings.ReplaceAll(stdhtml.EscapeString(in), "\n", "<br>") + "</p>"
		require.Equal(t, want, textHTML(in))
	}

	long := strings.Repeat("<", 8<<20) // 32 MiB once escaped
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := textHTML(long)
	runtime.ReadMemStats(&after)
	require.Greater(t, len(got), sanitize.MaxItemHTMLBytes)
	require.Less(t, len(got), sanitize.MaxItemHTMLBytes+8)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20))

	cut, wasCut := sanitize.CutHTML(got, sanitize.MaxItemHTMLBytes)
	require.True(t, wasCut)
	require.LessOrEqual(t, len(cut), sanitize.MaxItemHTMLBytes)
}
