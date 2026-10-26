package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schema19WithDevices is a database at schema 19 with the given settings rows and device profiles
// (id to profile JSON), closed, so the next open runs 0020 on it. Returns its path.
func schema19WithDevices(t *testing.T, rows, devices map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kipple.db")
	raw, err := sql.Open("sqlite", buildDSN(path, "writer"))
	require.NoError(t, err)
	require.NoError(t, BuildSchema(t.Context(), raw, 19))
	for k, v := range rows {
		_, err = raw.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", k, v)
		require.NoError(t, err)
	}
	for id, profile := range devices {
		_, err = raw.Exec("INSERT INTO devices (id, settings, created_at, last_seen_at) VALUES (?, ?, 1, 1)", id, profile)
		require.NoError(t, err)
	}
	require.NoError(t, raw.Close())
	return path
}

// 0020 turns the Coffee break layout into the Digest with reading-time sections, in device profiles and in
// the defaults for new devices, and keeps every other key, including the other list overrides.
func TestMigration0020CoffeeToDigest(t *testing.T) {
	t.Parallel()
	const (
		layout   = "0123456789abcdef"
		override = "fedcba9876543210"
		digest   = "aaaaaaaaaaaaaaaa"
		plain    = "bbbbbbbbbbbbbbbb"
		huge     = "cccccccccccccccc"
	)
	big := strings.Repeat("x", 8000)
	db := reopen(t, schema19WithDevices(t,
		map[string]string{
			"ui.device_defaults": `{"client.layout":"coffee","client.order":"oldest"}`,
			"tz":                 `"UTC"`,
		},
		map[string]string{
			layout:   `{"ui.theme":"fountain","client.layout":"coffee","client.list_overrides":{"feed":{"12":{"layout":"inbox"}}}}`,
			override: `{"client.layout":"cards","client.list_overrides":{"feed":{"5":{"layout":"coffee","order":"oldest"},"6":{"layout":"headlines"}},"folder":{"3":{"layout":"coffee"}}}}`,
			digest:   `{"client.layout":"digest","client.digest_sections":"feed","client.list_overrides":{"feed":{"5":{"layout":"coffee"}}}}`,
			plain:    `{"ui.theme":"paper","client.layout":"magazine"}`,
			huge:     `{"client.layout":"coffee","client.pad":"` + big + `"}`,
		}))
	get := func(id string) string {
		return scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	}
	require.JSONEq(t, `{"ui.theme":"fountain","client.layout":"digest","client.digest_sections":"length","client.list_overrides":{"feed":{"12":{"layout":"inbox"}}}}`, get(layout))
	require.JSONEq(t, `{"client.layout":"cards","client.digest_sections":"length","client.list_overrides":{"feed":{"5":{"layout":"digest","order":"oldest"},"6":{"layout":"headlines"}},"folder":{"3":{"layout":"digest"}}}}`, get(override))
	require.JSONEq(t, `{"client.layout":"digest","client.digest_sections":"feed","client.list_overrides":{"feed":{"5":{"layout":"digest"}}}}`, get(digest), "a Digest default keeps its own sections")
	require.JSONEq(t, `{"ui.theme":"paper","client.layout":"magazine"}`, get(plain))
	require.NotContains(t, get(huge), "coffee")
	require.JSONEq(t, `{"client.layout":"digest","client.digest_sections":"length","client.order":"oldest"}`,
		scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'ui.device_defaults'"))
	require.Equal(t, `"UTC"`, scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'tz'"))
	requireCleanIntegrity(t, db.Reader())
}
