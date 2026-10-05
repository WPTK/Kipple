package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The first successful fetch of a page address that linked a feed (fetch.Result.Discovered) makes
// that feed the feed's URL. The page stays in url_original, so subscribing the same page again finds
// this feed instead of adding a second one.
func TestCommitAdoptsDiscoveredFeedURL(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	res := e.okResult(e.snap(id), rss(numbered(2)...))
	res.Discovered, res.FinalURL = "https://blog.example/feed.xml", "https://blog.example/feed.xml"
	ci := e.commit(res)
	require.True(t, ci.Migrated)
	require.Equal(t, "https://blog.example/feed.xml", ci.URL)
	require.Equal(t, 2, ci.New)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://blog.example/feed.xml'
		AND url_key = 'blog.example/feed.xml' AND host = 'blog.example'
		AND url_original = 'https://blog.example/' AND url_original_key = 'blog.example/' AND last_success_at IS NOT NULL`, id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE '%discovered: https://blog.example/ -> https://blog.example/feed.xml%'"))

	for _, u := range []string{"https://blog.example/", "http://blog.example/feed.xml"} {
		got, found, err := e.db.FindFeedID(e.ctx, u)
		require.NoError(t, err)
		require.True(t, found, u)
		require.Equal(t, id, got, u)
	}
	res2, err := e.db.Subscribe(e.ctx, SubscribeOpts{URL: "blog.example/"})
	require.NoError(t, err)
	require.True(t, res2.Existed, "the page address subscribes the same feed")
	require.Equal(t, id, res2.FeedID)
}

// When another feed already has the discovered URL, the page's feed (never fetched, so empty) is
// removed: one feed, no duplicate. Nothing else is written.
func TestCommitOfDiscoveredDuplicateRemovesTheNewFeed(t *testing.T) {
	e := newEnv(t)
	kept := e.addFeed("https://blog.example/feed.xml")
	page := e.addFeed("https://blog.example/")
	res := e.okResult(e.snap(page), rss(numbered(2)...))
	res.Discovered = "http://blog.example/feed.xml" // http vs https: the same feed
	ci := e.commit(res)
	require.Equal(t, kept, ci.MergedInto)
	require.True(t, ci.Migrated)
	require.Empty(t, ci.URL)
	require.Zero(t, ci.New)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ?", page))
	require.Zero(t, e.count("SELECT count(*) FROM items"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://blog.example/feed.xml'", kept))
}

// A URL edit that lands while the page fetch is in flight wins: nothing is adopted.
func TestDiscoveredFeedIsStaleAfterAURLEdit(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.Discovered = "https://blog.example/feed.xml"
	e.exec("UPDATE feeds SET url = 'https://other.example/rss' WHERE id = ?", id)
	ci := e.commit(res)
	require.True(t, ci.Stale)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://other.example/rss' AND url_original IS NULL", id))
}

// A chunked commit checks the later chunks against the adopted URL, not the page.
func TestDiscoveredFeedCommitsAllChunks(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	res := e.okResult(e.snap(id), rss(numbered(chunkThreshold+10)...))
	res.Discovered = "https://blog.example/feed.xml"
	ci := e.commit(res)
	require.False(t, ci.Stale)
	require.Equal(t, chunkThreshold+10, ci.New)
	require.Equal(t, "https://blog.example/feed.xml", ci.URL)
}
