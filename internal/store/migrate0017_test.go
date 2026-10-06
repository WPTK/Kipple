package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0017 turns a current database into a schema-16 one, and is the first step of every downgrade
// helper below it (0017 has no IF NOT EXISTS: a file runs once, docs/design.md §2.5).
const undo0017 = `DROP TABLE feed_daily_new`

// A populated schema-16 database: 0017 adds the empty table and its index, leaves every row alone, and
// the result has exactly the objects of a fresh database. The next fetch then counts.
func TestMigration0017OnAPopulatedSchema16(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(5)...))
	e.exec("UPDATE items SET read = 1, starred = 1 WHERE title = 'title g1'")
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
	require.Equal(t, 17, v)
	requireCleanIntegrity(t, db.Reader())
	require.Equal(t, 5, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE title = 'title g1' AND read = 1 AND starred = 1"))
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM feed_daily_new"), "days before the migration have no row")

	fresh, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "fresh.db"), "writer"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	require.NoError(t, BuildSchema(t.Context(), fresh, LatestVersion()))
	require.Equal(t, schemaObjects(t, fresh), schemaObjects(t, db.Reader()))

	e2 := &env{t: t, db: db, clk: e.clk, ctx: e.ctx}
	e2.fetchBody(id, rss(append(numbered(5), newer(2)...)...))
	require.Equal(t, 2, e2.newsDaily(id, base))
}
