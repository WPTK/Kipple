package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeedSnapshotsByIDOneQueryInRequestOrder(t *testing.T) {
	e := newEnv(t)
	a := e.addFeed("https://a.example/feed")
	b := e.addFeed("https://b.example/feed")
	c := e.addFeed("https://c.example/feed")
	e.exec("UPDATE feeds SET enabled = 0 WHERE id = ?", b)

	got, err := e.db.FeedSnapshotsByID(e.ctx, e.db.FetchSettings(e.ctx), []int64{c, 999999, a, b, c})
	require.NoError(t, err)
	var ids []int64
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	require.Equal(t, []int64{c, a, b}, ids, "request order, unknown and repeated ids dropped, disabled kept")
	require.False(t, got[2].Enabled)
	require.Equal(t, "a.example", got[1].Host)

	none, err := e.db.FeedSnapshotsByID(e.ctx, e.db.FetchSettings(e.ctx), nil)
	require.NoError(t, err)
	require.Empty(t, none)
}
