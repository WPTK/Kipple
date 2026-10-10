package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// A new live database and its -wal and -shm are 0600, never world-readable,
// whatever the umask would give a file SQLite creates itself.
func TestNewDatabaseFilesArePrivate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://perm.example/feed"}) // a write, so the WAL exists
	require.NoError(t, err)
	for _, s := range []string{"", "-wal", "-shm"} {
		st, err := os.Stat(path + s)
		require.NoError(t, err, s)
		require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "kipple.db%s", s)
	}
}

// An existing database keeps its mode: the pre-create never touches it.
func TestExistingDatabaseModeIsKept(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := Open(ctx, Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	require.NoError(t, os.Chmod(path, 0o640))
	db, err = Open(ctx, Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), st.Mode().Perm())
}
