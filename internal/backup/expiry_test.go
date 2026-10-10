package backup

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A build that takes most of its allowance must still hand out a token that is
// good for a full TTL: expiry counts from when the export is ready.
func TestTokenTTLCountsFromReady(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	seed(t, db, 20)
	t0 := time.Now()
	var mu sync.Mutex
	calls := 0
	m := newManager(t, db, func(o *Options) {
		o.TTL = 5 * time.Minute
		o.Now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return t0 // the build starts
			}
			return t0.Add(9 * time.Minute) // and finishes nine minutes later
		}
	})
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	require.Equal(t, t0.Add(14*time.Minute).Unix(), exp.ExpiresAt.Unix(), "TTL from ready, not from the start")
	d, err := m.Take(exp.Token)
	require.NoError(t, err, "not already expired on arrival")
	require.NoError(t, d.Close())
}

func TestExportFilesArePrivate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	db := openDB(t)
	seed(t, db, 5)
	m := newManager(t, db)
	require.NoError(t, os.MkdirAll(m.o.Dir, 0o755)) // an older version's directory
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	st, err := os.Stat(m.o.Dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	defer d.Close()
	fi, err := d.File.Stat()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

func TestSnapshotToIsOwnerOnly(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	db := openDB(t)
	release, err := db.TrySnapshot()
	require.NoError(t, err)
	defer release()
	path := t.TempDir() + "/snap.db"
	require.NoError(t, db.SnapshotTo(context.Background(), path))
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	require.Error(t, db.SnapshotTo(context.Background(), path), "never overwrites")
}

func TestJobFailureLeavesNoFilesAndFreesTheSlot(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	seed(t, db, 5)
	m := newManager(t, db, func(o *Options) { o.FreeBytes = func(string) (uint64, error) { return 1, nil } })
	j, err := m.Start()
	require.NoError(t, err)
	select {
	case <-j.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("job did not finish")
	}
	st, err := m.Job(j.ID)
	require.NoError(t, err)
	require.Equal(t, JobFailed, st.Status)
	var ns *NoSpaceError
	require.ErrorAs(t, st.Err, &ns)
	entries, _ := os.ReadDir(m.o.Dir)
	require.Empty(t, entries)
	release, err := db.TrySnapshot()
	require.NoError(t, err, "the slot is released")
	release()
	_, err = m.Job("unknown")
	require.ErrorIs(t, err, ErrNoJob)
}
