package greader

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// itemJSON is the whole item as a client decodes it (writeItem writes it in parts).
type itemJSON struct {
	ID            string          `json:"id"`
	CrawlTimeMsec string          `json:"crawlTimeMsec"`
	TimestampUsec string          `json:"timestampUsec"`
	Published     int64           `json:"published"`
	Updated       int64           `json:"updated"`
	Title         string          `json:"title"`
	Author        string          `json:"author"`
	Canonical     []hrefJSON      `json:"canonical"`
	Alternate     []altJSON       `json:"alternate"`
	Summary       summaryJSON     `json:"summary"`
	Content       summaryJSON     `json:"content"`
	Categories    []string        `json:"categories"`
	Origin        originJSON      `json:"origin"`
	Enclosure     []enclosureJSON `json:"enclosure,omitempty"`
}

// bigRow is an item whose article is articleBytes of HTML with characters JSON must escape.
func bigRow(articleBytes int) *store.ContentRow {
	unit := `<p>A "quoted" line & a <a href="https://example.org/x?a=1&b=2">link</a>, ünïcode.</p>` + "\n"
	html := strings.Repeat(unit, articleBytes/len(unit)+1)[:articleBytes]
	return &store.ContentRow{ID: 1758700000123456, FeedID: 3, URL: "https://example.org/a", Title: "T", Author: "A",
		HTML: html, Published: 1758690000, Folder: "Tech", FeedTitle: "Feed", SiteURL: "https://example.org/",
		Enclosures: `[{"url":"https://example.org/a.mp3","type":"audio/mpeg","length":12}]`, Updated: sql.NullInt64{}}
}

// summary.content and content.content are the same bytes, and the item is valid JSON with every field.
func TestItemContentFieldsIdentical(t *testing.T) {
	for _, n := range []int{0, 10, 40_000, contentCap + 1000} {
		var buf bytes.Buffer
		bw := bufio.NewWriter(&buf)
		require.NoError(t, writeItem(bw, &bytes.Buffer{}, bigRow(n)))
		require.NoError(t, bw.Flush())
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(buf.Bytes(), &raw), "valid JSON for %d bytes", n)
		require.Equal(t, string(raw["summary"]), string(raw["content"]), n)
		var it itemJSON
		require.NoError(t, json.Unmarshal(buf.Bytes(), &it))
		require.Equal(t, it.Summary, it.Content)
		require.Equal(t, truncateUTF8(bigRow(n).HTML, contentCap), it.Content.Content)
		for _, k := range []string{"id", "crawlTimeMsec", "timestampUsec", "published", "updated", "title", "author",
			"canonical", "alternate", "categories", "origin"} {
			require.Contains(t, raw, k, n)
		}
	}
}

// BenchmarkWriteItem500K measures the allocation per item for a 500 KB article (docs/performance.md).
func BenchmarkWriteItem500K(b *testing.B) {
	r := bigRow(500_000)
	bw := bufio.NewWriterSize(io.Discard, 32<<10)
	var scratch bytes.Buffer
	b.ReportAllocs()
	for b.Loop() {
		if err := writeItem(bw, &scratch, r); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWriteItem500KFirst is the first item of a response: the scratch buffer is new.
func BenchmarkWriteItem500KFirst(b *testing.B) {
	r := bigRow(500_000)
	bw := bufio.NewWriterSize(io.Discard, 32<<10)
	b.ReportAllocs()
	for b.Loop() {
		if err := writeItem(bw, &bytes.Buffer{}, r); err != nil {
			b.Fatal(err)
		}
	}
}