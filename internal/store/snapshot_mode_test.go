package store

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every copy of the database holds the password hashes and the account secret,
// so the pre-migration and nightly snapshots are 0600 like the export snapshot.
// Left to SQLite's VACUUM INTO they were 0644 (found in the restore rehearsal:
// pre-migration-3-5-*.db was -rw-r--r-- on the volume).
func TestSnapshotsArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	ctx := context.Background()
	db, _ := openTest(t)
	seedFeed(t, db)

	require.NoError(t, db.preMigrationSnapshot(ctx, 1, 2, nil))
	pre, err := filepath.Glob(filepath.Join(db.backupDir, "pre-migration-*.db"))
	require.NoError(t, err)
	require.Len(t, pre, 1)

	nightly, err := db.WriteSnapshot(ctx, 1_800_000_000, false)
	require.NoError(t, err)

	export := filepath.Join(t.TempDir(), "export.db")
	release, err := db.TrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SnapshotTo(ctx, export))
	release()

	for _, p := range []string{pre[0], nightly, export} {
		st, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), p)
		require.Greater(t, st.Size(), int64(0), "SQLite filled the pre-created file: %s", p)
	}
}

// The rollback procedure tells the operator to find the pre-migration snapshot by the name the app logs, so the
// log line names the file.
func TestPreMigrationSnapshotIsNamedInTheLog(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	var buf bytes.Buffer
	db.log = slog.New(slog.NewTextHandler(&buf, nil))

	require.NoError(t, db.preMigrationSnapshot(ctx, 16, 18, nil))
	files, err := filepath.Glob(filepath.Join(db.backupDir, "pre-migration-16-18-*.db"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Contains(t, buf.String(), "file="+filepath.Base(files[0]))
	require.NotContains(t, buf.String(), db.backupDir, "the name only, not the volume layout")
}
