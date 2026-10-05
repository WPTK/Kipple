package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// schema13 builds a real schema-13 database by running the embedded migrations 0001 to 0013 in order,
// as the runner did when 0013 was the newest, and returns it open on one connection with its path.
func schema13(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 14)
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec("PRAGMA foreign_keys = OFF") // as the runner does for a migration marked foreign-keys-off (0012)
	require.NoError(t, err)
	for _, m := range ms[:13] {
		tx, err := raw.Begin()
		require.NoError(t, err)
		_, err = tx.Exec(m.sql)
		require.NoError(t, err, m.name)
		_, err = tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version))
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}
	_, err = raw.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	require.Equal(t, 13, scalar[int](t, raw, "PRAGMA user_version"))
	return raw, path
}

// seedStats13 inserts n stats rows into a schema-13 database, cycling through every client value
// schema 13 allowed, every kind, with and without event ids and session keys, then deletes the
// newest ten so the sequence high-water mark is above the largest id.
func seedStats13(t *testing.T, raw *sql.DB, n int) {
	t.Helper()
	_, err := raw.Exec(`WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1)
		INSERT INTO stats_events (id, ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id,
		                          feed_title, folder_id, folder_name, item_title, item_url, value, session_key, event_id)
		SELECT i, 1780000000 + i, '2026-09-' || printf('%02d', 1 + i % 28), i % 24, i % 7,
		       CASE i % 7 WHEN 0 THEN 'open' WHEN 1 THEN 'read_time' WHEN 2 THEN 'scroll' WHEN 3 THEN 'star'
		                  WHEN 4 THEN 'unstar' WHEN 5 THEN 'open_original' ELSE 'share' END,
		       CASE i % 6 WHEN 0 THEN 'web' WHEN 1 THEN 'pwa' WHEN 2 THEN 'reeder' WHEN 3 THEN 'netnewswire'
		                  WHEN 4 THEN 'unread' ELSE 'api' END,
		       i % 2, i, i % 50, 'Feed ' || (i % 50), CASE WHEN i % 3 = 0 THEN NULL ELSE i % 9 END,
		       CASE WHEN i % 3 = 0 THEN NULL ELSE 'Folder' END, 'Item ' || i, 'https://a.example/' || i,
		       CASE WHEN i % 7 IN (1, 2) THEN i % 100 END,
		       CASE WHEN i % 5 = 0 THEN NULL ELSE printf('%032x', i / 4) END,
		       CASE WHEN i % 4 = 0 THEN printf('ev%014d', i) END
		FROM s`, n)
	require.NoError(t, err)
	_, err = raw.Exec("DELETE FROM stats_events WHERE id > ?", n-10)
	require.NoError(t, err)
}

const statsFingerprint = `SELECT count(*) || ':' || total(id) || ':' || total(ts) || ':' || total(length(local_date)) || ':' ||
	total(local_hour * 7 + local_weekday) || ':' || total(length(kind)) || ':' || total(inferred) || ':' || total(item_id) || ':' ||
	total(feed_id) || ':' || total(length(feed_title)) || ':' || total(folder_id) || ':' || total(length(folder_name)) || ':' ||
	total(length(item_title)) || ':' || total(length(item_url)) || ':' || total(value) || ':' || total(length(session_key)) || ':' ||
	total(length(event_id)) || ':' || count(value) || ':' || count(session_key) || ':' || count(event_id) || ':' || count(folder_id)
	FROM stats_events`

// 0014 rewrites the Reader API client values to 'api' and keeps every other column and every id, the
// sequence high-water mark, the indexes (the schema equals a fresh install's) and a pre-migration
// snapshot. The seeded row count is large enough to time the rebuild.
func TestMigration0014OneAPIClient(t *testing.T) {
	const n = 200_000
	raw, path := schema13(t)
	seedStats13(t, raw, n)
	before := scalar[string](t, raw, statsFingerprint)
	seq := scalar[int64](t, raw, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'")
	require.EqualValues(t, n, seq)
	byClient := func(q Querier) string {
		return scalar[string](t, q, `SELECT group_concat(client || '=' || c, ',') FROM
			(SELECT client, count(*) AS c FROM stats_events GROUP BY client ORDER BY client)`)
	}
	old := byClient(raw)
	require.Contains(t, old, "reeder=", "the old values are seeded")
	require.NoError(t, raw.Close())

	start := time.Now()
	db, err := Open(t.Context(), Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("open, snapshot and migrate schema 13 to %d with %d stats rows: %s", LatestVersion(), n-10, time.Since(start))
	require.Equal(t, LatestVersion(), scalar[int](t, db.Reader(), "PRAGMA user_version"))
	requireCleanIntegrity(t, db.Reader())
	r := db.Reader()
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-13-*.db"))
	require.Len(t, snaps, 1, "a pre-migration snapshot")

	require.Equal(t, before, scalar[string](t, r, statsFingerprint), "every column but client is unchanged")
	// n-10 rows over six client values: web and pwa keep theirs, the other four become api.
	per := (n - 10) / 6
	require.Equal(t, fmt.Sprintf("api=%d,pwa=%d,web=%d", n-10-2*per-1, per+1, per), byClient(r))
	require.Equal(t, seq, scalar[int64](t, r, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'"), "ids are never reused")
	require.Zero(t, scalar[int](t, r, "SELECT count(*) FROM sqlite_sequence WHERE name = 'stats_events_new'"))

	// The CHECK allows only the surviving values.
	ins := func(client string) error {
		_, err := db.writer.Exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title)
			VALUES (1, '2026-01-01', 0, 4, 'open', ?, 5, 1, 'F')`, client)
		return err
	}
	for _, c := range []string{"web", "pwa", "api"} {
		require.NoError(t, ins(c), c)
	}
	for _, c := range []string{"reeder", "netnewswire", "unread", "other", ""} {
		require.ErrorContains(t, ins(c), "CHECK", c)
	}
	require.EqualValues(t, seq+3, scalar[int64](t, r, "SELECT max(id) FROM stats_events"), "the next id follows the carried mark")

	// The unique event id index still refuses a repeat.
	_, err = db.writer.Exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, event_id)
		VALUES (1, '2026-01-01', 0, 4, 'scroll', 'web', 5, 1, 'F', 'ev00000000000004')`)
	require.ErrorContains(t, err, "UNIQUE")

	// The migrated schema is exactly a fresh install's: same table, indexes, triggers and views.
	fresh, _ := openTest(t)
	schema := func(d *DB) string {
		return scalar[string](t, d.Reader(), `SELECT group_concat(type || ' ' || name || ': ' || COALESCE(sql, ''), char(10))
			FROM (SELECT * FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name)`)
	}
	require.Equal(t, schema(fresh), schema(db))
	require.Contains(t, schema(db), "CHECK (client IN ('web','pwa','api'))")
}

// An empty schema-13 table whose rows were all deleted keeps its sequence mark through the rebuild.
func TestMigration0014EmptyStatsKeepsSequence(t *testing.T) {
	raw, path := schema13(t)
	seedStats13(t, raw, 20)
	_, err := raw.Exec("DELETE FROM stats_events")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	db := reopen(t, path)
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM stats_events"))
	require.EqualValues(t, 20, scalar[int64](t, db.Reader(), "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'"))
}

// A failing 0014 leaves the schema-13 table, its values and its CHECK untouched, and the retry migrates.
func TestMigration0014RollsBackOnFailure(t *testing.T) {
	raw, _ := schema13(t)
	seedStats13(t, raw, 100)
	before := scalar[string](t, raw, statsFingerprint)
	ms, err := loadMigrations()
	require.NoError(t, err)
	m := ms[13]
	d := &DB{writer: raw} // the runner's transaction alone, on the schema-13 connection
	require.Error(t, d.applyMigration(t.Context(), migration{version: m.version, name: m.name, sql: m.sql + "\nCREATE TABLE items (a);"}))
	require.Equal(t, 13, scalar[int](t, raw, "PRAGMA user_version"))
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM sqlite_master WHERE name = 'stats_events_new'"))
	require.Contains(t, scalar[string](t, raw, "SELECT sql FROM sqlite_master WHERE name = 'stats_events'"), "'reeder'")
	require.Equal(t, 15, scalar[int](t, raw, "SELECT count(*) FROM stats_events WHERE client = 'reeder'"))
	require.Equal(t, before, scalar[string](t, raw, statsFingerprint))

	start := time.Now()
	require.NoError(t, d.applyMigration(t.Context(), m))
	t.Logf("migration 0014 alone, %d stats rows: %s", scalar[int](t, raw, "SELECT count(*) FROM stats_events"), time.Since(start))
	require.Equal(t, 14, scalar[int](t, raw, "PRAGMA user_version"))
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM stats_events WHERE client NOT IN ('web','pwa','api')"))
	require.Equal(t, before, scalar[string](t, raw, statsFingerprint))
}
