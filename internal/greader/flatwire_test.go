package greader

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A library without nested folders puts the same bytes on the wire as before folders could nest:
// testdata/flat_wire.golden was written by this test on the release before nesting (with
// KIPPLE_UPDATE_GOLDEN=1), and rewritten only when items gained content.content beside
// summary.content (the one intended difference in those lines). The fixture covers the cases the folder order and label strings could get
// wrong: two folders at the same position (ordered by name, ignoring case), names holding '/', '+',
// '&' and non-ASCII letters, an empty folder, the default folder, a disabled feed and an archived
// (unsubscribed, starred) item.
func TestFlatLibraryWireOutputUnchanged(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	folders := []struct {
		id       int64
		name     string
		position int64
	}{{2, "News & Politics+", 3}, {3, "AC/DC", 1}, {4, "beta", 2}, {5, "Alpha", 2}, {6, "Café", 5}, {7, "Empty", 4}}
	for _, f := range folders {
		require.NoError(t, execSQL(h, "INSERT INTO folders (id, name, position) VALUES (?, ?, ?)", f.id, f.name, f.position))
	}
	feeds := []struct {
		id     int64
		folder int64
		title  string
	}{{1, 2, "Politics"}, {2, 3, "Rock"}, {3, 4, "B one"}, {4, 5, "A one"}, {5, 5, "A two"}, {6, 6, "Coffee"}, {7, 1, "Loose"}, {8, 4, "B off"}}
	for _, f := range feeds {
		require.NoError(t, execSQL(h, `INSERT INTO feeds (id, folder_id, url, url_key, host, title, site_url, position, next_fetch_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 4102444800)`, f.id, f.folder, fmt.Sprintf("https://f%d.example/feed", f.id),
			fmt.Sprintf("f%d.example/feed", f.id), fmt.Sprintf("f%d.example", f.id), f.title, fmt.Sprintf("https://f%d.example/", f.id), 10-f.id))
	}
	require.NoError(t, execSQL(h, "UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = 8"))
	for i, feed := range []int64{1, 1, 2, 3, 4, 5, 5, 6, 7, 8} {
		h.addItem(feed, itemSeed{ID: baseID + int64(i+1)*1000, Title: fmt.Sprintf("item %d", i+1), Read: i%3 == 2, Starred: i == 4})
	}
	require.NoError(t, execSQL(h, `INSERT INTO feeds (id, folder_id, url, url_key, host, title, enabled, disabled_reason, retention)
		VALUES (9, 1, 'kipple:archive', 'kipple:archive', 'kipple.invalid', 'Unsubscribed (starred)', 0, 'archive', 0)`))
	h.addItem(9, itemSeed{ID: baseID + 20000, Title: "archived", Starred: true})

	var out bytes.Buffer
	for _, p := range []string{
		rd + "subscription/list?output=json",
		rd + "tag/list?output=json",
		rd + "unread-count?output=json",
		rd + "stream/items/ids?output=json&n=100&s=user/-/label/Alpha",
		rd + "stream/items/ids?output=json&n=100&s=user/-/label/AC%2FDC",
		rd + "stream/contents/user/-/label/News%20%26%20Politics%2B?output=json&n=100",
		rd + "stream/contents/user/-/state/com.google/reading-list?output=json&n=100",
	} {
		w := h.get(p)
		require.Equal(t, http.StatusOK, w.Code, p)
		fmt.Fprintf(&out, "== %s\nETag: %s\n%s\n", p, w.Header().Get("ETag"), w.Body.String())
	}
	golden := filepath.Join("testdata", "flat_wire.golden")
	if os.Getenv("KIPPLE_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, out.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), out.String())
}
