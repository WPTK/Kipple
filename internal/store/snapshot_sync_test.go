package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The nightly snapshot's rename is made durable: the backup directory is
// fsynced after the rename, and a failure there is reported, not swallowed.
func TestSnapshotSyncsBackupDirAfterRename(t *testing.T) {
	e := newEnv(t)
	final := filepath.Join(e.db.BackupDir(), SnapshotName)
	var synced []string
	syncDir = func(dir string) error {
		_, err := os.Stat(final)
		require.NoError(t, err, "the directory is synced after the rename")
		synced = append(synced, dir)
		return nil
	}
	t.Cleanup(func() { syncDir = realSyncDir })

	_, err := e.db.WriteSnapshot(e.ctx, 1_800_000_000, false)
	require.NoError(t, err)
	require.Equal(t, []string{e.db.BackupDir()}, synced)

	syncDir = func(string) error { return errors.New("disk on fire") }
	_, err = e.db.WriteSnapshot(e.ctx, 1_800_000_100, false)
	require.ErrorContains(t, err, "fsync backup dir")

	// The real one works on this platform (a no-op on Windows).
	require.NoError(t, realSyncDir(t.TempDir()))
}

var realSyncDir = syncDir
