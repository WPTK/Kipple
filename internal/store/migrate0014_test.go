package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// 0014 clears every stored title that is the host placeholder an earlier subscribe stored: the feed's
// host, or the host of the URL a redirect moved it from, fetched or not, and clears those feeds'
// validators so the next fetch is a full one. Real titles and the archive feed stay.
func TestMigration0014ClearsPlaceholderTitles(t *testing.T) {
	e := newEnv(t)
	never := e.addFeed("https://a.example/feed")
	fetched := e.addFeed("https://untitled.example/feed") // fetched, but its document has no title
	moved := e.addFeed("https://new.example/feed")        // a redirect moved it from old.example:8443
	named := e.addFeed("https://b.example/feed")
	e.exec("UPDATE feeds SET title = host, etag = '\"e\"', last_modified = 'lm', body_hash = 'bh' WHERE id IN (?, ?)", never, fetched)
	e.exec("UPDATE feeds SET last_success_at = 1 WHERE id IN (?, ?)", fetched, named)
	e.exec(`UPDATE feeds SET title = 'old.example', url_original = 'https://old.example:8443/feed', last_success_at = 1,
		etag = '"m"' WHERE id = ?`, moved)
	e.exec("UPDATE feeds SET title = 'B News', etag = '\"b\"' WHERE id = ?", named)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ensureArchiveFeed(ctx, tx)
		return err
	}))
	e.exec("PRAGMA user_version = 13")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	r := reopen(t, path).Reader()
	title := func(id int64) string { return scalar[string](t, r, "SELECT title FROM feeds WHERE id = ?", id) }
	cleared := func(id int64) bool {
		return scalar[int](t, r, "SELECT etag IS NULL AND last_modified IS NULL AND body_hash IS NULL FROM feeds WHERE id = ?", id) == 1
	}
	for _, id := range []int64{never, fetched, moved} {
		require.Equal(t, "", title(id), id)
		require.True(t, cleared(id), "the next fetch is a full one: %d", id)
	}
	require.Equal(t, "B News", title(named))
	require.False(t, cleared(named))
	require.Equal(t, "Unsubscribed (starred)", scalar[string](t, r, "SELECT title FROM feeds WHERE disabled_reason = 'archive'"))
}
