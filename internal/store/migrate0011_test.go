package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var deadSettingKeys = []string{"security.open_lan", "ui.font_size", "ui.font_ui", "ui.layouts", "stats.api_single_read_is_open", "sys.legacy_port"}

// schema10 builds a schema-10 database with the given settings rows and returns its path.
func schema10(t *testing.T, rows map[string]string) string {
	t.Helper()
	e := newEnv(t)
	e.exec("PRAGMA user_version = 10")
	for k, v := range rows {
		e.exec("INSERT INTO settings (key, value) VALUES (?, ?)", k, v)
	}
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())
	return path
}

// 0011 deletes the dead rows and leaves every other setting alone.
func TestMigration0011DropsTheDeadSettings(t *testing.T) {
	rows := map[string]string{"tz": `"Europe/Paris"`, "sys.setup_completed_at": "123", "security.allowed_hosts": `["nas"]`}
	for _, k := range deadSettingKeys {
		rows[k] = "true"
	}
	db := reopen(t, schema10(t, rows))
	for _, k := range deadSettingKeys {
		_, ok := settingRow(t, db, k)
		require.False(t, ok, k)
	}
	for _, k := range []string{"tz", "sys.setup_completed_at", "security.allowed_hosts"} {
		_, ok := settingRow(t, db, k)
		require.True(t, ok, k)
	}
}

// A database without those rows migrates unchanged.
func TestMigration0011WithoutTheRows(t *testing.T) {
	db := reopen(t, schema10(t, map[string]string{"tz": `"UTC"`}))
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM settings WHERE key = 'tz'"))
	for _, k := range deadSettingKeys {
		_, ok := settingRow(t, db, k)
		require.False(t, ok, k)
	}
}
