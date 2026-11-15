package opml

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Import keeps what it knows of a chain once per level, not once per folder: thousands of folders under
// a refused name cost no more than their own names, however long the name is.
func TestImportFoldersUnderRefusedChainCostOncePerLevel(t *testing.T) {
	db := openDB(t)
	name := strings.Repeat("a", 64<<10) // over store.MaxFolderNameRunes: the writer refuses it before any query
	doc := &Doc{Feeds: []Feed{{URL: "https://a.test/f", Folder: []string{name, "1"}}}}
	for i := range 1500 {
		doc.Folders = append(doc.Folders, []string{name, strconv.Itoa(i)})
	}
	var r Result
	n := allocated(func() {
		var err error
		r, err = Import(context.Background(), db, doc, ImportOptions{})
		require.NoError(t, err)
	})
	require.Equal(t, 1, r.FeedsAdded)
	require.Len(t, r.FoldersRefused, 1, "the refused name is reported once, at its top")
	require.Equal(t, "Uncategorized", folderOf(t, db, "https://a.test/f"))
	require.Less(t, n, uint64(16<<20), "bytes allocated for 1500 folders under a 64 KiB name")
}
