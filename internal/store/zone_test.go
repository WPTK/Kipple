package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// store.Zone: TZ env > tz setting > UTC, and a stored name that does not
// resolve (or "Local", which would silently mean the process zone) is UTC.
func TestZonePrecedence(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	t.Cleanup(func() { _ = SetEnvZone("") })
	for _, tc := range []struct {
		name, env, setting, want, wantName string
	}{
		{"default", "", "", "UTC", "UTC"},
		{"setting", "", "Asia/Tokyo", "Asia/Tokyo", "Asia/Tokyo"},
		{"env wins", "America/Chicago", "Asia/Tokyo", "America/Chicago", "America/Chicago"},
		{"env alone", "Europe/Paris", "", "Europe/Paris", "Europe/Paris"},
		{"unknown setting", "", "Mars/Base", "UTC", "Mars/Base"},
		{"Local setting", "", "Local", "UTC", "Local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, SetEnvZone(tc.env))
			set := map[string]any{"tz": nil}
			if tc.setting != "" {
				set["tz"] = tc.setting
			}
			require.NoError(t, e.db.SetSettings(ctx, set))
			require.Equal(t, tc.want, Zone(ctx, e.db.Reader()).String())
			require.Equal(t, tc.wantName, ZoneName(ctx, e.db.Reader()))
			name, ok := EnvZone()
			require.Equal(t, tc.env != "", ok)
			require.Equal(t, tc.env, name)
		})
	}
}

func TestSetEnvZoneRefusesBadNames(t *testing.T) {
	t.Cleanup(func() { _ = SetEnvZone("") })
	require.NoError(t, SetEnvZone("Asia/Tokyo"))
	for _, bad := range []string{"Local", "Not/AZone", string(make([]byte, 65))} {
		require.Error(t, SetEnvZone(bad), bad)
	}
	name, ok := EnvZone()
	require.True(t, ok)
	require.Equal(t, "Asia/Tokyo", name, "a refused name changes nothing")
	_, err := LoadZone("")
	require.Error(t, err)
}
