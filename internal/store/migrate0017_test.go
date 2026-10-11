package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0017 turns a current database into a schema-16 one (no feed_daily_new, no feeds.url_succeeded, no feeds.redirect_ack),
// and is the first step of every downgrade helper below it (a migration file runs once, docs/design.md
// §2.5).
const undo0017 = `ALTER TABLE feeds DROP COLUMN redirect_ack;
DROP TABLE feed_daily_new;
ALTER TABLE feeds DROP COLUMN url_succeeded`

// A populated schema-16 database: 0017 adds the empty table, its index and url_succeeded (1 where the feed
// has succeeded), leaves every other value alone, and the result has exactly the objects of a fresh
// database. The next fetch then counts.
func TestMigration0017OnAPopulatedSchema16(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(5)...))
	never := e.addFeed("http://b.example/feed")
	// A URL edited on schema 16 and not yet fetched: it has succeeded (at the old URL) but the edit cleared
	// body_hash, as PatchFeed does, so its first document is still a backlog.
	edited := e.addFeed("http://c.example/feed")
	e.fetchBody(edited, rss(numbered(3)...))
	e.exec("UPDATE feeds SET body_hash = NULL, last_fetch_at = last_success_at WHERE id = ?", edited)
	e.exec("UPDATE items SET read = 1, starred = 1 WHERE title = 'title g1' AND feed_id = ?", id)
	e.exec(`INSERT INTO settings (key, value) VALUES ('tz', '"UTC"')`)
	e.exec(undo0017)
	e.exec("PRAGMA user_version = 16")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	at16 := schemaObjects(t, e.db.Reader())
	require.NoError(t, e.db.Close())

	// The database really is at schema 16: the same objects as one built to 16.
	ref, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "ref.db"), "writer"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ref.Close() })
	require.NoError(t, BuildSchema(t.Context(), ref, 16))
	require.Equal(t, schemaObjects(t, ref), at16)

	db, err := Open(t.Context(), Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(t.Context())
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	requireCleanIntegrity(t, db.Reader())
	require.Equal(t, 5, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE feed_id = ? AND title = 'title g1' AND read = 1 AND starred = 1", id))
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM feed_daily_new"), "days before the migration have no row")
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT url_succeeded FROM feeds WHERE id = ?", id), "a feed that has succeeded starts at 1")
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT url_succeeded FROM feeds WHERE id = ?", never), "a feed that never succeeded starts at 0")
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT url_succeeded FROM feeds WHERE id = ?", edited), "an edited, unfetched URL starts at 0")

	fresh, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "fresh.db"), "writer"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	require.NoError(t, BuildSchema(t.Context(), fresh, LatestVersion()))
	require.Equal(t, schemaObjects(t, fresh), schemaObjects(t, db.Reader()))

	e2 := &env{t: t, db: db, clk: e.clk, ctx: e.ctx}
	e2.fetchBody(id, rss(append(numbered(5), newer(2)...)...))
	require.Equal(t, 2, e2.newsDaily(id, base))
	e2.fetchBody(edited, rss(append(numbered(3), newer(2)...)...))
	require.Zero(t, e2.newsDaily(edited, base), "the edited URL's first document is a backlog even with guids new to the feed")
	// Idempotent: opening the migrated database again runs nothing (gated by user_version) and keeps the flags.
	require.NoError(t, db.Close())
	again := reopen(t, path)
	require.Equal(t, 1, scalar[int](t, again.Reader(), "SELECT url_succeeded FROM feeds WHERE id = ?", edited), "set by its own fetch, kept")
}
