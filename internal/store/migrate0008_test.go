package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// undo0009 turns a current database into a schema-8 one: no stats summary covering indexes (and none of
// the later additive objects, undo0017).
const undo0009 = undo0017 + `;
DROP INDEX idx_stats_open_cov;
DROP INDEX idx_stats_rt_cov;
DROP INDEX idx_stats_scroll_cov`

// undo0008 turns a current database into a schema-7 one: no stats_events.event_id and no index on it
// (and, on top of it, none of the schema-9 indexes).
const undo0008 = undo0009 + `;
DROP INDEX idx_stats_event;
ALTER TABLE stats_events DROP COLUMN event_id`

func TestMigration0008FreshSchema(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	r := db.Reader()
	require.GreaterOrEqual(t, LatestVersion(), 8)
	require.True(t, columnNames(t, r, "stats_events")["event_id"])
	require.Contains(t, scalar[string](t, r, "SELECT sql FROM sqlite_master WHERE name = 'idx_stats_event'"), "UNIQUE")
	requireCleanIntegrity(t, r)
}

func insertStatRow(e *env, kind, key string, eventID any, value int) {
	e.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, value, session_key, event_id)
		VALUES (1, '2026-01-01', 0, 4, ?, 'web', 5, 1, 'F', ?, ?, ?)`, kind, value, key, eventID)
}

func TestMigration0008OnPopulatedSchema7(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		insertStatRow(e, "read_time", "sk", nil, 10+i) // pre-migration rows: no event_id, same session
	}
	insertStatRow(e, "open", "sk", nil, 0)
	e.exec(`UPDATE stats_events SET value = NULL WHERE kind = 'open'`)
	before := scalar[string](t, e.db.Reader(), "SELECT group_concat(id || ':' || kind || ':' || coalesce(value, 'n'), '|') FROM (SELECT * FROM stats_events ORDER BY id)")
	e.exec(undo0008)
	e.exec(`PRAGMA user_version = 7`)
	require.False(t, columnNames(t, e.db.Reader(), "stats_events")["event_id"])
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-7-*.db"))
	require.Len(t, snaps, 1)
	r := db.Reader()
	requireCleanIntegrity(t, r)
	require.Equal(t, before, scalar[string](t, r, "SELECT group_concat(id || ':' || kind || ':' || coalesce(value, 'n'), '|') FROM (SELECT * FROM stats_events ORDER BY id)"))
	require.Equal(t, 6, e.count("SELECT count(*) FROM stats_events WHERE event_id IS NULL"))

	// The unique index: an event_id may not repeat (any kind); NULLs may.
	insertStatRow(e, "read_time", "sk", "evt-00000001", 10)
	insertStatRow(e, "scroll", "sk", nil, 10)
	insertStatRow(e, "read_time", "sk", nil, 10)
	require.Error(t, db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, value, session_key, event_id)
			VALUES (1, '2026-01-01', 0, 4, 'share', 'web', 5, 1, 'F', NULL, NULL, 'evt-00000001')`)
		return err
	}))
}

func TestMigration0008RollsBackOnFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	insertStatRow(e, "read_time", "sk", nil, 10)
	e.exec(undo0008)
	e.exec(`PRAGMA user_version = 7`)
	ms, err := loadMigrations()
	require.NoError(t, err)
	bad := append([]migration(nil), ms...)
	last := bad[7]
	bad[7] = migration{version: 8, name: last.name, sql: last.sql + "\nCREATE TABLE items (a);"}
	e.db.migrations = bad
	require.Error(t, e.db.migrate(e.ctx))
	v, err := e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 7, v)
	require.False(t, columnNames(t, e.db.Reader(), "stats_events")["event_id"])
	e.db.migrations = nil
	require.NoError(t, e.db.migrate(e.ctx))
	require.True(t, columnNames(t, e.db.Reader(), "stats_events")["event_id"])
	require.Equal(t, 1, e.count("SELECT count(*) FROM stats_events"))
}

// An older binary (seven migrations) refuses a schema-8 database.
func TestOlderBinaryRefusesSchema8(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 8)
	db.migrations = ms[:7]
	require.ErrorContains(t, db.migrate(context.Background()), "newer than this binary")
}
