package lock

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAcquireExcludesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kipple.lock")
	l1, err := Acquire(path)
	require.NoError(t, err)

	// A second open file description in the same process still conflicts (flock
	// and LockFileEx both lock per open file).
	_, err = Acquire(path)
	require.ErrorIs(t, err, ErrLocked)

	require.NoError(t, l1.Release())
	require.NoError(t, l1.Release(), "release is idempotent")

	l2, err := Acquire(path)
	require.NoError(t, err, "the lock is free again after release")
	require.NoError(t, l2.Release())
}
