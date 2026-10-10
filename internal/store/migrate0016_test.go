package store

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schema15WithDevices is a database at schema 15 with the given settings rows and device profiles
// (id to profile JSON), closed, so the next open runs 0016 on it. Returns its path.
func schema15WithDevices(t *testing.T, rows, devices map[string]string) string {
	t.Helper()
	e := newEnv(t)
	e.exec(undo0017)
	e.exec("PRAGMA user_version = 15")
	for k, v := range rows {
		e.exec("INSERT INTO settings (key, value) VALUES (?, ?)", k, v)
	}
	for id, profile := range devices {
		e.exec("INSERT INTO devices (id, settings, created_at, last_seen_at) VALUES (?, ?, 1, 1)", id, profile)
	}
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	return path
}

// 0016 carries every layout override into client.list_overrides as the "layout" field of its entry and
// removes the old key, in device profiles and in the defaults for new devices; other keys and profiles
// without the old key are left alone.
func TestMigration0016ListOverrides(t *testing.T) {
	t.Parallel()
	const (
		both  = "0123456789abcdef"
		feeds = "fedcba9876543210"
		none  = "aaaaaaaaaaaaaaaa"
		empty = "bbbbbbbbbbbbbbbb"
		odd   = "cccccccccccccccc"
	)
	db := reopen(t, schema15WithDevices(t,
		map[string]string{
			"ui.device_defaults": `{"client.layout":"cards","client.layout_overrides":{"feed":{"4":"inbox"},"folder":{}}}`,
			"tz":                 `"UTC"`,
		},
		map[string]string{
			both:  `{"ui.theme":"fountain","client.layout_overrides":{"feed":{"12":"inbox"},"folder":{"3":"compact"}}}`,
			feeds: `{"client.layout_overrides":{"feed":{"5":"headlines"}}}`,
			none:  `{"ui.theme":"paper","client.order":"oldest"}`,
			empty: `{}`,
			odd:   `{"client.layout_overrides":{"feed":{"6":"cards","7":3},"folder":"x"}}`,
		}))
	get := func(id string) string {
		return scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	}
	require.JSONEq(t, `{"ui.theme":"fountain","client.list_overrides":{"feed":{"12":{"layout":"inbox"}},"folder":{"3":{"layout":"compact"}}}}`, get(both))
	require.JSONEq(t, `{"client.list_overrides":{"feed":{"5":{"layout":"headlines"}},"folder":{}}}`, get(feeds))
	require.JSONEq(t, `{"ui.theme":"paper","client.order":"oldest"}`, get(none))
	require.JSONEq(t, `{}`, get(empty))
	require.JSONEq(t, `{"client.list_overrides":{"feed":{"6":{"layout":"cards"}},"folder":{}}}`, get(odd), "values the API never stores are dropped")

	dd, ok := settingRow(t, db, "ui.device_defaults")
	require.True(t, ok)
	require.JSONEq(t, `{"client.layout":"cards","client.list_overrides":{"feed":{"4":{"layout":"inbox"}},"folder":{}}}`, dd)
	tz, _ := settingRow(t, db, "tz")
	require.JSONEq(t, `"UTC"`, tz)
}

// oldOverrides is a client.layout_overrides value of n feed entries.
func oldOverrides(n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"%d":"headlines"`, 1000000+i)
	}
	return `{"feed":{` + strings.Join(entries, ",") + `}}`
}

// reopenLogged runs the pending migrations like reopen and returns what they logged.
func reopenLogged(t *testing.T, path string) (*DB, string) {
	t.Helper()
	var buf bytes.Buffer
	db, err := Open(context.Background(), Options{Path: path, Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	v, err := db.Version(context.Background())
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	return db, buf.String()
}

// A row the wrapper would push past its limits loses its layout overrides and keeps everything else,
// rather than failing the migration and the start, and the loss is logged. Limits: the new key's own
// budget (MaxListOverridesBytes) and the whole value's 8192 bytes, for a profile and for the defaults
// for new devices alike.
func TestMigration0016OverTheLimits(t *testing.T) {
	t.Parallel()
	const (
		budget = "dddddddddddddddd" // the new key passes its 4096-byte budget; the profile would still fit
		whole  = "eeeeeeeeeeeeeeee" // the whole profile passes 8192 bytes
		fits   = "ffffffffffffffff"
	)
	// 100 entries fit the key's budget once wrapped; with the padding, the whole profile then does not.
	pad := strings.Repeat("v", 5000)
	huge := `{"ui.theme":"paper","client.voice":"` + pad + `","client.layout_overrides":` + oldOverrides(100) + `}`
	require.Less(t, len(huge), 8192)
	require.Less(t, len(oldOverrides(100))+100*len(`{"layout":}`), 4096)
	path := schema15WithDevices(t,
		map[string]string{"ui.device_defaults": `{"client.layout":"cards","client.layout_overrides":` + oldOverrides(150) + `}`},
		map[string]string{
			budget: `{"ui.theme":"paper","client.layout_overrides":` + oldOverrides(150) + `}`,
			whole:  huge,
			fits:   `{"client.layout_overrides":` + oldOverrides(3) + `}`,
		})
	db, logged := reopenLogged(t, path)
	get := func(id string) string {
		return scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	}
	require.JSONEq(t, `{"ui.theme":"paper"}`, get(budget))
	require.JSONEq(t, `{"ui.theme":"paper","client.voice":"`+pad+`"}`, get(whole))
	require.Contains(t, get(fits), `"client.list_overrides"`)
	dd, ok := settingRow(t, db, "ui.device_defaults")
	require.True(t, ok)
	require.JSONEq(t, `{"client.layout":"cards"}`, dd)
	require.Contains(t, logged, "migration 0016: 2 device profile(s) lost their per-feed and per-folder layouts")
	require.Contains(t, logged, "migration 0016: the defaults for new devices lost their per-feed and per-folder layouts")
	require.Contains(t, logged, "level=WARN")
	// The temp tables lived on the migration's connection, which is the writer's only one: none is left there.
	require.Empty(t, writerTempTables(t, db))
}

// writerTempTables lists the temp tables on the writer's connection (it has one: SetMaxOpenConns(1)).
func writerTempTables(t *testing.T, db *DB) []string {
	t.Helper()
	conn, err := db.writer.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	rows, err := conn.QueryContext(context.Background(), "SELECT name FROM sqlite_temp_master WHERE type = 'table'")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// A migration that fails after creating temp.migration_notice rolls back whole: an error, user_version unchanged
// and no temp table left on the writer, so the next migration can create the table again and commit.
func TestMigrationNoticeRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	before, err := e.db.Version(ctx)
	require.NoError(t, err)
	bad := migration{version: before + 1, name: "9998_bad.sql", sql: `CREATE TEMP TABLE migration_notice (message TEXT NOT NULL);
INSERT INTO migration_notice VALUES ('should never be logged');
INSERT INTO no_such_table VALUES (1);`}
	require.Error(t, e.db.applyMigration(ctx, bad))
	v, err := e.db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, before, v)
	require.Empty(t, writerTempTables(t, e.db))

	good := migration{version: before + 1, name: "9999_good.sql", sql: `CREATE TEMP TABLE migration_notice (message TEXT NOT NULL);
INSERT INTO migration_notice VALUES ('a notice');`}
	require.NoError(t, e.db.applyMigration(ctx, good))
	v, err = e.db.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, v)
	require.Empty(t, writerTempTables(t, e.db))
}

// The 8192 cap is bytes, as the API measures ui.device_defaults: defaults whose text is under 8192 characters
// but over 8192 bytes once converted (non-ASCII voice name) lose their layout overrides.
func TestMigration0016DefaultsCountBytes(t *testing.T) {
	t.Parallel()
	voice := strings.Repeat("é", 3000) // 3000 characters, 6000 bytes
	old := `{"client.voice":"` + voice + `","client.layout_overrides":` + oldOverrides(70) + `}`
	converted := len(`{"client.voice":"`+voice+`","client.list_overrides":{"feed":{}}}`) + 70*len(`"1000000":{"layout":"headlines"},`)
	require.Less(t, len([]rune(old))+70*len(`{"layout":}`), 8192, "under the cap counted in characters")
	require.Greater(t, converted, 8192, "over it counted in bytes")
	db, logged := reopenLogged(t, schema15WithDevices(t, map[string]string{"ui.device_defaults": old}, nil))
	dd, ok := settingRow(t, db, "ui.device_defaults")
	require.True(t, ok)
	require.JSONEq(t, `{"client.voice":"`+voice+`"}`, dd)
	require.Contains(t, logged, "the defaults for new devices lost their per-feed and per-folder layouts")
}

// A migration without anything to report logs no warning.
func TestMigration0016NoNotice(t *testing.T) {
	t.Parallel()
	_, logged := reopenLogged(t, schema15WithDevices(t, nil, map[string]string{"gggggggggggggggg": `{"client.layout_overrides":` + oldOverrides(2) + `}`}))
	require.NotContains(t, logged, "level=WARN")
}

// Without devices or defaults the migration has nothing to do.
func TestMigration0016Empty(t *testing.T) {
	t.Parallel()
	db := reopen(t, schema15WithDevices(t, nil, nil))
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM devices"))
	_, ok := settingRow(t, db, "ui.device_defaults")
	require.False(t, ok)
}
