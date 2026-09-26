//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixOwnershipWhenRunningAsRoot(t *testing.T) {
	dir := t.TempDir()
	var chowned []string
	var uid, gid int
	oldE, oldC := geteuid, chown
	t.Cleanup(func() { geteuid, chown = oldE, oldC })
	chown = func(p string, u, g int) error { chowned = append(chowned, p); uid, gid = u, g; return nil }

	var out bytes.Buffer
	geteuid = func() int { return 1000 }
	fixOwnership(&out, dir, "a")
	require.Empty(t, chowned, "not root: nothing to do")
	require.Empty(t, out.String())

	geteuid = func() int { return 0 }
	if os.Getuid() == 0 {
		t.Skip("the data dir is root-owned here, so there is nobody to hand files to")
	}
	fixOwnership(&out, dir, "a", "b")
	require.Equal(t, []string{"a", "b"}, chowned)
	require.Equal(t, os.Getuid(), uid, "files go to the data directory's owner")
	_ = gid
	require.Contains(t, out.String(), "running as root")
}

// Run as root, restore also hands kipple.lock and a backup/ directory it
// created to the data directory's owner, even when it stops before the swap.
func TestRestoreAsRootChownsLockAndBackupDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the data dir is root-owned here, so there is nobody to hand files to")
	}
	oldE, oldC := geteuid, chown
	t.Cleanup(func() { geteuid, chown = oldE, oldC })
	var chowned []string
	geteuid = func() int { return 0 }
	chown = func(p string, u, g int) error { chowned = append(chowned, p); return nil }

	dir := newData(t, 3)
	zipPath := export(t, dir)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "backup"))) // the export's scratch; start without backup/
	lockPath := filepath.Join(dir, "kipple.lock")

	_, err := doRestore(dir, zipPath, false)
	require.ErrorIs(t, err, errNotConfirmed)
	require.Equal(t, []string{lockPath}, chowned, "a refused restore still hands back the lock it created")

	chowned = nil
	_, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)
	backupDir := filepath.Join(dir, "backup")
	require.Contains(t, chowned, lockPath)
	require.Contains(t, chowned, backupDir, "backup/ was created by this restore")
	require.Contains(t, chowned, filepath.Join(dir, "kipple.db"))
	require.Contains(t, chowned, filepath.Join(backupDir, "pre-restore-20260925-120000Z"))

	// A second restore: backup/ already existed, so it is not handed over again.
	chowned = nil
	_, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)
	require.NotContains(t, chowned, backupDir)
	require.Contains(t, chowned, lockPath)
}
