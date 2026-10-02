package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// store.Zone is the tz setting, else UTC; a stored name that does not resolve
// (or "Local", which would silently mean the process zone) is UTC.
func TestZoneIsTheSetting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, setting, want, wantName string
	}{
		{"default", "", "UTC", "UTC"},
		{"setting", "Asia/Tokyo", "Asia/Tokyo", "Asia/Tokyo"},
		{"unknown setting", "Mars/Base", "UTC", "Mars/Base"},
		{"Local setting", "Local", "UTC", "Local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := map[string]any{"tz": nil}
			if tc.setting != "" {
				set["tz"] = tc.setting
			}
			require.NoError(t, e.db.SetSettings(ctx, set))
			require.Equal(t, tc.want, Zone(ctx, e.db.Reader()).String())
			require.Equal(t, tc.wantName, ZoneName(ctx, e.db.Reader()))
		})
	}
	_, err := LoadZone("")
	require.Error(t, err)
	_, err = LoadZone("Local")
	require.Error(t, err)
}

// TZ only gives a new install its zone: it is stored when no tz row exists and is
// never read again once one does.
func TestSeedZone(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	require.NoError(t, e.db.SeedZone(ctx, ""), "no TZ seeds nothing")
	_, stored, err := StoredZoneName(ctx, e.db.Reader())
	require.NoError(t, err)
	require.False(t, stored)

	require.NoError(t, e.db.SeedZone(ctx, "America/Chicago"))
	require.Equal(t, "America/Chicago", ZoneName(ctx, e.db.Reader()))

	require.NoError(t, e.db.SeedZone(ctx, "Europe/Paris"), "a stored zone is not replaced")
	require.Equal(t, "America/Chicago", ZoneName(ctx, e.db.Reader()))
	require.NoError(t, e.db.SeedZone(ctx, "Not/AZone"), "TZ is not even checked once a row exists")
	require.Equal(t, "America/Chicago", ZoneName(ctx, e.db.Reader()))

	require.NoError(t, e.db.SetSettings(ctx, map[string]any{"tz": "UTC"}))
	require.NoError(t, e.db.SeedZone(ctx, "Asia/Tokyo"), "an explicit UTC is a stored choice too")
	require.Equal(t, "UTC", ZoneName(ctx, e.db.Reader()))

	require.NoError(t, e.db.SetSettings(ctx, map[string]any{"tz": nil}))
	for _, bad := range []string{"Local", "Not/AZone", string(make([]byte, 65))} {
		require.Error(t, e.db.SeedZone(ctx, bad), bad)
	}
	require.Equal(t, "UTC", ZoneName(ctx, e.db.Reader()), "a refused name stores nothing")
}
