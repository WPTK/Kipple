package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// schema15WithDevices is a database at schema 15 with the given settings rows and device profiles
// (id to profile JSON), closed, so the next open runs 0016 on it. Returns its path.
func schema15WithDevices(t *testing.T, rows, devices map[string]string) string {
	t.Helper()
	e := newEnv(t)
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

// A profile that the wrapper would push past the devices CHECK (8192 bytes) loses its layout
// overrides and keeps everything else, rather than failing the migration and the start.
func TestMigration0016ProfileAtTheCap(t *testing.T) {
	const id = "dddddddddddddddd"
	entries := make([]string, 0, 400)
	profile := ""
	for i := 1; ; i++ {
		entries = append(entries, fmt.Sprintf(`"%d":"headlines"`, 1000000+i))
		next := `{"ui.theme":"paper","client.layout_overrides":{"feed":{` + strings.Join(entries, ",") + `}}}`
		if len(next) > 8000 {
			break
		}
		profile = next
	}
	db := reopen(t, schema15WithDevices(t, nil, map[string]string{id: profile}))
	got := scalar[string](t, db.Reader(), "SELECT settings FROM devices WHERE id = ?", id)
	require.JSONEq(t, `{"ui.theme":"paper"}`, got)
}

// Without devices or defaults the migration has nothing to do.
func TestMigration0016Empty(t *testing.T) {
	db := reopen(t, schema15WithDevices(t, nil, nil))
	require.Zero(t, scalar[int](t, db.Reader(), "SELECT count(*) FROM devices"))
	_, ok := settingRow(t, db, "ui.device_defaults")
	require.False(t, ok)
}
