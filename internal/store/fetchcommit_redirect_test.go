package store

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// A redirect migration to another host drops the credentials and the network
// exceptions, which were granted for the old host (as a URL edit does).
func TestRedirectMigrationToNewHostDropsCredentials(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	e.exec("UPDATE feeds SET http_auth = 'bob:secret', allow_insecure_tls = 1, allow_private_net = 1 WHERE id = ?", id)

	res := e.okResult(e.snap(id), rss(numbered(1)...))
	res.FinalURL = "https://b.example/feed"
	res.Redirect = fetch.RedirectDecision{Action: fetch.RedirectMigrate, To: res.FinalURL, Kind: "permanent", Count: 3}
	require.True(t, e.commit(res).Migrated)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM feeds WHERE id = ? AND url = 'https://b.example/feed' AND host = 'b.example'
		AND http_auth IS NULL AND allow_insecure_tls = 0 AND allow_private_net = 0`, id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE keep = 1 AND note LIKE '%redirect_new_host: http_auth, allow_insecure_tls and allow_private_net reset%'"))
}

// The same-host migration (http -> https) keeps them.
func TestRedirectMigrationSameHostKeepsCredentials(t *testing.T) {
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
