package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// schema14 builds a real schema-14 database by running the embedded migrations 0001 to 0014 in order,
// as the runner did when 0014 was the newest, and returns it open on one connection with its path.
func schema14(t *testing.T) (*sql.DB, string) {
	t.Helper()
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 15)
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = raw.Close() })
	_, err = raw.Exec("PRAGMA foreign_keys = OFF") // as the runner does for a migration marked foreign-keys-off (0012)
	require.NoError(t, err)
	for _, m := range ms[:14] {
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
	require.Equal(t, 14, scalar[int](t, raw, "PRAGMA user_version"))
	return raw, path
}

// seedStats14 inserts n stats rows into a schema-14 database, cycling through every client value
// schema 14 allowed, every kind, with and without event ids and session keys, then deletes the
// newest ten so the sequence high-water mark is above the largest id.
func seedStats14(t *testing.T, raw *sql.DB, n int) {
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

// statsCols lists every stats_events column but client, in table order.
const statsCols = `id, ts, local_date, local_hour, local_weekday, kind, inferred, item_id, feed_id, feed_title, folder_id,
	folder_name, item_title, item_url, value, session_key, event_id`

// clientAfter0015 is what 0015 makes of a schema-14 client value.
const clientAfter0015 = `CASE WHEN client IN ('web','pwa') THEN client ELSE 'api' END`

const oldClientCheck = "CHECK (client IN ('web','pwa','reeder','netnewswire','unread','api'))"

// statsDump is every stats row, every column quoted (NULL included), in id order, with the client
// column given by clientExpr.
func statsDump(t *testing.T, q Querier, clientExpr string) string {
	t.Helper()
	parts := []string{"quote(" + clientExpr + ")"}
	for _, c := range strings.Split(statsCols, ",") {
		parts = append(parts, "quote("+strings.TrimSpace(c)+")")
	}
	return scalar[string](t, q, `SELECT group_concat(r, char(10)) FROM (SELECT `+strings.Join(parts, " || '|' || ")+
		` AS r FROM stats_events ORDER BY id)`)
}

// schemaObjects is sqlite_master as "type name" -> sql, without SQLite's own objects.
func schemaObjects(t *testing.T, q Querier) map[string]string {
	t.Helper()
	rows, err := q.QueryContext(t.Context(), "SELECT type || ' ' || name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		require.NoError(t, rows.Scan(&k, &v))
		out[k] = v
	}
	require.NoError(t, rows.Err())
	return out
}

// tableShape reduces a CREATE TABLE to its tokens: comments, whitespace and the quotes a rename adds
// are dropped, so a table written in one statement compares equal to the same table built by CREATE
// plus ALTER TABLE ADD COLUMN.
func tableShape(sqlText string) string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
	}
	return strings.NewReplacer(" ", "", "\t", "", "\r", "", `"`, "").Replace(b.String())
}

// 0015 rewrites the Reader API client values to 'api' and keeps every other column of every row, the
// sequence high-water mark, every other schema object byte for byte and a pre-migration snapshot. The
// seeded row count is large enough to time the rebuild.
func TestMigration0015OneAPIClient(t *testing.T) {
	const n = 200_000
	raw, path := schema14(t)
	seedStats14(t, raw, n)
	seq := scalar[int64](t, raw, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'")
	require.EqualValues(t, n, seq)
	byClient := func(q Querier) string {
		return scalar[string](t, q, `SELECT group_concat(client || '=' || c, ',') FROM
			(SELECT client, count(*) AS c FROM stats_events GROUP BY client ORDER BY client)`)
	}
	require.Contains(t, byClient(raw), "reeder=", "the old values are seeded")
	oldSchema := schemaObjects(t, raw)
	require.Contains(t, oldSchema["table stats_events"], oldClientCheck)
	require.NoError(t, raw.Close())
	st, err := os.Stat(path)
	require.NoError(t, err)
	sizeBefore := st.Size()

	start := time.Now()
	db, err := Open(t.Context(), Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("open, snapshot and migrate schema 14 to %d with %d stats rows: %s", LatestVersion(), n-10, time.Since(start))
	require.Equal(t, LatestVersion(), scalar[int](t, db.Reader(), "PRAGMA user_version"))
	requireCleanIntegrity(t, db.Reader())
	r := db.Reader()
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-14-*.db"))
	require.Len(t, snaps, 1, "a pre-migration snapshot")

	// The database and its WAL grew within what the free-space check reserves for the migration.
	var after int64
	for _, f := range []string{path, path + "-wal"} {
		if st, err := os.Stat(f); err == nil {
			after += st.Size()
		}
	}
	t.Logf("database %d bytes before, database and WAL %d bytes after", sizeBefore, after)
	require.LessOrEqual(t, after-sizeBefore, int64(migrateRewriteFactor)*sizeBefore+migrateHeadroom)

	// Row by row against the snapshot, both ways: every column but client is unchanged (a swapped or
	// altered column makes a row differ) and client is mapped.
	cmp, err := sql.Open("sqlite", buildDSN(path, "reader"))
	require.NoError(t, err)
	cmp.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = cmp.Close() })
	_, err = cmp.Exec("ATTACH DATABASE ? AS old", snaps[0])
	require.NoError(t, err)
	oldRows := "SELECT " + clientAfter0015 + ", " + statsCols + " FROM old.stats_events"
	newRows := "SELECT client, " + statsCols + " FROM main.stats_events"
	require.Equal(t, n-10, scalar[int](t, cmp, "SELECT count(*) FROM old.stats_events"))
	require.Equal(t, n-10, scalar[int](t, cmp, "SELECT count(*) FROM main.stats_events"))
	require.Zero(t, scalar[int](t, cmp, "SELECT count(*) FROM ("+oldRows+" EXCEPT "+newRows+")"))
	require.Zero(t, scalar[int](t, cmp, "SELECT count(*) FROM ("+newRows+" EXCEPT "+oldRows+")"))
	require.NoError(t, cmp.Close())

	// n-10 rows over six client values: web and pwa keep theirs, the other four become api.
	per := (n - 10) / 6
	require.Equal(t, fmt.Sprintf("api=%d,pwa=%d,web=%d", n-10-2*per-1, per+1, per), byClient(r))
	require.Equal(t, seq, scalar[int64](t, r, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'"), "ids are never reused")
	require.Zero(t, scalar[int](t, r, "SELECT count(*) FROM sqlite_sequence WHERE name = 'stats_events_new'"))

	// The schema is schema 14's with only the client CHECK changed: every index (the INDEXED BY ones
	// included), trigger, view and other table byte for byte, and stats_events the same columns.
	newSchema := schemaObjects(t, r)
	oldTable, newTable := oldSchema["table stats_events"], newSchema["table stats_events"]
	delete(oldSchema, "table stats_events")
	delete(newSchema, "table stats_events")
	require.Equal(t, oldSchema, newSchema)
	require.Equal(t, tableShape(strings.Replace(oldTable, oldClientCheck, "CHECK (client IN ('web','pwa','api'))", 1)), tableShape(newTable))

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
}

// The free-space check reserves room for a table rebuild: twice the database plus the headroom on its
// volume, and the snapshot on top when both share it (the fake reports one shared volume).
func TestMigration0015SpaceCheckCoversARebuild(t *testing.T) {
	raw, path := schema14(t)
	seedStats14(t, raw, 20_000)
	require.NoError(t, raw.Close())
	need := func() uint64 { // from the file's size when the check runs (opening may checkpoint into it)
		st, err := os.Stat(path)
		require.NoError(t, err)
		size := uint64(st.Size())
		return migrateRewriteFactor*size + migrateHeadroom + uint64(float64(size)*snapshotFreeFactor)
	}

	orig := migrationFreeBytes
	t.Cleanup(func() { migrationFreeBytes = orig })
	migrationFreeBytes = func(string) (uint64, error) { return need() - 1, nil }
	_, err := Open(t.Context(), Options{Path: path})
	require.ErrorContains(t, err, "not enough free disk space")
	migrationFreeBytes = func(string) (uint64, error) { return need(), nil }
	db, err := Open(t.Context(), Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.Equal(t, LatestVersion(), scalar[int](t, db.Reader(), "PRAGMA user_version"))
}

// An empty schema-14 table whose rows were all deleted keeps its sequence mark through the rebuild.
func TestMigration0015EmptyStatsKeepsSequence(t *testing.T) {
	raw, path := schema14(t)
	seedStats14(t, raw, 20)
	_, err := raw.Exec("DELETE FROM stats_events")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	db := reopen(t, path)
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM stats_events"))
	require.EqualValues(t, 20, scalar[int64](t, db.Reader(), "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'"))
}

// A failing 0015 leaves the schema-14 table, its values and its CHECK untouched, and the retry migrates.
func TestMigration0015RollsBackOnFailure(t *testing.T) {
	raw, _ := schema14(t)
	seedStats14(t, raw, 100)
	before := statsDump(t, raw, "client")
	mapped := statsDump(t, raw, clientAfter0015)
	require.NotEqual(t, before, mapped)
	ms, err := loadMigrations()
	require.NoError(t, err)
	m := ms[14]
	d := &DB{writer: raw} // the runner's transaction alone, on the schema-14 connection
	require.Error(t, d.applyMigration(t.Context(), migration{version: m.version, name: m.name, sql: m.sql + "\nCREATE TABLE items (a);"}))
	require.Equal(t, 14, scalar[int](t, raw, "PRAGMA user_version"))
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM sqlite_master WHERE name = 'stats_events_new'"))
	require.Contains(t, scalar[string](t, raw, "SELECT sql FROM sqlite_master WHERE name = 'stats_events'"), oldClientCheck)
	require.Equal(t, before, statsDump(t, raw, "client"), "untouched")

	start := time.Now()
	require.NoError(t, d.applyMigration(t.Context(), m))
	t.Logf("migration 0015 alone, %d stats rows: %s", scalar[int](t, raw, "SELECT count(*) FROM stats_events"), time.Since(start))
	require.Equal(t, 15, scalar[int](t, raw, "PRAGMA user_version"))
	require.Equal(t, mapped, statsDump(t, raw, "client"))
}
