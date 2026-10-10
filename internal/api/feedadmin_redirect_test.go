package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// An address that redirects to a feed the user already has is refused, naming that feed and its
// folder, instead of creating a second copy of it.
func TestAddFeedRefusesAddressRedirectingToExistingFeed(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	folder := h.addFolder("News")
	id := h.storeFeed(srv+"/feed.xml", func(nf *store.NewFeed) { nf.FolderID = folder })
	h.exec("UPDATE feeds SET title = 'My News' WHERE id = ?", id)
	before := h.count("SELECT count(*) FROM feeds")

	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/moved"}))
	require.Equal(t, 409, code, body)
	require.Equal(t, "feed_exists", body["error"])
	require.Equal(t, sid(id), body["feed_id"])
	require.Equal(t, "You already have this feed: My News in the folder News. The address you entered redirects to it.", body["message"])
	require.Equal(t, before, h.count("SELECT count(*) FROM feeds"), "no duplicate is created")
	require.Empty(t, h.sched.submitted(), "nothing is fetched for a refused address")

	t.Run("a feed in the default folder is named without a folder", func(t *testing.T) {
		h.exec("UPDATE feeds SET folder_id = 1 WHERE id = ?", id)
		_, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/moved"}))
		require.Equal(t, "You already have this feed: My News. The address you entered redirects to it.", body["message"])
	})
	t.Run("a page whose one linked feed redirects to a feed you have is refused too", func(t *testing.T) {
		h.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", folder, id)
		before := h.count("SELECT count(*) FROM feeds")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/pagemoved"}))
		require.Equal(t, 409, code, body)
		require.Equal(t, "feed_exists", body["error"])
		require.Equal(t, before, h.count("SELECT count(*) FROM feeds"))
	})
	t.Run("the same address is added when no feed owns the target", func(t *testing.T) {
		h.exec("DELETE FROM feeds")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/moved"}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "ok", body["status"])
	})
}

// Fixing a feed's address to one that redirects to another feed, or is another feed's, names that feed.
func TestPatchFeedURLNamesTheFeedThatOwnsIt(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	owner := h.storeFeed(srv + "/feed.xml")
	h.exec("UPDATE feeds SET title = 'Owner Feed' WHERE id = ?", owner)
	mine := h.storeFeed("https://other.example/feed")
	path := "/api/feeds/" + sid(mine)

	code, body, _ := h.api(c, "PATCH", path, jsonStr(map[string]any{"url": srv + "/feed.xml"}))
	require.Equal(t, 409, code, body)
	require.Equal(t, "url_exists", body["error"])
	require.Contains(t, body["message"], "Owner Feed")

	code, body, _ = h.api(c, "PATCH", path, jsonStr(map[string]any{"url": srv + "/moved"}))
	require.Equal(t, 409, code, body)
	require.Equal(t, "url_exists", body["error"])
	require.Contains(t, body["message"], "Owner Feed")
	require.Contains(t, body["message"], "redirects to it")
	require.Equal(t, "https://other.example/feed", h.feedRow(mine, "url").String, "the feed is unchanged")
}
