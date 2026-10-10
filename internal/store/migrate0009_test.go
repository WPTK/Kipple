package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration0009FreshSchema(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	r := db.Reader()
	require.GreaterOrEqual(t, LatestVersion(), 9)
	for _, ix := range []string{"idx_stats_open_cov", "idx_stats_rt_cov", "idx_stats_scroll_cov"} {
		require.Contains(t, scalar[string](t, r, "SELECT sql FROM sqlite_master WHERE name = ?", ix), "WHERE kind = ", ix)
	}
	requireCleanIntegrity(t, r)
}

func TestMigration0009OnPopulatedSchema8(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		insertStatRow(e, "read_time", "sk", nil, 10+i)
	}
	insertStatRow(e, "open", "sk", nil, 0)
	before := scalar[string](t, e.db.Reader(), "SELECT group_concat(id || ':' || kind || ':' || coalesce(value, 'n'), '|') FROM (SELECT * FROM stats_events ORDER BY id)")
	e.exec(undo0009)
	e.exec(`PRAGMA user_version = 8`)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-8-*.db"))
	require.Len(t, snaps, 1)
	requireCleanIntegrity(t, db.Reader())
	require.Equal(t, before, scalar[string](t, db.Reader(), "SELECT group_concat(id || ':' || kind || ':' || coalesce(value, 'n'), '|') FROM (SELECT * FROM stats_events ORDER BY id)"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM stats_events INDEXED BY idx_stats_open_cov WHERE kind = 'open'"))
	require.Equal(t, 5, e.count("SELECT count(*) FROM stats_events INDEXED BY idx_stats_rt_cov WHERE kind = 'read_time'"))
}

func TestMigration0009RollsBackOnFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	insertStatRow(e, "read_time", "sk", nil, 10)
	e.exec(undo0009)
	e.exec(`PRAGMA user_version = 8`)
	ms, err := loadMigrations()
	require.NoError(t, err)
	bad := append([]migration(nil), ms...)
	last := bad[8]
	bad[8] = migration{version: 9, name: last.name, sql: last.sql + "\nCREATE TABLE items (a);"}
	e.db.migrations = bad
	require.Error(t, e.db.migrate(e.ctx))
	v, err := e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 8, v)
	require.Equal(t, 0, e.count("SELECT count(*) FROM sqlite_master WHERE name = 'idx_stats_open_cov'"))
	e.db.migrations = nil
	require.NoError(t, e.db.migrate(e.ctx))
	require.Equal(t, 1, e.count("SELECT count(*) FROM sqlite_master WHERE name = 'idx_stats_open_cov'"))
}

// An older binary (eight migrations) refuses a schema-9 database.
func TestOlderBinaryRefusesSchema9(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 9)
	db.migrations = ms[:8]
	require.ErrorContains(t, db.migrate(t.Context()), "newer than this binary")
}

// A nearly full volume refuses the upgrade before anything is written: no snapshot, the schema
// version and the indexes untouched, and the same database migrates once there is room.
func TestMigrationRefusesWhenDiskTooFull(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		insertStatRow(e, "read_time", "sk", nil, 10+i)
	}
	e.exec(undo0009)
	e.exec(`PRAGMA user_version = 8`)
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	orig := migrationFreeBytes
	t.Cleanup(func() { migrationFreeBytes = orig })
	migrationFreeBytes = func(string) (uint64, error) { return 1024, nil }
	_, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.ErrorContains(t, err, "not enough free disk space")
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-*.db"))
	require.Empty(t, snaps, "nothing was written")

	// Everything on one volume: room for the database's own growth is not enough, the snapshot must fit too.
	st, err := os.Stat(path)
	require.NoError(t, err)
	size := uint64(st.Size())
	migrationFreeBytes = func(string) (uint64, error) { return size + migrateHeadroom, nil }
	_, err = Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.ErrorContains(t, err, "not enough free disk space")

	migrationFreeBytes = orig
	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err, "the refusal changed nothing: the retry migrates")
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
}
