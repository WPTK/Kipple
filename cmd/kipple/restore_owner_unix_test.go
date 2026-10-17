//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
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

// chownPlain hands over a plain file or directory, and never what a link
// planted under the name points to. (Run unprivileged, the owner given is the
// caller's own, so the change itself is allowed and is a no-op.)
func TestChownPlainRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	file := filepath.Join(dir, "kipple.db")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	require.NoError(t, chownPlain(file, uid, gid))
	sub := filepath.Join(dir, "backup")
	require.NoError(t, os.Mkdir(sub, 0o700))
	require.NoError(t, chownPlain(sub, uid, gid))

	target := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	link := filepath.Join(dir, "kipple.lock")
	require.NoError(t, os.Symlink(target, link))
	require.ErrorIs(t, chownPlain(link, uid, gid), errNotPlain)
	dirLink := filepath.Join(dir, "backup-link")
	require.NoError(t, os.Symlink(sub, dirLink))
	require.ErrorIs(t, chownPlain(dirLink, uid, gid), errNotPlain)

	hard := filepath.Join(dir, "hard")
	require.NoError(t, os.Link(target, hard))
	require.ErrorIs(t, chownPlain(hard, uid, gid), errNotPlain)

	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	require.ErrorIs(t, chownPlain(fifo, uid, gid), errNotPlain)

	require.True(t, os.IsNotExist(chownPlain(filepath.Join(dir, "missing"), uid, gid)), "a missing path stays a not-exist error")

	// fixOwnership reports a refused path and carries on with the rest.
	oldE := geteuid
	t.Cleanup(func() { geteuid = oldE })
	geteuid = func() int { return 0 }
	if os.Getuid() == 0 {
		t.Skip("the data dir is root-owned here, so there is nobody to hand files to")
	}
	var out bytes.Buffer
	fixOwnership(&out, dir, link, file)
	require.Contains(t, out.String(), "could not give "+link)
	require.NotContains(t, out.String(), "could not give "+file)
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
