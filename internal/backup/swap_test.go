package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailedSwapLeavesNoEmptyPreRestoreDir(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "kipple.db")
	require.NoError(t, os.WriteFile(live, []byte("live"), 0o600))
	require.NoError(t, os.WriteFile(live+"-wal", []byte("wal"), 0o600))

	pre, err := Swap(dir, filepath.Join(dir, "missing-tmp.db"), time.Now())
	require.Error(t, err)
	require.Empty(t, pre)
	b, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "live", string(b), "the live database was put back")
	require.FileExists(t, live+"-wal")
	dirs, err := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
	require.NoError(t, err)
	require.Empty(t, dirs, "the rollback removed the empty pre-restore directory")
}

// farFuture is a now at which every copy in these tests is old enough to prune.
var farFuture = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// A run of resets or restores in a short time never deletes a copy younger
// than KeepPreRestoreFor: the library replaced a day ago survives three more
// swaps in a row, and goes only once it is old and three newer ones exist.
func TestPruneKeepsRecentPreRestoreCopies(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backup")
	day0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	mk := func(at time.Time) string {
		d, err := newPreRestoreDir(backupDir, at)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(d, "kipple.db"), []byte("x"), 0o600))
		return d
	}
	library := mk(day0)
	var later []string
	for i := range 4 {
		later = append(later, mk(day0.Add(24*time.Hour+time.Duration(i)*time.Minute)))
	}
	now := day0.Add(24*time.Hour + time.Hour)
	PrunePreRestore(backupDir, now, time.Local)
	require.DirExists(t, library, "a day-old copy is kept however many came after it")
	for _, d := range later {
		require.DirExists(t, d)
	}
	PrunePreRestore(backupDir, now.Add(KeepPreRestoreFor), time.Local)
	require.NoDirExists(t, library, "old, and three newer copies exist")
	require.NoDirExists(t, later[0], "old, and three newer copies exist")
	for _, d := range later[1:] {
		require.DirExists(t, d, "the newest three are kept at any age")
	}
}

// Each copy is a whole library, so recent copies are capped too: past
// KeepPreRestoreMax the oldest goes, however young.
func TestPruneCapsRecentPreRestoreCopies(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backup")
	day0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var all []string
	for i := range KeepPreRestoreMax + 2 {
		d, err := newPreRestoreDir(backupDir, day0.Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(d, "kipple.db"), []byte("x"), 0o600))
		all = append(all, d)
	}
	PrunePreRestore(backupDir, day0.Add(time.Hour), time.Local)
	for _, d := range all[:2] {
		require.NoDirExists(t, d, "the oldest go past the cap")
	}
	for _, d := range all[2:] {
		require.DirExists(t, d)
	}
}

func TestPruneIgnoresEmptyPreRestoreDirs(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backup")
	full := func(name string) string {
		d := filepath.Join(backupDir, name)
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "kipple.db"), []byte("x"), 0o600))
		return d
	}
	oldest := full("pre-restore-20260101-000000")
	full("pre-restore-20260102-000000")
	full("pre-restore-20260103-000000")
	empty := filepath.Join(backupDir, "pre-restore-20260104-000000")
	require.NoError(t, os.MkdirAll(empty, 0o755))

	PrunePreRestore(backupDir, farFuture, time.Local)
	require.DirExists(t, oldest, "an empty directory does not push a real copy out")
	require.NoDirExists(t, empty)
}

// The -N suffix orders as a number (-10 is newer than -2) and a name that is
// not ours is never pruned.
func TestPrunePreRestoreOrdersSuffixNumerically(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backup")
	full := func(name string) string {
		d := filepath.Join(backupDir, name)
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "kipple.db"), []byte("x"), 0o600))
		return d
	}
	var all []string
	all = append(all, full("pre-restore-20260101-000000"))
	for i := 2; i <= 10; i++ {
		all = append(all, full(fmt.Sprintf("pre-restore-20260101-000000-%d", i)))
	}
	foreign := full("pre-restore-keep-me")
	PrunePreRestore(backupDir, farFuture, time.Local)
	for _, d := range all[:len(all)-3] {
		require.NoDirExists(t, d)
	}
	for _, d := range all[len(all)-3:] {
		require.DirExists(t, d, "the three newest (-8, -9, -10) are kept")
	}
	require.DirExists(t, foreign, "a name that does not parse is left alone")

	// The timestamp is compared as a time, not as text.
	at, n, ok := preRestoreKey("pre-restore-20260101-000000Z-12", time.Local)
	require.True(t, ok)
	require.Equal(t, 12, n)
	require.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), at)
	for _, bad := range []string{"pre-restore-2026", "pre-restore-20260101-000000-", "pre-restore-20260101-000000-x", "pre-restore-20261301-000000", "pre-restore-20260101-000000Z-", "pre-restore-20260101-000000ZZ", "pre-restore-20260101-000000z"} {
		_, _, ok := preRestoreKey(bad, time.Local)
		require.False(t, ok, bad)
	}
}

// Names are UTC, so the repeated hour at the end of daylight saving time can
// never make the newer directory sort first.
func TestPreRestoreNamesAreUTC(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	backupDir := filepath.Join(t.TempDir(), "backup")
	// 2026-11-01 01:30 EDT, then 01:10 EST (40 minutes later in real time).
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).In(ny)
	second := time.Date(2026, 11, 1, 6, 10, 0, 0, time.UTC).In(ny)
	require.True(t, first.Format("150405") > second.Format("150405"), "local wall clock goes backwards")
	a, err := newPreRestoreDir(backupDir, first)
	require.NoError(t, err)
	b, err := newPreRestoreDir(backupDir, second)
	require.NoError(t, err)
	require.Equal(t, "pre-restore-20261101-053000Z", filepath.Base(a))
	require.Equal(t, "pre-restore-20261101-061000Z", filepath.Base(b))
	ka, _, _ := preRestoreKey(filepath.Base(a), time.Local)
	kb, _, _ := preRestoreKey(filepath.Base(b), time.Local)
	require.True(t, ka.Before(kb))
}

// Across the upgrade the backup directory holds zone-less names written in the
// server's local time by older versions next to new UTC (Z) names. On a host
// east of UTC the old names read as UTC would look hours newer than they are
// and pruning would delete the new copy; they are read as local time instead.
func TestPrunePreRestoreMixesLegacyLocalAndUTCNames(t *testing.T) {
	local := time.FixedZone("UTC+3", 3*3600)

	at, _, ok := preRestoreKey("pre-restore-20260926-120000", local)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC), at, "a legacy name is local time")
	at, _, ok = preRestoreKey("pre-restore-20260926-120000Z", local)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), at)

	backupDir := filepath.Join(t.TempDir(), "backup")
	full := func(name string) string {
		d := filepath.Join(backupDir, name)
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "kipple.db"), []byte("x"), 0o600))
		return d
	}
	oldest := full("pre-restore-20260926-110000") // 08:00 UTC
	full("pre-restore-20260926-120000")           // 09:00 UTC
	full("pre-restore-20260926-130000")           // 10:00 UTC
	// The first restore after the upgrade, at 10:30 UTC (13:30 local).
	newest, err := newPreRestoreDir(backupDir, time.Date(2026, 9, 26, 10, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(newest, "kipple.db"), []byte("x"), 0o600))
	require.Equal(t, "pre-restore-20260926-103000Z", filepath.Base(newest))

	PrunePreRestore(backupDir, farFuture, local)
	require.DirExists(t, newest, "the newest copy is kept")
	require.NoDirExists(t, oldest, "the oldest (a legacy one) goes")
}
