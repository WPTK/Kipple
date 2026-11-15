package opml

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// allocated is the bytes f allocates in all. It reads process-wide statistics, so the tests that use
// it stay serial (the parallel tests wait until every serial one has finished).
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A folder name has no length limit in a file, so what Parse keeps for each outline under one must not
// grow with the names above it: a 256 KiB name over many outlines costs a few MiB, not hundreds.
func TestParseLongFolderNameCostsOncePerFile(t *testing.T) {
	name := strings.Repeat("a", 256<<10)
	var b strings.Builder
	b.WriteString(`<opml><body><outline text="` + name + `">`)
	for range 7 {
		b.WriteString(`<outline text="b">`)
	}
	for i := range 400 {
		fmt.Fprintf(&b, `<outline text="s%d"/>`, i)
	}
	b.WriteString(strings.Repeat(`</outline>`, 8) + `</body></opml>`)
	in := b.String()

	var doc *Doc
	n := allocated(func() {
		var err error
		doc, err = parse(strings.NewReader(in))
		require.NoError(t, err)
	})
	require.Len(t, doc.FoldersRefused, 1, "the long name is refused once, at its top")
	require.Equal(t, refusal(store.ErrBadFolderName), doc.FoldersRefused[0].Reason)
	require.Less(t, len(doc.FoldersRefused[0].Path), 300, "and its path is cut in the report")
	require.Empty(t, doc.Folders)
	require.Less(t, n, uint64(16<<20), "bytes allocated for a 256 KiB name over 400 outlines")
}

// retained is the heap f leaves in use once a collection has run, while its result is still held.
func retained(f func() any) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	keep := f()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	return uint64(max(int64(after.HeapAlloc)-int64(before.HeapAlloc), 0))
}

// Wide rather than long: eight 100-character names of four-byte characters, then thousands of distinct
// siblings below the deepest level. What Parse keeps is the report, which lists a bounded number of
// refused folders and counts the rest, so it does not grow with the file.
func TestParseWideFileKeepsABoundedReport(t *testing.T) {
	const siblings = 20000
	var b strings.Builder
	b.WriteString(`<opml><body>`)
	for i := range 8 {
		fmt.Fprintf(&b, `<outline text="%d%s">`, i, strings.Repeat("𐀀", 96))
	}
	for i := range siblings {
		fmt.Fprintf(&b, `<outline text="s%d"/>`, i)
	}
	b.WriteString(strings.Repeat(`</outline>`, 8) + `</body></opml>`)
	in := b.String()

	kept := retained(func() any {
		doc, err := parse(strings.NewReader(in))
		require.NoError(t, err)
		require.Len(t, doc.Folders, 8)
		require.Len(t, doc.FoldersRefused, maxReportedFolders)
		require.Equal(t, siblings-maxReportedFolders, doc.FoldersRefusedMore)
		return doc
	})
	require.Less(t, kept, uint64(2<<20), "heap kept by the parsed document")
}

// The report of merged spellings is bounded the same way.
func TestParseManyCaseVariantsKeepsABoundedReport(t *testing.T) {
	const pairs = 5000
	var b strings.Builder
	b.WriteString(`<opml><body>`)
	for i := range pairs {
		fmt.Fprintf(&b, `<outline text="ab%d"/><outline text="AB%d"/>`, i, i)
	}
	b.WriteString(`<outline xmlUrl="https://a.test/f"/></body></opml>`)
	doc, err := parse(strings.NewReader(b.String()))
	require.NoError(t, err)
	require.Len(t, doc.Folders, pairs)
	require.Len(t, doc.FoldersMergedCase, maxReportedFolders)
	require.Equal(t, pairs-maxReportedFolders, doc.FoldersMergedCaseMore)
}
