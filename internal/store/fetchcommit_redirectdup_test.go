package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

func (e *env) redirected(id int64, to, kind string) *fetch.Result {
	e.t.Helper()
	return &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeOK, Status: 200,
		FinalURL: to, Redirect: fetch.RedirectDecision{Action: fetch.RedirectSet, To: to, Kind: kind, Count: 1}}
}

// A feed that has never fetched and answers through a redirect to a feed the user already has (an
// import or a client subscribe cannot check first) is removed, and the kept feed takes its folder and
// custom title, as for a discovered duplicate.
func TestCommitRedirectDuplicateMergesAFeedThatNeverFetched(t *testing.T) {
	e := newEnv(t)
	kept := e.addFeed("https://blog.example/feed.xml")
	dup := e.addFeed("https://blog.example/old-feed")
	e.exec("INSERT INTO folders (id, name, position) VALUES (7, 'Tech', 1)")
	e.exec("UPDATE feeds SET folder_id = 7, custom_title = 'My blog' WHERE id = ?", dup)

	ci, handled, err := e.db.CommitRedirectDuplicate(e.ctx, e.redirected(dup, "http://blog.example/feed.xml", "permanent"))
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, kept, ci.MergedInto)
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ?", dup))
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ? AND folder_id = 7 AND custom_title = 'My blog'", kept))
}

// Anything else commits as a normal fetch: a free redirect target, no redirect, a feed that already
// fetched (a real redirect of a feed the user has used), or an edit that landed while it was in flight.
func TestCommitRedirectDuplicateLeavesEveryOtherFetchAlone(t *testing.T) {
	e := newEnv(t)
	e.addFeed("https://blog.example/feed.xml")
	dup := e.addFeed("https://blog.example/old-feed")

	for name, res := range map[string]*fetch.Result{
		"free target": e.redirected(dup, "https://elsewhere.example/feed", "permanent"),
		"no redirect": {Snap: e.snap(dup), Outcome: fetch.OutcomeOK, Redirect: fetch.RedirectDecision{Action: fetch.RedirectClear}},
	} {
		_, handled, err := e.db.CommitRedirectDuplicate(e.ctx, res)
		require.NoError(t, err, name)
		require.False(t, handled, name)
	}

	e.exec("UPDATE feeds SET last_success_at = 1 WHERE id = ?", dup)
	_, handled, err := e.db.CommitRedirectDuplicate(e.ctx, e.redirected(dup, "https://blog.example/feed.xml", "permanent"))
	require.NoError(t, err)
	require.False(t, handled, "a feed that has fetched before keeps its pending redirect")

	e.exec("UPDATE feeds SET last_success_at = NULL WHERE id = ?", dup)
	res := e.redirected(dup, "https://blog.example/feed.xml", "permanent")
	e.exec("UPDATE feeds SET url = 'https://moved.example/rss' WHERE id = ?", dup)
	_, handled, err = e.db.CommitRedirectDuplicate(e.ctx, res)
	require.NoError(t, err)
	require.False(t, handled, "stale")
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE id = ?", dup))
}
