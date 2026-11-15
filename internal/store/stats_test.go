package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// A stats row names the feed the way the rest of the app does: the custom title, else the stored
// title, else the URL, with a blank title counting as absent.
func TestStatSnapshotsUseTheFeedDisplayTitle(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("http://stats.example/feed")
	e.fetchBody(id, frss(fspec{guid: "s1", title: "one"}))
	var item int64
	require.NoError(t, e.db.Reader().QueryRow("SELECT id FROM items WHERE feed_id = ?", id).Scan(&item))

	title := func() (string, string) {
		s, ok, err := StatItemSnapshot(e.ctx, e.db.Reader(), item)
		require.NoError(t, err)
		require.True(t, ok)
		f, err := StatFeedSnapshot(e.ctx, e.db.Reader(), id)
		require.NoError(t, err)
		return s.FeedTitle, f.FeedTitle
	}
	for _, tc := range []struct {
		custom, stored any
		want           string
	}{
		{nil, "Stored", "Stored"},
		{"Mine", "Stored", "Mine"},
		{"", "Stored", "Stored"},
		{" ", "", "http://stats.example/feed"},
		{nil, "", "http://stats.example/feed"},
	} {
		e.exec("UPDATE feeds SET custom_title = ?, title = ? WHERE id = ?", tc.custom, tc.stored, id)
		a, b := title()
		require.Equal(t, tc.want, a, "item snapshot, custom %v stored %v", tc.custom, tc.stored)
		require.Equal(t, tc.want, b, "feed snapshot, custom %v stored %v", tc.custom, tc.stored)
	}
}

// A stats snapshot names a nested folder by its full path, reads a gone folder as none, and one
// statement writes a row per live item of a feed.
func TestStatSnapshotFolderPathAndBulkRows(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("http://stats.example/nested")
	e.fetchBody(id, frss(fspec{guid: "n1", title: "one"}, fspec{guid: "n2", title: ""}))
	e.exec("INSERT INTO folders (id, parent_id, name, position) VALUES (11, 1, 'News', 1)")
	e.exec("UPDATE folders SET parent_id = NULL WHERE id = 11")
	e.exec("INSERT INTO folders (id, parent_id, name, position) VALUES (12, 11, 'World', 1)")
	e.exec("UPDATE feeds SET folder_id = 12 WHERE id = ?", id)

	f, err := StatFeedSnapshot(e.ctx, e.db.Reader(), id)
	require.NoError(t, err)
	require.Equal(t, "News/World", f.FolderName.String)
	require.True(t, f.FolderName.Valid)

	var ids []int64
	rows, err := e.db.Reader().Query("SELECT id FROM items WHERE feed_id = ? ORDER BY id", id)
	require.NoError(t, err)
	for rows.Next() {
		var i int64
		require.NoError(t, rows.Scan(&i))
		ids = append(ids, i)
	}
	require.NoError(t, rows.Close())
	require.Len(t, ids, 2)

	s, ok, err := StatItemSnapshot(e.ctx, e.db.Reader(), ids[0])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "News/World", s.FolderName.String)

	byFeed, err := StatItemFeeds(e.ctx, e.db.Reader(), append(ids, 999999))
	require.NoError(t, err)
	require.Equal(t, map[int64][]int64{id: ids}, byFeed)

	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return InsertItemStats(ctx, tx, StatRow{TS: 5, LocalDate: "2026-01-02", Kind: "star", Client: "pwa",
			FeedID: id, FeedTitle: f.FeedTitle, FolderID: f.FolderID, FolderName: f.FolderName}, ids)
	}))
	require.Equal(t, 2, e.count("SELECT count(*) FROM stats_events WHERE kind = 'star' AND folder_name = 'News/World' AND feed_id = ?", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM stats_events WHERE item_title = 'one' AND item_id = ?", ids[0]))

	e.exec("DELETE FROM folders WHERE id = 11")
	g, err := StatFeedSnapshot(e.ctx, e.db.Reader(), id)
	require.NoError(t, err)
	require.EqualValues(t, 1, g.FolderID.Int64, "the feed falls back to the default folder")
	require.True(t, g.FolderName.Valid)
	require.NotContains(t, g.FolderName.String, "/")
}
