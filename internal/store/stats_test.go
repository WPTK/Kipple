package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A stats row names the feed the way the rest of the app does: the custom title, else the stored
// title, else the URL, with a blank title counting as absent.
func TestStatSnapshotsUseTheFeedDisplayTitle(t *testing.T) {
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
