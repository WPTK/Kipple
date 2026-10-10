package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func (e *env) discovered(id int64, link string) *fetch.Result {
	e.t.Helper()
	return &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeOK, Status: 200,
		FinalURL: e.snap(id).URL, Discovered: link}
}

func (e *env) commitDiscovered(res *fetch.Result) CommitInfo {
	e.t.Helper()
	ci, err := e.db.CommitDiscovered(e.ctx, res)
	require.NoError(e.t, err)
	return ci
}

// A page address that linked a feed becomes that feed, due at once; the page stays in url_original
// so subscribing the same page again finds this feed.
func TestCommitDiscoveredAdoptsTheFeedURL(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	e.exec("UPDATE feeds SET next_fetch_at = 99999999999, etag = 'x', body_hash = 'y' WHERE id = ?", id)
	ci := e.commitDiscovered(e.discovered(id, "https://blog.example/feed.xml"))
	require.True(t, ci.Migrated)
	require.Equal(t, "https://blog.example/feed.xml", ci.URL)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://blog.example/feed.xml'
		AND url_key = 'blog.example/feed.xml' AND host = 'blog.example' AND url_original = 'https://blog.example/'
		AND url_original_key = 'blog.example/' AND last_success_at IS NULL AND etag IS NULL AND body_hash IS NULL
		AND next_fetch_at = ?`, id, e.clk.Now().Unix()))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE outcome = 'ok' AND keep = 1 AND note = 'discovered: https://blog.example/ -> https://blog.example/feed.xml'"))
	require.True(t, e.snap(id).URLChanged, "the feed is never discovered again")

	for _, u := range []string{"https://blog.example/", "http://blog.example/feed.xml"} {
		got, found, err := e.db.FindFeedID(e.ctx, u)
		require.NoError(t, err)
		require.True(t, found, u)
		require.Equal(t, id, got, u)
	}
	res2, err := e.db.Subscribe(e.ctx, SubscribeOpts{URL: " https://Blog.example/#top "})
	require.NoError(t, err)
	require.True(t, res2.Existed, "the page address subscribes the same feed")
	require.Equal(t, id, res2.FeedID)
}

// HTTP credentials never follow a change of host, nor network exceptions a change of site.
func TestCommitDiscoveredDropsCredentialsOffTheirHost(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("https://example.com/")
	e.exec("UPDATE feeds SET http_auth = 'bob:pw', allow_private_net = 1, allow_insecure_tls = 1 WHERE id = ?", id)
	e.commitDiscovered(e.discovered(id, "https://feeds.example.com/rss"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND http_auth IS NULL AND allow_private_net = 1 AND allow_insecure_tls = 1", id),
		"another host of the same site: the login goes, the exceptions stay")

	id2 := e.addFeed("https://example.org/")
	e.exec("UPDATE feeds SET http_auth = 'bob:pw', allow_private_net = 1 WHERE id = ?", id2)
	e.commitDiscovered(e.discovered(id2, "https://example.org/rss"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND http_auth = 'bob:pw' AND allow_private_net = 1", id2), "same host: kept")

	id3 := e.addFeed("https://example.net/")
	e.exec("UPDATE feeds SET allow_private_net = 1 WHERE id = ?", id3)
	e.commitDiscovered(e.discovered(id3, "https://other.example.io/rss"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND allow_private_net = 0", id3), "another site: dropped")
}

// When another feed already has the discovered URL, the new feed (empty) is removed, and the kept
// feed takes its folder and custom title as a subscribe of an existing feed would.
func TestCommitDiscoveredDuplicateMergesIntoTheKeptFeed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	kept := e.addFeed("https://blog.example/feed.xml")
	page := e.addFeed("https://blog.example/")
	e.exec("INSERT INTO folders (id, name, position) VALUES (7, 'Tech', 1)")
	e.exec("UPDATE feeds SET folder_id = 7, custom_title = 'My blog' WHERE id = ?", page)
	ci := e.commitDiscovered(e.discovered(page, "http://blog.example/feed.xml")) // http vs https: the same feed
	require.Equal(t, kept, ci.MergedInto)
	require.True(t, ci.Migrated)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ?", page))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND folder_id = 7 AND custom_title = 'My blog' AND url = 'https://blog.example/feed.xml'", kept))

	// A page in the default folder with no title leaves the kept feed where and as it is; a disabled
	// kept feed stays disabled (as a subscribe of it would leave it).
	other := e.addFeed("https://news.example/rss")
	e.exec("UPDATE feeds SET folder_id = 7, custom_title = 'News', enabled = 0, disabled_reason = 'user' WHERE id = ?", other)
	page2 := e.addFeed("https://news.example/")
	ci = e.commitDiscovered(e.discovered(page2, "https://news.example/rss"))
	require.Equal(t, other, ci.MergedInto)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND folder_id = 7 AND custom_title = 'News' AND enabled = 0", other))
}

// A URL edit that lands while the page fetch is in flight wins: nothing is adopted.
func TestCommitDiscoveredIsStaleAfterAURLEdit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("https://blog.example/")
	res := e.discovered(id, "https://blog.example/feed.xml")
	e.exec("UPDATE feeds SET url = 'https://other.example/rss' WHERE id = ?", id)
	ci := e.commitDiscovered(res)
	require.True(t, ci.Stale)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://other.example/rss' AND url_original IS NULL", id))
}
