package store

import (
	"database/sql"
	"fmt"
	"io/fs"
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

// statsRows is the seeded stats row count: large enough to time the rebuild, smaller under the race
// detector (CI), where it would take minutes.
func statsRows() int {
	if raceEnabled {
		return 20_000
	}
	return 200_000
}

// 0015, applied by the runner's transaction to a real schema-14 database, rewrites the Reader API
// client values to 'api' and keeps every other column of every row, the sequence high-water mark and
// every other schema object byte for byte. It is applied on its own, so a later migration cannot
// change what this test compares.
func TestMigration0015OneAPIClient(t *testing.T) {
	n := statsRows()
	raw, path := schema14(t)
	seedStats14(t, raw, n)
	seq := scalar[int64](t, raw, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'")
	require.EqualValues(t, n, seq)
	byClient := func() string {
		return scalar[string](t, raw, `SELECT group_concat(client || '=' || c, ',') FROM
			(SELECT client, count(*) AS c FROM stats_events GROUP BY client ORDER BY client)`)
	}
	require.Contains(t, byClient(), "reeder=", "the old values are seeded")
	oldSchema := schemaObjects(t, raw)
	require.Contains(t, oldSchema["table stats_events"], oldClientCheck)
	before := filepath.Join(filepath.Dir(path), "schema14.db")
	_, err := raw.Exec("VACUUM INTO ?", before)
	require.NoError(t, err)

	ms, err := loadMigrations()
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, (&DB{writer: raw}).applyMigration(t.Context(), ms[14]))
	t.Logf("migration 0015 alone, %d stats rows: %s", n-10, time.Since(start))
	require.Equal(t, 15, scalar[int](t, raw, "PRAGMA user_version"))
	requireCleanIntegrity(t, raw)

	// Row by row against the copy taken before, both ways: every column but client is unchanged (a
	// swapped or altered column makes a row differ) and client is mapped.
	_, err = raw.Exec("ATTACH DATABASE ? AS old", before)
	require.NoError(t, err)
	oldRows := "SELECT " + clientAfter0015 + ", " + statsCols + " FROM old.stats_events"
	newRows := "SELECT client, " + statsCols + " FROM main.stats_events"
	require.Equal(t, n-10, scalar[int](t, raw, "SELECT count(*) FROM old.stats_events"))
	require.Equal(t, n-10, scalar[int](t, raw, "SELECT count(*) FROM main.stats_events"))
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM ("+oldRows+" EXCEPT "+newRows+")"))
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM ("+newRows+" EXCEPT "+oldRows+")"))
	_, err = raw.Exec("DETACH DATABASE old")
	require.NoError(t, err)

	// n-10 rows over six client values: web and pwa keep theirs, the other four become api.
	per := (n - 10) / 6
	require.Equal(t, fmt.Sprintf("api=%d,pwa=%d,web=%d", n-10-2*per-1, per+1, per), byClient())
	require.Equal(t, seq, scalar[int64](t, raw, "SELECT seq FROM sqlite_sequence WHERE name = 'stats_events'"), "ids are never reused")
	require.Zero(t, scalar[int](t, raw, "SELECT count(*) FROM sqlite_sequence WHERE name = 'stats_events_new'"))

	// The schema is schema 14's with only the client CHECK changed: every index (the INDEXED BY ones
	// included), trigger, view and other table byte for byte, and stats_events the same columns.
	newSchema := schemaObjects(t, raw)
	oldTable, newTable := oldSchema["table stats_events"], newSchema["table stats_events"]
	delete(oldSchema, "table stats_events")
	delete(newSchema, "table stats_events")
	require.Equal(t, oldSchema, newSchema)
	require.Equal(t, tableShape(strings.Replace(oldTable, oldClientCheck, "CHECK (client IN ('web','pwa','api'))", 1)), tableShape(newTable))

	// The CHECK allows only the surviving values.
	ins := func(client string) error {
		_, err := raw.Exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title)
			VALUES (1, '2026-01-01', 0, 4, 'open', ?, 5, 1, 'F')`, client)
		return err
	}
	for _, c := range []string{"web", "pwa", "api"} {
		require.NoError(t, ins(c), c)
	}
	for _, c := range []string{"reeder", "netnewswire", "unread", "other", ""} {
		require.ErrorContains(t, ins(c), "CHECK", c)
	}
	require.EqualValues(t, seq+3, scalar[int64](t, raw, "SELECT max(id) FROM stats_events"), "the next id follows the carried mark")

	// The unique event id index still refuses a repeat.
	_, err = raw.Exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, event_id)
		VALUES (1, '2026-01-01', 0, 4, 'scroll', 'web', 5, 1, 'F', 'ev00000000000004')`)
	require.ErrorContains(t, err, "UNIQUE")
}

// The upgrade path through Open: the free-space check passes, a pre-migration snapshot is taken, and
// the peak disk use while it runs (sampled every few milliseconds) stays inside what the check
// reserves: the database file and its WAL grow by at most migrateRewriteFactor times the database plus
// the headroom, and everything with the snapshot by at most the whole reservation.
func TestMigration0015PeakSpaceWithinTheReservation(t *testing.T) {
	n := statsRows()
	raw, path := schema14(t)
	seedStats14(t, raw, n)
	_, err := raw.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	st, err := os.Stat(path)
	require.NoError(t, err)
	size := st.Size()
	dir := filepath.Dir(path)
	dbAndWAL := func() (total int64) {
		for _, f := range []string{path, path + "-wal"} {
			if st, err := os.Stat(f); err == nil {
				total += st.Size()
			}
		}
		return total
	}
	all := func() (total int64) {
		_ = filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				if fi, err := e.Info(); err == nil {
					total += fi.Size()
				}
			}
			return nil
		})
		return total
	}
	baseDB, baseAll := dbAndWAL(), all()
	var peakDB, peakAll int64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			peakDB, peakAll = max(peakDB, dbAndWAL()), max(peakAll, all())
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	db, err := Open(t.Context(), Options{Path: path})
	close(stop)
	<-done
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	snaps, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-migration-14-*.db"))
	require.Len(t, snaps, 1, "a pre-migration snapshot")
	t.Logf("open, snapshot and migrate from schema 14, %d stats rows, %d MB database: %s; peak growth of the database and WAL %.2fx, of everything with the snapshot %.2fx",
		n-10, size>>20, time.Since(start), float64(peakDB-baseDB)/float64(size), float64(peakAll-baseAll)/float64(size))
	require.LessOrEqual(t, peakDB-baseDB, migrateRewriteFactor*size+migrateHeadroom)
	require.LessOrEqual(t, peakAll-baseAll, migrateRewriteFactor*size+migrateHeadroom+int64(float64(size)*snapshotFreeFactor))
}

// The free-space reservation is declared per migration: a pending set without a table rebuild needs
// the database's size plus the headroom on its volume, one with a rebuild (0015) twice the size plus
// the headroom, each plus the snapshot when both share a volume. The refusal names the shortfall.
func TestMigrationSpaceFollowsTheDeclaredRebuild(t *testing.T) {
	raw, path := schema14(t)
	seedStats14(t, raw, 20_000)
	require.NoError(t, raw.Close())
	st, err := os.Stat(path)
	require.NoError(t, err)
	size := uint64(st.Size())
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.False(t, ms[13].marked(rebuildsTableMarker), "0014 only updates rows")
	require.True(t, ms[14].marked(rebuildsTableMarker), "0015 rebuilds stats_events")
	require.True(t, ms[11].marked(rebuildsTableMarker), "0012 rebuilds folders")
	require.True(t, ms[11].marked(foreignKeysOffMarker))

	orig := migrationFreeBytes
	t.Cleanup(func() { migrationFreeBytes = orig })
	d := &DB{path: path, backupDir: filepath.Join(filepath.Dir(path), "backup")}
	snap := uint64(float64(size) * snapshotFreeFactor)
	for _, tc := range []struct {
		name    string
		pending []migration
		need    uint64
	}{
		{"no rebuild", ms[13:14], size + migrateHeadroom + snap},
		{"with a rebuild", ms[13:15], migrateRewriteFactor*size + migrateHeadroom + snap},
	} {
		migrationFreeBytes = func(string) (uint64, error) { return tc.need - 3<<20, nil }
		err := d.checkMigrationSpace(13, 15, tc.pending)
		require.ErrorContains(t, err, "not enough free disk space", tc.name)
		require.ErrorContains(t, err, fmt.Sprintf("of the %d MB needed, so free at least 3 MB more", mbCeil(tc.need)), tc.name)
		migrationFreeBytes = func(string) (uint64, error) { return tc.need, nil }
		require.NoError(t, d.checkMigrationSpace(13, 15, tc.pending), tc.name)
	}
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
