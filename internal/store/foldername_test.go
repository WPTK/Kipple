package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Folder names from the Reader API (subscribe, edit, rename-tag) get the web
// UI's limits in the store: 100 characters, no control characters.
func TestFolderNameLimitsInStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)

	require.NoError(t, CheckFolderName(strings.Repeat("é", 100)), "100 characters, not bytes")
	require.NoError(t, CheckFolderName("tab\tok"))
	for _, bad := range []string{strings.Repeat("x", 101), "new\nline", "nul\x00", "del\x7f", "esc\x1b[31m"} {
		require.ErrorIs(t, CheckFolderName(bad), ErrBadFolderName, "%q", bad)
	}

	// A refused label never costs the subscription: the new feed goes to the default folder.
	res, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: strings.Repeat("x", 101)})
	require.NoError(t, err)
	_, found, err := db.FindLabel(ctx, []string{strings.Repeat("x", 101)})
	require.NoError(t, err)
	require.False(t, found)
	var folder int64
	require.NoError(t, db.Reader().QueryRow("SELECT folder_id FROM feeds WHERE id = ?", res.FeedID).Scan(&folder))
	require.Equal(t, int64(1), folder)

	// An existing feed subscribed again under a refused label stays where it is.
	res, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "News"})
	require.NoError(t, err)
	_, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "News/"})
	require.NoError(t, err)
	require.NoError(t, db.Reader().QueryRow("SELECT f.folder_id FROM feeds f WHERE f.id = ?", res.FeedID).Scan(&folder))
	news, _, err := db.FindLabel(ctx, []string{"News"})
	require.NoError(t, err)
	require.Equal(t, news, folder)
	_, err = db.EditSubscription(ctx, []FeedRef{{ID: res.FeedID}}, EditOpts{Folder: "bad\x01name", SetFolder: true})
	require.ErrorIs(t, err, ErrBadFolderName)

	id, found, err := db.FindLabel(ctx, []string{"News"})
	require.NoError(t, err)
	require.True(t, found)
	require.ErrorIs(t, renameLabel(ctx, db, id, strings.Repeat("y", 101)), ErrBadFolderName)
	require.ErrorIs(t, renameLabel(ctx, db, id, "a\x7fb"), ErrBadFolderName)
	names, err := db.FolderNames(ctx)
	require.NoError(t, err)
	require.Contains(t, names, "News", "unchanged")
	require.NoError(t, renameLabel(ctx, db, id, strings.Repeat("z", 100)))
}
