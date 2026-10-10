package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sched"
)

// A page links a page that links a feed you already have: the new feed is created, its first fetch
// merges it into the feed you have and removes it, and the answer is the same refusal naming that feed,
// not a server error.
func TestAddFeedMergedDuringFirstFetchAnswersFeedExists(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	owner := h.storeFeed(srv + "/feed.xml")
	h.exec("UPDATE feeds SET title = 'My News' WHERE id = ?", owner)
	h.sched.onSubmit = func(p sched.Priority) { h.exec("DELETE FROM feeds WHERE id = ?", p.FeedID) }
	h.sched.reply = sched.Reply{MergedInto: owner}

	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/pageofpage"}))
	require.Equal(t, 409, code, body)
	require.Equal(t, "feed_exists", body["error"])
	require.Equal(t, sid(owner), body["feed_id"])
	require.Contains(t, body["message"], "You already have this feed: My News")
	require.NotContains(t, body["message"], "redirects")
}

// The wording names what redirected: for a page it is the feed the page links, not the page itself.
func TestAddFeedPageWordingNamesTheLinkedFeed(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	h.storeFeed(srv + "/feed.xml")

	_, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/pagemoved"}))
	require.Contains(t, body["message"], "The page you entered links a feed that redirects to it.")
	_, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/moved"}))
	require.Contains(t, body["message"], "The address you entered redirects to it.")
}

// The feed editor refuses a page whose one linked feed redirects to a feed you have, as Add does, and
// words it for a page.
func TestPatchFeedURLRefusesPageLinkingRedirectingFeed(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	h.storeFeed(srv + "/feed.xml")
	mine := h.storeFeed("https://other.example/feed")

	code, body, _ := h.api(c, "PATCH", "/api/feeds/"+sid(mine), jsonStr(map[string]any{"url": srv + "/pagemoved"}))
	require.Equal(t, 409, code, body)
	require.Equal(t, "url_exists", body["error"])
	require.Contains(t, body["message"], "The page you entered links a feed that redirects to it.")
	require.Equal(t, "https://other.example/feed", h.feedRow(mine, "url").String, "the feed is unchanged")
}

// Keeping a feed that redirects to a feed you have clears its Moved state and the choice survives the
// next fetch's redirect; an unknown feed is a 404.
func TestKeepRedirect(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	owner := h.storeFeed("https://new.example/feed")
	dup := h.storeFeed("https://old.example/feed")
	h.exec("UPDATE feeds SET redirect_to = 'https://new.example/feed', redirect_kind = 'permanent', redirect_count = 2 WHERE id = ?", dup)
	_ = owner

	code, _, rec := h.api(c, "POST", "/api/feeds/"+sid(dup)+"/redirect/keep", "")
	require.Equal(t, 204, code)
	require.Empty(t, rec.Body.String())
	require.False(t, h.feedRow(dup, "redirect_to").Valid)
	require.False(t, h.feedRow(dup, "redirect_kind").Valid)
	require.Equal(t, "https://new.example/feed", h.feedRow(dup, "redirect_ack").String)

	code, _, _ = h.api(c, "POST", "/api/feeds/"+sid(dup)+"/redirect/keep", "")
	require.Equal(t, 204, code, "keeping again changes nothing")
	for _, p := range []string{"/api/feeds/999/redirect/keep", "/api/feeds/x/redirect/keep"} {
		code, _, _ = h.api(c, "POST", p, "")
		require.Equal(t, 404, code, p)
	}
}
