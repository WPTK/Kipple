package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func devID(i int) string { return fmt.Sprintf("device-id-%08d", i) }

func TestDeviceRegisterTouchAndProfile(t *testing.T) {
	t.Parallel()
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

	require.NoError(t, e.db.ReplaceDeviceProfile(e.ctx, devID(1), map[string]any{"ui.list_density": "dense"}))
	got, _, _ = e.db.GetDevice(e.ctx, devID(1))
	require.Equal(t, map[string]any{"ui.list_density": "dense"}, got.Profile)
	require.ErrorIs(t, e.db.ReplaceDeviceProfile(e.ctx, devID(9), nil), ErrDeviceNotFound)

	require.NoError(t, e.db.SetDeviceName(e.ctx, devID(1), "Phone"))
	got, _, _ = e.db.GetDevice(e.ctx, devID(1))
	require.Equal(t, "Phone", got.Name)
	require.ErrorIs(t, e.db.SetDeviceName(e.ctx, devID(9), "x"), ErrDeviceNotFound)
}

func TestDeviceProfileSizeLimit(t *testing.T) {
	t.Parallel()
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

// fillDevices registers MaxDevices devices: the first nOld unseen for 60 days, the rest seen
// within the last day.
func fillDevices(t *testing.T, e *env, now int64, nOld int) {
	t.Helper()
	for i := 0; i < MaxDevices; i++ {
		ts := now - 3600 - int64(i)
		if i < nOld {
			ts = now - 60*86400 - int64(nOld-i) // id 0 is the oldest
		}
		_, err := e.db.RegisterDevice(e.ctx, devID(i), "", "web", ts)
		require.NoError(t, err)
	}
	require.Equal(t, MaxDevices, e.count("SELECT count(*) FROM devices"))
}

func TestDeviceEvictionTakesOnlyOldDevicesAndKeepsTheNewcomer(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := int64(1_800_000_000)
	fillDevices(t, e, now, 3)
	// The oldest (id 0) goes; the newcomer and every recent device stay.
	_, err := e.db.RegisterDevice(e.ctx, devID(999), "", "web", now)
	require.NoError(t, err)
	require.Equal(t, MaxDevices, e.count("SELECT count(*) FROM devices"))
	for id, want := range map[int]bool{999: true, 0: false, 1: true, 2: true, 3: true, MaxDevices - 1: true} {
		_, ok, _ := e.db.GetDevice(e.ctx, devID(id))
		require.Equal(t, want, ok, "device %d", id)
	}
	// A protected old device (the caller's own) is skipped: the next oldest goes instead.
	_, err = e.db.RegisterDevice(e.ctx, devID(998), "", "web", now, devID(1))
	require.NoError(t, err)
	_, ok, _ := e.db.GetDevice(e.ctx, devID(1))
	require.True(t, ok, "protected")
	_, ok, _ = e.db.GetDevice(e.ctx, devID(2))
	require.False(t, ok)
}

// With no device unseen for 30 days the cap refuses the newcomer and evicts nobody.
func TestDeviceRegistrationRefusedWhenEveryDeviceIsRecent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := int64(1_800_000_000)
	fillDevices(t, e, now, 0)
	_, err := e.db.RegisterDevice(e.ctx, devID(999), "", "web", now)
	require.ErrorIs(t, err, ErrDeviceLimit)
	require.Equal(t, MaxDevices, e.count("SELECT count(*) FROM devices"))
	_, ok, _ := e.db.GetDevice(e.ctx, devID(999))
	require.False(t, ok, "the insert was rolled back")
	for i := 0; i < MaxDevices; i++ {
		_, ok, _ = e.db.GetDevice(e.ctx, devID(i))
		require.True(t, ok, "device %d", i)
	}
	// Two old devices but a burst of three: the third is refused, the first two evicted nobody recent.
	e2 := newEnv(t)
	fillDevices(t, e2, now, 2)
	for i := 0; i < 2; i++ {
		_, err = e2.db.RegisterDevice(e2.ctx, devID(900+i), "", "web", now)
		require.NoError(t, err)
	}
	_, err = e2.db.RegisterDevice(e2.ctx, devID(902), "", "web", now)
	require.ErrorIs(t, err, ErrDeviceLimit)
	require.Equal(t, MaxDevices, e2.count("SELECT count(*) FROM devices"))
}

// Touching a device seen today is a read: it must not need the writer.
func TestTouchDeviceRecentDeviceDoesNotTakeTheWriter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	now := int64(1_800_000_000)
	_, err := e.db.RegisterDevice(e.ctx, devID(1), "ua", "web", now)
	require.NoError(t, err)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		dv, found, touched, err := e.db.TouchDevice(short, devID(1), "ua", "web", now+60)
		require.NoError(t, err)
		require.True(t, found)
		require.False(t, touched)
		require.Equal(t, devID(1), dv.ID)
		_, found, _, err = e.db.TouchDevice(short, devID(2), "ua", "web", now+60)
		require.NoError(t, err)
		require.False(t, found, "an unknown id is answered from the reader too")
		return nil
	}))
}

func TestPurgeDevicesAndDelete(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	require.True(t, ValidDeviceID("AAAAAAAAAAAAAAAAAAAAAA"))
	for _, s := range []string{"", "short", strings.Repeat("a", 33), "has space......chars!!", "a/b" + strings.Repeat("c", 20)} {
		require.False(t, ValidDeviceID(s), s)
	}
}

func TestCanonicalTheme(t *testing.T) {
	t.Parallel()
	require.Equal(t, "paper", CanonicalTheme("white"))
	require.Equal(t, "midnight", CanonicalTheme("oled"))
	require.Equal(t, "system", CanonicalTheme("system"))
	require.Equal(t, "fountain", CanonicalTheme("fountain"))
}
