package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var deadSettingKeys = []string{"security.open_lan", "ui.font_size", "ui.font_ui", "ui.layouts", "stats.api_single_read_is_open", "sys.legacy_port"}

// schema10 builds a schema-10 database with the given settings rows and returns its path.
func schema10(t *testing.T, rows map[string]string) string {
	return schema10WithDevices(t, rows, nil)
}

// schema10WithDevices is schema10 plus device rows (id to profile JSON).
func schema10WithDevices(t *testing.T, rows, devices map[string]string) string {
	t.Helper()
	e := newEnv(t)
	e.exec("PRAGMA user_version = 10")
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

// 0011 also removes the three dead ui.* keys from device profiles, touching only
// the profiles that hold one and leaving every other key (and an empty profile) alone.
func TestMigration0011CleansDeviceProfiles(t *testing.T) {
	const keep = "0123456789abcdef"
	const dirty = "fedcba9876543210"
	const empty = "aaaaaaaaaaaaaaaa"
	db := reopen(t, schema10WithDevices(t, nil, map[string]string{
		keep:  `{"ui.theme":"fountain","client.layout":"inbox"}`,
		dirty: `{"ui.theme":"fountain","ui.font_size":20,"ui.font_ui":"inter","ui.layouts":{"a":1}}`,
		empty: `{}`,
	}))
	get := func(id string) string {
		return scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	}
	require.JSONEq(t, `{"ui.theme":"fountain","client.layout":"inbox"}`, get(keep))
	require.JSONEq(t, `{"ui.theme":"fountain"}`, get(dirty))
	require.JSONEq(t, `{}`, get(empty))
}

// A database with no devices at all migrates fine.
func TestMigration0011WithoutDevices(t *testing.T) {
	db := reopen(t, schema10(t, nil))
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM devices"))
}
