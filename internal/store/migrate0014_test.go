package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// 0014 clears the host placeholder an earlier subscribe stored as title: the feed's host, or the host
// of the URL a redirect moved it from. It clears it on a feed that never fetched (enabled or not) and
// on an enabled feed that fetched (whose validators it clears too, so the next fetch is a full one),
// never on a disabled, gone or archive feed that fetched, whose title may really be its domain.
func TestMigration0014ClearsPlaceholderTitles(t *testing.T) {
	e := newEnv(t)
	type row struct {
		url, title, original string
		fetched, disabled    bool
		reason               string // disabled_reason when disabled
		cleared, refetch     bool
	}
	rows := map[string]row{
		"never fetched":           {url: "https://a.example/feed", title: "a.example", cleared: true},
		"never fetched, disabled": {url: "https://b.example/feed", title: "b.example", disabled: true, reason: "user", cleared: true},
		"fetched, untitled doc":   {url: "https://untitled.example/feed", title: "untitled.example", fetched: true, cleared: true, refetch: true},
		"redirected":              {url: "https://new.example/feed", title: "old.example", original: "https://old.example:8443/feed", fetched: true, cleared: true, refetch: true},
		"redirected, user info":   {url: "https://new2.example/feed", title: "old2.example", original: "https://user:pw@old2.example/feed", fetched: true, cleared: true, refetch: true},
		"redirected, query":       {url: "https://new3.example/feed", title: "old3.example", original: "https://old3.example?x=a/b", fetched: true, cleared: true, refetch: true},
		"redirected, fragment":    {url: "https://new4.example/feed", title: "old4.example", original: "http://old4.example#a/b", fetched: true, cleared: true, refetch: true},
		"disabled after success":  {url: "https://xkcd.example/atom.xml", title: "xkcd.example", fetched: true, disabled: true, reason: "user"},
		"gone after success":      {url: "https://gone.example/feed", title: "gone.example", fetched: true, disabled: true, reason: "gone"},
		"real title":              {url: "https://c.example/feed", title: "C News", fetched: true},
	}
	ids := map[string]int64{}
	for name, r := range rows {
		id := e.addFeed(r.url)
		ids[name] = id
		e.exec(`UPDATE feeds SET title = ?, etag = '"e"', last_modified = 'lm', body_hash = 'bh',
			url_original = NULLIF(?, ''), last_success_at = CASE WHEN ? THEN 1 END,
			enabled = ?, disabled_reason = NULLIF(?, '') WHERE id = ?`,
			r.title, r.original, r.fetched, !r.disabled, r.reason, id)
	}
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ensureArchiveFeed(ctx, tx)
		return err
	}))
	e.exec("PRAGMA user_version = 13")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	r := reopen(t, path).Reader()
	for name, want := range rows {
		id := ids[name]
		title := scalar[string](t, r, "SELECT title FROM feeds WHERE id = ?", id)
		if want.cleared {
			require.Equal(t, "", title, name)
		} else {
			require.Equal(t, want.title, title, name)
		}
		validatorsGone := scalar[int](t, r, "SELECT etag IS NULL AND last_modified IS NULL AND body_hash IS NULL FROM feeds WHERE id = ?", id) == 1
		require.Equal(t, want.refetch, validatorsGone, "%s: validators cleared only for a feed that will refetch", name)
	}
	require.Equal(t, "Unsubscribed (starred)", scalar[string](t, r, "SELECT title FROM feeds WHERE disabled_reason = 'archive'"))
}
