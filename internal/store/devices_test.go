package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func devID(i int) string { return fmt.Sprintf("device-id-%08d", i) }

func TestDeviceRegisterTouchAndProfile(t *testing.T) {
	e := newEnv(t)
	now := int64(1_800_000_000)
	_, found, _, err := e.db.TouchDevice(e.ctx, devID(1), "ua", "web", now)
	require.NoError(t, err)
	require.False(t, found, "touch never creates")
	dv, err := e.db.RegisterDevice(e.ctx, devID(1), "UA/1", "pwa", now)
	require.NoError(t, err)
	require.Equal(t, "pwa", dv.Client)
	require.Equal(t, now, dv.CreatedAt)

	// Under a day: no write. A day later: bumped.
	dv, found, touched, err := e.db.TouchDevice(e.ctx, devID(1), "UA/2", "web", now+3600)
	require.NoError(t, err)
	require.True(t, found)
	require.False(t, touched)
	require.Equal(t, now, dv.LastSeenAt)
	dv, _, touched, err = e.db.TouchDevice(e.ctx, devID(1), "UA/2", "web", now+DeviceTouchInterval)
	require.NoError(t, err)
	require.True(t, touched)
	require.Equal(t, now+DeviceTouchInterval, dv.LastSeenAt)
	got, ok, err := e.db.GetDevice(e.ctx, devID(1))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "UA/2", got.UserAgent)
	require.Equal(t, "web", got.Client)

	p, err := e.db.PatchDeviceProfile(e.ctx, devID(1), map[string]any{"ui.theme": "paper", "client.order": "oldest"})
	require.NoError(t, err)
	require.Len(t, p, 2)
	p, err = e.db.PatchDeviceProfile(e.ctx, devID(1), map[string]any{"ui.theme": nil})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"client.order": "oldest"}, p)
	_, err = e.db.PatchDeviceProfile(e.ctx, devID(9), map[string]any{"a": 1})
	require.ErrorIs(t, err, ErrDeviceNotFound)

	require.NoError(t, e.db.ReplaceDeviceProfile(e.ctx, devID(1), map[string]any{"ui.font_size": 20}))
	got, _, _ = e.db.GetDevice(e.ctx, devID(1))
	require.Equal(t, map[string]any{"ui.font_size": float64(20)}, got.Profile)
	require.ErrorIs(t, e.db.ReplaceDeviceProfile(e.ctx, devID(9), nil), ErrDeviceNotFound)

	require.NoError(t, e.db.SetDeviceName(e.ctx, devID(1), "Phone"))
	got, _, _ = e.db.GetDevice(e.ctx, devID(1))
	require.Equal(t, "Phone", got.Name)
	require.ErrorIs(t, e.db.SetDeviceName(e.ctx, devID(9), "x"), ErrDeviceNotFound)
}

func TestDeviceProfileSizeLimit(t *testing.T) {
	e := newEnv(t)
	_, err := e.db.RegisterDevice(e.ctx, devID(1), "", "web", 1)
	require.NoError(t, err)
	big := map[string]any{"client.voice": strings.Repeat("x", MaxDeviceProfileBytes)}
	_, err = e.db.PatchDeviceProfile(e.ctx, devID(1), big)
	require.ErrorIs(t, err, ErrDeviceProfileTooLarge)
	require.ErrorIs(t, e.db.ReplaceDeviceProfile(e.ctx, devID(1), big), ErrDeviceProfileTooLarge)
	// Just under the limit is fine, and the failed writes left the row alone.
	ok := map[string]any{"k": strings.Repeat("x", MaxDeviceProfileBytes-10)}
	_, err = e.db.PatchDeviceProfile(e.ctx, devID(1), ok)
	require.NoError(t, err)
}

func TestDeviceEvictionKeepsNewestAndTheNewcomer(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < MaxDevices; i++ {
		_, err := e.db.RegisterDevice(e.ctx, devID(i), "", "web", int64(1000+i))
		require.NoError(t, err)
	}
	require.Equal(t, MaxDevices, e.count("SELECT count(*) FROM devices"))
	// The oldest seen (id 0) is touched a day+ later, so id 1 is now the least recent.
	_, _, touched, err := e.db.TouchDevice(e.ctx, devID(0), "", "web", 1000+DeviceTouchInterval)
	require.NoError(t, err)
	require.True(t, touched)
	// A newcomer with the oldest possible time still survives its own eviction pass.
	_, err = e.db.RegisterDevice(e.ctx, devID(999), "", "web", 1)
	require.NoError(t, err)
	require.Equal(t, MaxDevices, e.count("SELECT count(*) FROM devices"))
	_, ok, _ := e.db.GetDevice(e.ctx, devID(999))
	require.True(t, ok)
	_, ok, _ = e.db.GetDevice(e.ctx, devID(0))
	require.True(t, ok, "recently seen device kept")
	_, ok, _ = e.db.GetDevice(e.ctx, devID(1))
	require.False(t, ok, "least recently seen device evicted")
}

func TestPurgeDevicesAndDelete(t *testing.T) {
	e := newEnv(t)
	now := int64(1_800_000_000)
	old := now - int64(DeviceMaxAgeDays)*86400 - 1
	for i, ts := range []int64{old, old - 10, now - 86400} {
		_, err := e.db.RegisterDevice(e.ctx, devID(i), "", "web", ts)
		require.NoError(t, err)
	}
	n, err := e.db.PurgeDevices(e.ctx, now, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "batch limit")
	n, err = e.db.PurgeDevices(e.ctx, now, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	list, err := e.db.ListDevices(e.ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, devID(2), list[0].ID)
	ok, err := e.db.DeleteDevice(e.ctx, devID(2))
	require.NoError(t, err)
	require.True(t, ok)
	ok, _ = e.db.DeleteDevice(e.ctx, devID(2))
	require.False(t, ok)
}

func TestValidDeviceID(t *testing.T) {
	require.True(t, ValidDeviceID("AAAAAAAAAAAAAAAAAAAAAA"))
	for _, s := range []string{"", "short", strings.Repeat("a", 33), "has space......chars!!", "a/b" + strings.Repeat("c", 20)} {
		require.False(t, ValidDeviceID(s), s)
	}
}

func TestCanonicalTheme(t *testing.T) {
	require.Equal(t, "paper", CanonicalTheme("white"))
	require.Equal(t, "midnight", CanonicalTheme("oled"))
	require.Equal(t, "system", CanonicalTheme("system"))
	require.Equal(t, "fountain", CanonicalTheme("fountain"))
}
