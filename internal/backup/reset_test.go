package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A reset marker is "swap the database for nothing": the live database goes to
// backup/pre-restore-*, nothing replaces it, and every state a crash can leave
// is finished or cleaned at the next start.
func TestApplyStagedReset(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	write := func(p, body string) { require.NoError(t, os.WriteFile(p, []byte(body), 0o600)) }
	read := func(p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("a live database is kept and nothing replaces it", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "library")
		write(filepath.Join(dir, "kipple.db-wal"), "wal")
		write(filepath.Join(dir, "kipple.db-shm"), "shm")
		require.NoError(t, WriteResetMarker(dir, "1.0.0", "owner", now))
		require.True(t, MarkerPending(dir))
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.True(t, done.Reset)
		require.False(t, done.Restored)
		require.Equal(t, "owner", done.Username)
		require.NoFileExists(t, filepath.Join(dir, "kipple.db"))
		require.NoFileExists(t, filepath.Join(dir, "kipple.db-wal"))
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.Equal(t, "library", read(filepath.Join(done.Pre, "kipple.db")))
		require.Equal(t, "wal", read(filepath.Join(done.Pre, "kipple.db-wal")))
		require.Equal(t, "pre-restore-20261006-120000Z", filepath.Base(done.Pre), "the name of a restore's safety copy")
		require.False(t, MarkerPending(dir))
	})

	t.Run("without a live database it was already done", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, WriteResetMarker(dir, "1.0.0", "owner", now))
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.False(t, done.Reset)
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.NoDirExists(t, filepath.Join(dir, "backup"))
	})

	t.Run("a crash between the move and the marker removal", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "library")
		require.NoError(t, WriteResetMarker(dir, "1.0.0", "owner", now))
		pre, err := Retire(dir, now) // the move ran, then the power went
		require.NoError(t, err)
		done, err := ApplyStaged(dir, now.Add(time.Minute), time.UTC)
		require.NoError(t, err)
		require.False(t, done.Reset)
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.Equal(t, "library", read(filepath.Join(pre, "kipple.db")))
		dirs, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
		require.Len(t, dirs, 1)
	})

	t.Run("a restore marker still swaps the staged database in", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "live")
		write(filepath.Join(dir, StagedFile), "staged")
		write(filepath.Join(dir, MarkerFile), `{"kind":"restore","username":"owner"}`)
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.True(t, done.Restored)
		require.False(t, done.Reset)
		require.Equal(t, "staged", read(filepath.Join(dir, "kipple.db")))
		require.Equal(t, "live", read(filepath.Join(done.Pre, "kipple.db")))
	})

	t.Run("the retention of safety copies applies", func(t *testing.T) {
		dir := t.TempDir()
		for i := range KeepPreRestore + 1 {
			write(filepath.Join(dir, "kipple.db"), "library")
			require.NoError(t, WriteResetMarker(dir, "1.0.0", "owner", now))
			_, err := ApplyStaged(dir, now.Add(time.Duration(i)*time.Minute), time.UTC)
			require.NoError(t, err)
		}
		dirs, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
		require.Len(t, dirs, KeepPreRestore)
	})

	t.Run("a pending marker is never replaced", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, MarkerFile), `{"kind":"restore"}`)
		require.ErrorIs(t, WriteResetMarker(dir, "1.0.0", "owner", now), ErrRestorePending)
		require.JSONEq(t, `{"kind":"restore"}`, read(filepath.Join(dir, MarkerFile)))
	})
}
