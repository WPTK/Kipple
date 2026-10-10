package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A feed whose pending redirect points at a feed the user already has says whose it is.
func TestFeedHealthNamesTheOwnerOfARedirectTarget(t *testing.T) {
	e := newEnv(t)
	owner := e.addFeed("https://blog.example/feed.xml")
	e.exec("UPDATE feeds SET title = 'The Blog' WHERE id = ?", owner)
	dup := e.addFeed("https://blog.example/old")
	free := e.addFeed("https://news.example/old")
	e.exec("UPDATE feeds SET redirect_to = 'http://blog.example/feed.xml', redirect_kind = 'permanent', redirect_count = 1 WHERE id = ?", dup)
	e.exec("UPDATE feeds SET redirect_to = 'https://news.example/new', redirect_kind = 'permanent', redirect_count = 1 WHERE id = ?", free)

	list, err := e.db.FeedHealth(e.ctx)
	require.NoError(t, err)
	got := map[int64]*string{}
	for _, h := range list {
		got[h.ID] = h.RedirectOwner
	}
	require.NotNil(t, got[dup])
	require.Equal(t, "The Blog", *got[dup])
	require.Nil(t, got[free])
	require.Nil(t, got[owner])
}
