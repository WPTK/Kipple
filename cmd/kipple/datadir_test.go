package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureDataDirIsPrivateWhenCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "a", "data")
	require.NoError(t, ensureDataDir(dir))
	st, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm(), "a new data directory is 0700")

	// An existing directory is used as it is, never re-moded and never refused.
	existing := filepath.Join(t.TempDir(), "shared")
	require.NoError(t, os.Mkdir(existing, 0o755))
	require.NoError(t, os.Chmod(existing, 0o755))
	require.NoError(t, ensureDataDir(existing))
	st, err = os.Stat(existing)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), st.Mode().Perm())
}
