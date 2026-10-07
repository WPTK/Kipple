package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A schema-17 database with counts: 0018 rebuilds feed_daily_new keyed by day with the counted id spans,
// drops the rows that have none (counting starts again), and the result is a fresh database's shape.
func TestMigration0018RebuildsFeedDailyNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	require.NoError(t, BuildSchema(t.Context(), raw, 17))
	_, err = raw.Exec(`INSERT INTO feed_daily_new (feed_id, local_date, new_items) VALUES (1, '2026-09-20', 4)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	e := &env{t: t, db: reopen(t, path)}
	v, err := e.db.Version(t.Context())
	require.NoError(t, err)
	require.Equal(t, 18, v)
	requireCleanIntegrity(t, e.db.Reader())
	require.Zero(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM feed_daily_new"), "rows without ids cannot take part")

	fresh, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "fresh.db"), "writer"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	require.NoError(t, BuildSchema(t.Context(), fresh, LatestVersion()))
	require.Equal(t, schemaObjects(t, fresh), schemaObjects(t, e.db.Reader()))
}

// Each chunk widens the day's span to the ids it counted; the first document adds none.
func TestFeedDailyNewKeepsTheCountedSpan(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(5)...))
	e.fetchBody(id, rss(append(numbered(5), newer(2)...)...))
	e.fetchBody(id, rss(append(numbered(5), newer(4)...)...))
	ids := e.ids(`SELECT id FROM items WHERE feed_id = ? AND title LIKE 'title n%' ORDER BY id`, id)
	require.Len(t, ids, 4)
	var n int
	var first, last int64
	require.NoError(t, e.db.Reader().QueryRow(`SELECT new_items, first_item, last_item FROM feed_daily_new WHERE feed_id = ?`, id).Scan(&n, &first, &last))
	require.Equal(t, 4, n)
	require.Equal(t, ids[0], first)
	require.Equal(t, ids[3], last)
	backlog := e.ids(`SELECT id FROM items WHERE feed_id = ? AND title LIKE 'title g%' ORDER BY id DESC LIMIT 1`, id)
	require.Less(t, backlog[0], first, "the first document lies before the span")
}
