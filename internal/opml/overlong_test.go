package opml

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/store"
)

// overlongURLs is the hostile overlong-urls file (scripts/maintainers/new-opml-set.ps1) at the size given: a path and
// a query of n characters each, and a 300-character host name. The checked-in fixture is the 3,000-character sample;
// the full set uses 2 MiB, a 4 MB file.
func overlongURLs(n int) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0"><head><title>Hostile</title></head><body>
<outline type="rss" text="Long path" xmlUrl="https://rss.example.com/` + strings.Repeat("p", n) + `.xml"/>
<outline type="rss" text="Long host" xmlUrl="https://` + strings.Repeat("h", 300) + `.example.com/f.xml"/>
<outline type="rss" text="Long query" xmlUrl="https://rss.example.com/f.xml?q=` + strings.Repeat("q", n) + `"/>
</body></opml>
`
}

// A file of overlong addresses stores none of them (issue #470): each is skipped with its reason, the report cuts the
// address, and the database does not grow with the file. Before the cap the full 4 MB file grew a new database from
// 0.6 MB to 37 MB. In the sample the path and the query (3,000 characters) fit; its host name never does.
func TestOverlongURLsStoreNothing(t *testing.T) {
	t.Parallel()
	sample, err := os.ReadFile("testdata/hostile/hostile/overlong-urls.opml")
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		file    string
		added   int
		reasons map[string]int
	}{
		{"sample", string(sample), 2, map[string]int{feedurl.HostTooLongMessage: 1}},
		{"full", overlongURLs(2 << 20), 0, map[string]int{feedurl.TooLongMessage: 2, feedurl.HostTooLongMessage: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db")})
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			before := dirSize(t, dir)

			doc, err := Parse(strings.NewReader(tc.file))
			require.NoError(t, err)
			r, err := Import(context.Background(), db, doc, ImportOptions{})
			require.NoError(t, err)
			require.Equal(t, tc.added, r.FeedsAdded)
			reasons := map[string]int{}
			for _, s := range r.Skipped {
				reasons[s.Reason]++
				require.Less(t, len(s.URL), 300, "the report cuts an overlong address")
			}
			require.Equal(t, tc.reasons, reasons)

			var feeds int
			require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds").Scan(&feeds))
			require.Equal(t, tc.added, feeds)
			require.Less(t, dirSize(t, dir)-before, int64(256<<10), "the database does not grow with the file")
		})
	}
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range es {
		i, err := e.Info()
		require.NoError(t, err)
		n += i.Size()
	}
	return n
}
