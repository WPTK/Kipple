package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Feeds that share a title carry a short address to tell them apart (issue #484); every other feed carries none, so
// the bootstrap does not hold every feed's address.
func TestUIFeedAddressHint(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.addFeed("https://a.example/feed/")
	b := e.addFeed("http://b.example/rss?token=secret")
	c := e.addFeed("https://c.example/x?tag=one")
	d := e.addFeed("https://c.example/x?tag=two")
	other := e.addFeed("https://other.example/feed")
	e.exec("UPDATE feeds SET custom_title = 'News' WHERE id = ?", a)
	e.exec("UPDATE feeds SET custom_title = ' news ' WHERE id = ?", b)
	e.exec("UPDATE feeds SET custom_title = 'Tagged' WHERE id IN (?, ?)", c, d)
	e.exec("UPDATE feeds SET custom_title = 'Alone' WHERE id = ?", other)

	feeds, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.NoError(t, err)
	got := map[int64]*string{}
	for _, f := range feeds {
		got[f.ID] = f.AddressHint
	}
	hint := func(id int64) string {
		require.NotNil(t, got[id], "feed %d has a hint", id)
		return *got[id]
	}
	require.Equal(t, "a.example/feed", hint(a))
	require.Equal(t, "b.example/rss", hint(b), "no scheme, and no query when host and path tell them apart")
	require.Equal(t, "c.example/x?tag=one", hint(c), "the query only where it is what tells them apart")
	require.Equal(t, "c.example/x?tag=two", hint(d))
	require.Nil(t, got[other], "a title nobody else has carries no address")
}
