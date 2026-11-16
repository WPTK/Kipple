package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A run over every enabled feed starts the feed fetched longest ago first and
// a feed never fetched before the rest, so the same subscriptions are not
// always at the front.
func TestEnabledFeedsOldestFetchFirst(t *testing.T) {
	e := newEnv(t)
	a := e.addFeed("http://a.example/feed")
	b := e.addFeed("http://b.example/feed")
	c := e.addFeed("http://c.example/feed")
	d := e.addFeed("http://d.example/feed")
	off := e.addFeed("http://off.example/feed")
	e.exec("UPDATE feeds SET enabled = 0 WHERE id = ?", off)
	e.exec("UPDATE feeds SET last_fetch_at = 300 WHERE id = ?", a)
	e.exec("UPDATE feeds SET last_fetch_at = 100 WHERE id = ?", b)
	e.exec("UPDATE feeds SET last_fetch_at = 100 WHERE id = ?", d)
	// c was never fetched.

	snaps, err := e.db.EnabledFeeds(e.ctx, e.db.FetchSettings(e.ctx))
	require.NoError(t, err)
	var got []int64
	for _, s := range snaps {
		got = append(got, s.ID)
	}
	require.Equal(t, []int64{c, b, d, a}, got)
}
