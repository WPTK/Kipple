package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A Reader API rename onto an existing folder's name merges the folders; the
// old folder's filters follow its feeds instead of cascading away.
func TestRenameMergeKeepsFolderFilters(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	_, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "Old"})
	require.NoError(t, err)
	_, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "New"})
	require.NoError(t, err)
	oldID, _, err := db.FindLabel(ctx, []string{"Old"})
	require.NoError(t, err)
	newID, _, err := db.FindLabel(ctx, []string{"New"})
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
		VALUES ('f', 1, 'folder', ?, 'text', '["sponsored"]', '["title"]', 'mute')`, oldID)
	require.NoError(t, err)

	require.NoError(t, db.RenameLabel(ctx, oldID, "New"))
	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE folder_id = ?", newID).Scan(&n))
	require.Equal(t, 1, n, "the filter moved to the merged folder")
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM folders WHERE id = ?", oldID).Scan(&n))
	require.Zero(t, n)
}
