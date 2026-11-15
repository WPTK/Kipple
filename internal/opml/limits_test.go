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

	// Too deep rather than too long: each outline below the deepest level costs its own name, not the
	// eight names above it.
	b.Reset()
	b.WriteString(`<opml><body>`)
	for range 8 {
		b.WriteString(`<outline text="` + strings.Repeat("b", 90) + `">`)
	}
	for i := range 3000 {
		fmt.Fprintf(&b, `<outline text="s%d"><outline xmlUrl="https://a.test/%d"/></outline>`, i, i)
	}
	b.WriteString(strings.Repeat(`</outline>`, 8) + `</body></opml>`)
	in = b.String()
	n = allocated(func() {
		var err error
		doc, err = parse(strings.NewReader(in))
		require.NoError(t, err)
	})
	require.Len(t, doc.Folders, 8)
	require.Len(t, doc.FoldersRefused, 3000, "each distinct outline below the deepest level is reported once")
	require.Less(t, n, uint64(32<<20), "bytes allocated for 3000 outlines below eight 90-character names")
}
