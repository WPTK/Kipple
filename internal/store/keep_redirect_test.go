package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// Keeping a feed that redirects to a feed you already have clears the pending redirect, and no later
// fetch records it again: the feed stops being Moved for good, until the other feed goes away.
func TestKeepRedirectHoldsThroughLaterFetches(t *testing.T) {
	e := newEnv(t)
	owner := e.addFeed("https://b.example/feed")
	id := e.addFeed("https://a.example/feed")
	target := "https://b.example/feed"
	commit := func(action string, count int) {
		res := e.okResult(e.snap(id), rss(numbered(1)...))
		res.Redirect = fetch.RedirectDecision{Action: action, To: target, Kind: "permanent", Count: count}
		e.commit(res)
	}
	commit(fetch.RedirectMigrate, 3)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_kind = 'permanent'", id))

	require.NoError(t, e.db.KeepRedirect(e.ctx, id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to IS NULL AND redirect_kind IS NULL AND redirect_count = 0 AND redirect_ack = ?", id, target))

	commit(fetch.RedirectMigrate, 3)
	commit(fetch.RedirectSet, 1)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to IS NULL", id), "a kept redirect is not recorded again")

	// Another address, or the owner going away, is a new situation.
	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectSet, To: "https://c.example/feed", Kind: "permanent", Count: 1}
	e.commit(res)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to = 'https://c.example/feed'", id))
	e.exec("DELETE FROM feeds WHERE id = ?", owner)
	commit(fetch.RedirectSet, 1)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to = ?", id, target))

	require.ErrorIs(t, e.db.KeepRedirect(e.ctx, 9999), ErrFeedNotFound)
}

// A kept redirect is recorded as no redirect at all, so it also ends an older pending one.
func TestKeptRedirectClearsAnOlderPendingRedirect(t *testing.T) {
	e := newEnv(t)
	e.addFeed("https://b.example/feed")
	id := e.addFeed("https://a.example/feed")
	e.exec("UPDATE feeds SET redirect_to = 'https://c.example/feed', redirect_kind = 'permanent', redirect_count = 2, redirect_ack = 'https://b.example/feed' WHERE id = ?", id)
	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectSet, To: "https://b.example/feed", Kind: "permanent", Count: 1}
	e.commit(res)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_to IS NULL AND redirect_kind IS NULL AND redirect_count = 0", id))
}

// A URL edit, or a discovery that changes the address, forgets the kept choice.
func TestRedirectAckEndsWithTheAddress(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	e.exec("UPDATE feeds SET redirect_ack = 'https://b.example/feed' WHERE id = ?", id)
	nu := "https://a.example/other"
	_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &nu, Cols: map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_ack IS NULL", id))

	page := e.addFeed("https://p.example/")
	e.exec("UPDATE feeds SET redirect_ack = 'https://b.example/feed' WHERE id = ?", page)
	e.commitDiscovered(e.discovered(page, "https://p.example/feed.xml"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND redirect_ack IS NULL", page))
}
