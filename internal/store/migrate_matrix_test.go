package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rawAt builds an empty database at schema version v and returns its path with a connection that has
// to be closed before the file is opened as a store.
func rawAt(t *testing.T, v int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	require.NoError(t, BuildSchema(t.Context(), raw, v))
	return path, raw
}

// shapes lists every schema object of q as "type name" -> its structure as SQLite reads it, so a table
// built by ALTER TABLE equals the same table written in one statement.
func shapes(t *testing.T, q Querier) map[string]string {
	t.Helper()
	objs, err := ReadSchema(t.Context(), q)
	require.NoError(t, err)
	out := map[string]string{}
	for _, o := range objs {
		if strings.HasPrefix(o.Name, "sqlite_") { // statistics tables that PRAGMA optimize adds
			continue
		}
		s, err := Shape(t.Context(), q, o)
		require.NoError(t, err)
		out[o.Type+" "+o.Name] = s
	}
	return out
}

// A database at every older schema version, with a feed and a setting in it, migrates to the latest
// schema: the same objects and shapes (columns, keys and index structure; not defaults, checks or SQL text) as a fresh database, an intact file, and the rows still there.
func TestMigrateFromEveryVersionMatchesAFreshSchema(t *testing.T) {
	t.Parallel()
	fresh, err := sql.Open("sqlite", buildDSN(filepath.Join(t.TempDir(), "fresh.db"), "writer"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	require.NoError(t, BuildSchema(t.Context(), fresh, LatestVersion()))
	want := shapes(t, fresh)

	for v := 1; v < LatestVersion(); v++ {
		t.Run(fmt.Sprintf("from_%d", v), func(t *testing.T) {
			t.Parallel()
			path, raw := rawAt(t, v)
			_, err := raw.Exec(`INSERT INTO feeds (url, url_key, host) VALUES ('http://a.example/feed', 'a.example/feed', 'a.example')`)
			require.NoError(t, err)
			_, err = raw.Exec(`INSERT INTO settings (key, value) VALUES ('test.keep', '"yes"')`)
			require.NoError(t, err)
			require.NoError(t, raw.Close())

			db := reopen(t, path)
			require.Equal(t, want, shapes(t, db.Reader()))
			require.Equal(t, "http://a.example/feed", scalar[string](t, db.Reader(), `SELECT url FROM feeds`))
			require.Equal(t, `"yes"`, scalar[string](t, db.Reader(), `SELECT value FROM settings WHERE key = 'test.keep'`))
		})
	}
}

// 0002 adds feeds.ua_fallback (off for every existing feed) and removes only the two reading settings
// that ui.reading_density replaced.
func TestMigration0002KeepsDataAndDropsTheTwoSettings(t *testing.T) {
	t.Parallel()
	path, raw := rawAt(t, 1)
	_, err := raw.Exec(`INSERT INTO feeds (url, url_key, host, title, enabled) VALUES
		('http://a.example/feed', 'a.example/feed', 'a.example', 'A', 1),
		('http://b.example/feed', 'b.example/feed', 'b.example', 'B', 0)`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO settings (key, value) VALUES
		('ui.line_height', '1.8'), ('ui.content_width', '"wide"'), ('ui.reading_density', '"roomy"'), ('refresh.minutes', '15')`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	db := reopen(t, path)
	require.Equal(t, 2, scalar[int](t, db.Reader(), `SELECT count(*) FROM feeds`))
	require.Equal(t, 2, scalar[int](t, db.Reader(), `SELECT count(*) FROM feeds WHERE ua_fallback = 0`))
	require.Equal(t, "A", scalar[string](t, db.Reader(), `SELECT title FROM feeds WHERE url = 'http://a.example/feed'`))
	require.Equal(t, 0, scalar[int](t, db.Reader(), `SELECT enabled FROM feeds WHERE url = 'http://b.example/feed'`))
	require.Zero(t, scalar[int](t, db.Reader(), `SELECT count(*) FROM settings WHERE key IN ('ui.line_height','ui.content_width')`))
	require.Equal(t, 2, scalar[int](t, db.Reader(), `SELECT count(*) FROM settings WHERE key IN ('ui.reading_density','refresh.minutes')`))
}

// 0019 adds feeds.redirect_ack as NULL and leaves every other column of every feed as it was.
func TestMigration0019KeepsFeedsAndLeavesNoRedirectChoice(t *testing.T) {
	t.Parallel()
	path, raw := rawAt(t, 18)
	_, err := raw.Exec(`INSERT INTO feeds (url, url_key, host, title, etag, last_status, consecutive_failures, ua_fallback) VALUES
		('http://a.example/feed', 'a.example/feed', 'a.example', 'A', 'e1', 301, 2, 1),
		('http://b.example/feed', 'b.example/feed', 'b.example', 'B', NULL, NULL, 0, 0)`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	db := reopen(t, path)
	require.Equal(t, 2, scalar[int](t, db.Reader(), `SELECT count(*) FROM feeds`))
	require.Equal(t, 2, scalar[int](t, db.Reader(), `SELECT count(*) FROM feeds WHERE redirect_ack IS NULL`))
	require.Equal(t, "A|e1|301|2|1", scalar[string](t, db.Reader(),
		`SELECT title || '|' || etag || '|' || last_status || '|' || consecutive_failures || '|' || ua_fallback FROM feeds WHERE host = 'a.example'`))
	require.Equal(t, "B", scalar[string](t, db.Reader(), `SELECT title FROM feeds WHERE host = 'b.example'`))
}
