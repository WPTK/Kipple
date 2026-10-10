package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// A permanent redirect to another site is not migrated automatically while the
// feed has HTTP credentials or a network exception, which were granted for the
// old host and would have to be dropped: it stays pending with a note.
func TestRedirectToNewSiteHeldWhileExceptionsSet(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	e.exec("UPDATE feeds SET http_auth = 'bob:secret', allow_insecure_tls = 1, allow_private_net = 1 WHERE id = ?", id)

	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.FinalURL = "https://b.example/feed"
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: res.FinalURL, Kind: "permanent", Count: 3}
	require.False(t, e.commit(res).Migrated)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://a.example/feed' AND host = 'a.example'
		AND redirect_to = 'https://b.example/feed' AND redirect_kind = 'permanent' AND redirect_count = 2
		AND http_auth = 'bob:secret' AND allow_insecure_tls = 1 AND allow_private_net = 1`, id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE '%redirect_held_new_site: https://b.example/feed is on another site%'"))
}

// Without any of them the move to another site migrates as before.
func TestRedirectToNewSiteMigratesWithoutExceptions(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	e.exec("UPDATE feeds SET allow_private_net = 0 WHERE id = ?", id) // the test helper sets it
	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.FinalURL = "https://b.example/feed"
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: res.FinalURL, Kind: "permanent", Count: 3}
	require.True(t, e.commit(res).Migrated)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://b.example/feed' AND host = 'b.example'
		AND redirect_to IS NULL AND http_auth IS NULL AND allow_insecure_tls = 0 AND allow_private_net = 0`, id))
}

// A move inside the same site keeps them: bare -> www, and a LAN name gaining
// its search domain (the feed would otherwise be left guard-blocked or 401).
func TestRedirectMigrationSameSiteKeepsCredentials(t *testing.T) {
	t.Parallel()
	for _, c := range [][2]string{
		{"https://example.com/feed", "https://www.example.com/feed"},
		{"http://nas/feed", "http://nas.lan/feed"},
	} {
		e := newEnv(t)
		id := e.addFeed(c[0])
		e.exec("UPDATE feeds SET http_auth = 'bob:secret', allow_insecure_tls = 1, allow_private_net = 1 WHERE id = ?", id)
		res := e.okResult(e.snap(id), rss(numbered(1)...))
		res.FinalURL = c[1]
		res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: c[1], Kind: "permanent", Count: 3}
		require.True(t, e.commit(res).Migrated, c[0])
		require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = ?
			AND http_auth = 'bob:secret' AND allow_insecure_tls = 1 AND allow_private_net = 1`, id, c[1]), c[0])
	}
}

// The same-host migration (http -> https) keeps them.
func TestRedirectMigrationSameHostKeepsCredentials(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET http_auth = 'bob:secret', allow_insecure_tls = 1, allow_private_net = 1 WHERE id = ?", id)

	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.FinalURL = "https://A.example/feed"
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: "https://a.example/feed", Kind: "permanent", Count: 3}
	require.True(t, e.commit(res).Migrated)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://a.example/feed'
		AND http_auth = 'bob:secret' AND allow_insecure_tls = 1 AND allow_private_net = 1`, id))
	require.Equal(t, 0, e.count("SELECT count(*) FROM fetch_log WHERE note LIKE '%redirect_new_host%'"))
}
