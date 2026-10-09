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

	"github.com/stretchr/testify/require"
)

// deepDoc is a feed whose first entry holds n unclosed namespaced elements, one inside the next.
func deepDoc(format string, n int) []byte {
	var b strings.Builder
	switch format {
	case "rss":
		b.WriteString(`<?xml version="1.0"?><rss version="2.0" xmlns:x="urn:x"><channel><title>t</title><item><title>i</title>`)
	case "atom":
		b.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:x="urn:x"><title>t</title><entry><title>e</title>`)
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
	for _, format := range []string{"rss", "atom"} {
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
	tail := map[string]string{"rss": `</item></channel></rss>`, "atom": `</entry></feed>`}[format]
	return []byte(string(deepDoc(format, n)) + strings.Repeat("</x:a>", n) + tail)
}

func TestParseNestingLimitBoundary(t *testing.T) {
	for _, format := range []string{"rss", "atom"} {
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

// A feed with more entries than any retention setting keeps is cut to its first entries, in document order, before
// any entry is converted or sanitized.
func TestParseKeepsFirstItemsUpToLimit(t *testing.T) {
	const limit = 2000
	var sanitized atomic.Int64
	f, err := ParseFeed(manyItems(limit+500), ParseOptions{
		FeedURL: "https://example.com/feed",
		Content: func(raw string, _ ...string) (string, string) {
			sanitized.Add(1)
			return raw, raw
		},
	})
	require.NoError(t, err)
	require.Equal(t, limit, len(f.Items))
	require.Equal(t, "item 0", f.Items[0].Title)
	require.Equal(t, fmt.Sprintf("item %d", limit-1), f.Items[limit-1].Title)
	require.LessOrEqual(t, sanitized.Load(), int64(limit))
	require.Contains(t, f.Notes, fmt.Sprintf("items_over_limit: kept %d of %d", limit, limit+500))

	f, err = ParseFeed(manyItems(limit), ParseOptions{})
	require.NoError(t, err)
	require.Equal(t, limit, len(f.Items))
	require.Empty(t, f.Notes)
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
