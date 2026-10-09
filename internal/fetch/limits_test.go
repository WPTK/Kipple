package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/stretchr/testify/require"
)

// nestingCases are the documents the nesting tests build: a format, optionally with a leading NUL byte or a stray
// control byte, which the parser drops before it reads the document.
var nestingCases = []string{"rss", "atom", "rss+nul", "atom+nul", "rss+ctl", "atom+ctl"}

// deepDoc is a feed whose first entry holds n unclosed namespaced elements, one inside the next.
func deepDoc(format string, n int) []byte {
	var b strings.Builder
	format, extra, _ := strings.Cut(format, "+")
	if extra == "nul" {
		b.WriteByte(0)
	}
	switch format {
	case "rss":
		b.WriteString(`<?xml version="1.0"?><rss version="2.0" xmlns:x="urn:x"><channel><title>t</title><item><title>i</title>`)
	case "atom":
		b.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:x="urn:x"><title>t</title><entry><title>e</title>`)
	}
	if extra == "ctl" {
		b.WriteByte(1)
	}
	b.WriteString(strings.Repeat("<x:a>", n))
	return []byte(b.String())
}

// A document nested millions of levels deep must be refused before the parser walks it. The parse runs in a child
// process: if the depth reached the parser, the stack would overflow and take the whole test binary down with it.
func TestParseRefusesDeepNesting(t *testing.T) {
	if format := os.Getenv("KIPPLE_DEEP_NESTING_CHILD"); format != "" {
		_, err := ParseFeed(deepDoc(format, 2_100_000), ParseOptions{FeedURL: "https://example.com/feed"})
		if err == nil {
			t.Fatal("a deeply nested document parsed")
		}
		return
	}
	for _, format := range nestingCases {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestParseRefusesDeepNesting$", "-test.count=1")
			cmd.Env = append(os.Environ(), "KIPPLE_DEEP_NESTING_CHILD="+format)
			out, err := cmd.CombinedOutput()
			if err != nil {
				tail := string(out)
				if len(tail) > 2000 {
					tail = tail[:2000]
				}
				t.Fatalf("child process: %v\n%s", err, tail)
			}
		})
	}
}

// closedDoc is deepDoc with every element closed: a well-formed document.
func closedDoc(format string, n int) []byte {
	base, _, _ := strings.Cut(format, "+")
	tail := map[string]string{"rss": `</item></channel></rss>`, "atom": `</entry></feed>`}[base]
	return []byte(string(deepDoc(format, n)) + strings.Repeat("</x:a>", n) + tail)
}

func TestParseNestingLimitBoundary(t *testing.T) {
	for _, format := range nestingCases {
		// Ordinary markup nests a handful of levels; the limit leaves wide room for it.
		f, err := ParseFeed(closedDoc(format, 100), ParseOptions{})
		require.NoError(t, err, format)
		require.Equal(t, 1, len(f.Items), format)

		_, err = ParseFeed(closedDoc(format, 600), ParseOptions{})
		require.Error(t, err, format)
		require.Contains(t, err.Error(), "nest", format)
	}
}

func manyItems(n int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`)
	for i := range n {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>item %d</title><description>body %d</description></item>`, i, i, i)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// datedItems is a feed of n entries; entry i is dated i days after a fixed day, so a larger i is newer. Entries are
// listed oldest first when asc is set, newest first otherwise.
func datedItems(n int, asc bool) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`)
	for k := range n {
		i := k
		if !asc {
			i = n - 1 - k
		}
		d := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>item %d</title><pubDate>%s</pubDate><description>body %d</description></item>`,
			i, i, d.Format(time.RFC1123Z), i)
	}
	b.WriteString(`</channel></rss>`)
	return []byte(b.String())
}

// A feed with more entries than any retention setting keeps is cut to its newest entries (the order retention trims
// by), whatever order it lists them in, before any entry is converted or sanitized. The kept entries stay in
// document order.
func TestParseKeepsNewestItemsUpToLimit(t *testing.T) {
	const limit, extra = MaxItemsPerFetch, 500
	for _, asc := range []bool{false, true} {
		var sanitized atomic.Int64
		f, err := ParseFeed(datedItems(limit+extra, asc), ParseOptions{
			FeedURL: "https://example.com/feed",
			Content: func(raw string, _ ...string) (string, string) {
				sanitized.Add(1)
				return raw, raw
			},
		})
		require.NoError(t, err)
		require.Equal(t, limit, len(f.Items), asc)
		first, last := fmt.Sprintf("item %d", limit+extra-1), fmt.Sprintf("item %d", extra)
		if asc {
			first, last = last, first
		}
		require.Equal(t, first, f.Items[0].Title, asc)
		require.Equal(t, last, f.Items[limit-1].Title, asc)
		require.LessOrEqual(t, sanitized.Load(), int64(limit), asc)
		require.Contains(t, f.Notes, fmt.Sprintf("items_over_limit: kept %d of %d", limit, limit+extra), asc)
	}

	// Undated entries count as new (retention stamps them with the fetch time), and among equal dates the earlier
	// entry in the document wins (the store gives it the larger id).
	f, err := ParseFeed(manyItems(limit+500), ParseOptions{})
	require.NoError(t, err)
	require.Equal(t, limit, len(f.Items))
	require.Equal(t, "item 0", f.Items[0].Title)
	require.Equal(t, fmt.Sprintf("item %d", limit-1), f.Items[limit-1].Title)

	f, err = ParseFeed(manyItems(limit), ParseOptions{})
	require.NoError(t, err)
	require.Equal(t, limit, len(f.Items))
	require.Empty(t, f.Notes)
}

// Ties on the capped date go as the store's ids would: the later uncapped date first, an undated entry (inserted
// last) before a dated one, then the entry earlier in the document.
func TestNewestEntriesBreaksTiesAsRetention(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *gofeed.Item { t := now.Add(d); return &gofeed.Item{PublishedParsed: &t} }
	require.Equal(t, []int{1}, newestEntries([]*gofeed.Item{at(240 * time.Hour), at(480 * time.Hour), at(-time.Hour)}, 1, now))
	require.Equal(t, []int{1}, newestEntries([]*gofeed.Item{at(0), {}}, 1, now))
	require.Equal(t, []int{0}, newestEntries([]*gofeed.Item{{}, {}}, 1, now))
	require.Equal(t, []int{0}, newestEntries([]*gofeed.Item{at(0), at(0)}, 1, now))
}

// Unclosed inline tags in raw (neither escaped nor CDATA) markup are common in the wild. The parser closes them at
// the next end tag, so they add no nesting.
func TestParseAcceptsUnclosedInlineTags(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>t</title>`)
	for i := range 60 {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>item %d</title><description><p>a%s</p></description></item>`,
			i, i, strings.Repeat("<br>b", 10))
	}
	b.WriteString(`</channel></rss>`)
	f, err := ParseFeed([]byte(b.String()), ParseOptions{FeedURL: "https://example.com/feed"})
	require.NoError(t, err)
	require.Equal(t, 60, len(f.Items))
}

// Entity declarations in a feed are inert: nothing is expanded, nothing outside the document is read or fetched,
// and the parse ends quickly.
func TestParseLeavesEntitiesUnexpanded(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("fetched-entity-body"))
	}))
	defer srv.Close()

	laughs := `<!ENTITY a "lollollollollollollollollollol">`
	for i := 'b'; i <= 'j'; i++ {
		laughs += fmt.Sprintf(`<!ENTITY %c "%s">`, i, strings.Repeat(fmt.Sprintf("&%c;", i-1), 10))
	}
	dtd := `<!DOCTYPE d [` + laughs +
		`<!ENTITY ext SYSTEM "` + srv.URL + `/ext"><!ENTITY file SYSTEM "file:///etc/passwd">` +
		`<!ENTITY % param SYSTEM "` + srv.URL + `/param"> %param;]>`
	refs := `&j; &ext; &file;`
	docs := map[string]string{
		"rss": `<?xml version="1.0"?>` + dtd + `<rss version="2.0"><channel><title>t ` + refs + `</title>` +
			`<item><guid>g</guid><title>i ` + refs + `</title><description>d ` + refs + `</description></item></channel></rss>`,
		"atom": `<?xml version="1.0"?>` + dtd + `<feed xmlns="http://www.w3.org/2005/Atom"><title>t ` + refs + `</title>` +
			`<entry><id>g</id><title>e ` + refs + `</title><content>c ` + refs + `</content></entry></feed>`,
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://example.com/feed"})
			require.Less(t, time.Since(start), 5*time.Second)
			if err == nil {
				for _, it := range f.Items {
					all := it.Title + it.ContentHTML + it.ContentText
					require.Less(t, strings.Count(all, "lol"), 100)
					require.NotContains(t, all, "fetched-entity-body")
					require.NotContains(t, all, "root:")
				}
			}
			require.Zero(t, hits.Load())
		})
	}
}

// A charset declaration inside the document changes how the parser reads the element names that follow it, so the
// nesting check must read them the same way. In Big5 the bytes A2CE and A4CA both decode to one character, so the two
// names below differ as raw bytes and are equal as the parser reads them.
func TestParseNestingCheckReadsDeclaredCharset(t *testing.T) {
	if os.Getenv("KIPPLE_DEEP_NESTING_CHILD") != "" {
		return
	}
	n1, n2 := "\xe4\xb8\xa4\xca\xa4\x61", "\xe4\xb8\xa2\xce\xa4\x61"
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0" xmlns:x="urn:x"><channel><title>t</title><item><title>i</title><x:ext>`)
	b.WriteString(`<?xml version="1.0" encoding="big5"?>`)
	for range 600 {
		b.WriteString("<x:" + n2 + "><x:" + n1 + "></x:" + n2 + ">")
	}
	b.WriteString("</x:ext></item></channel></rss>")
	_, err := ParseFeed([]byte(b.String()), ParseOptions{FeedURL: "https://example.com/feed"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nest")
}
