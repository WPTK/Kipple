package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// library creates a Kipple database in dir/kipple.db: with an account when
// account is set, and with the given settings. It is closed again (one file).
func library(t *testing.T, dir string, account bool, settings map[string]any) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	if account {
		_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
		require.NoError(t, err)
	}
	require.NoError(t, db.SetSettings(ctx, settings))
	require.NoError(t, db.Close())
}

func openLive(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func settingKeys(t *testing.T, path string) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query("SELECT key FROM settings")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		out[k] = true
	}
	return out
}

// A reset stages a fresh database that carries the server settings and nothing
// of the library, and the next start applies it as it applies a restore.
func TestStageResetThenApply(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	library(t, dir, true, map[string]any{
		store.SettingPublicURL:    "https://rss.example.test",
		store.SettingAllowedHosts: []string{"rss.example.test"},
		"ui.theme":                "paper",
	})
	live := openLive(t, dir)
	require.NoError(t, StageReset(context.Background(), dir, live.Reader(), "1.0.0", "owner", now))
	require.True(t, MarkerPending(dir))
	require.FileExists(t, filepath.Join(dir, StagedFile))
	require.NoFileExists(t, filepath.Join(dir, StagedFile+"-wal"))
	keys := settingKeys(t, filepath.Join(dir, StagedFile))
	require.True(t, keys[store.SettingPublicURL])
	require.True(t, keys[store.SettingAllowedHosts])
	require.False(t, keys["ui.theme"], "library settings are not carried over")

	require.ErrorIs(t, StageReset(context.Background(), dir, live.Reader(), "1.0.0", "owner", now), ErrRestorePending)
	require.NoError(t, live.Close())

	done, err := ApplyStaged(dir, now, time.UTC)
	require.NoError(t, err)
	require.True(t, done.Restored)
	require.FileExists(t, filepath.Join(done.Pre, "kipple.db"), "the library is kept")
	require.NoFileExists(t, filepath.Join(dir, MarkerFile))
	fresh := openLive(t, dir)
	_, ok, err := fresh.Account(context.Background())
	require.NoError(t, err)
	require.False(t, ok, "setup mode")
	sec, err := fresh.SecuritySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://rss.example.test", sec.PublicURL)
	require.Equal(t, []string{"rss.example.test"}, sec.AllowedHosts)
}

// Two requests at once: one marker, one winner.
func TestStageResetConcurrentHasOneWinner(t *testing.T) {
	dir := t.TempDir()
	library(t, dir, true, nil)
	live := openLive(t, dir)
	var ok, pending atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := StageReset(context.Background(), dir, live.Reader(), "1.0.0", "owner", time.Now()); err {
			case nil:
				ok.Add(1)
			case ErrRestorePending:
				pending.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, ok.Load())
	require.EqualValues(t, 7, pending.Load())
}

// An empty database (what setup mode creates) is deleted, never kept, so it
// cannot push a real safety copy out of the newest three.
func TestEmptyLiveDatabaseTakesNoSafetyCopy(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	library(t, dir, true, nil)
	staged := func() {
		other := t.TempDir()
		library(t, other, true, nil)
		require.NoError(t, os.Rename(filepath.Join(other, "kipple.db"), filepath.Join(dir, StagedFile)))
		require.NoError(t, writeMarker(filepath.Join(dir, MarkerFile), marker{}, false))
	}
	staged()
	first, err := ApplyStaged(dir, now, time.UTC) // the real library goes to pre-restore
	require.NoError(t, err)
	require.NotEmpty(t, first.Pre)
	// Now the live database has an account too (the staged one had). Empty it: a setup-mode database.
	require.NoError(t, os.Remove(filepath.Join(dir, "kipple.db")))
	for i := range KeepPreRestore + 2 {
		library(t, dir, false, nil)
		staged()
		done, err := ApplyStaged(dir, now.Add(time.Duration(i+1)*time.Minute), time.UTC)
		require.NoError(t, err)
		require.Empty(t, done.Pre, "an empty database is not kept")
		require.NoError(t, os.Remove(filepath.Join(dir, "kipple.db")))
	}
	require.DirExists(t, first.Pre, "the real library's copy is still there")
	dirs, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
	require.Len(t, dirs, 1)
}

// A database that holds an account moves as one file (checkpointed first); one
// that cannot be opened is kept as it is, all three files.
func TestKeepLiveMovesOneFileOrAllThree(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	t.Run("a healthy library", func(t *testing.T) {
		dir := t.TempDir()
		library(t, dir, true, nil)
		pre, undo, err := keepLive(dir, now)
		require.NoError(t, err)
		require.NotNil(t, undo)
		ents, _ := os.ReadDir(pre)
		require.Len(t, ents, 1)
		require.Equal(t, "kipple.db", ents[0].Name())
		require.NoFileExists(t, filepath.Join(dir, "kipple.db"))
	})
	t.Run("a database that cannot be opened", func(t *testing.T) {
		dir := t.TempDir()
		for _, s := range []string{"", "-wal", "-shm"} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "kipple.db"+s), []byte("not a database"), 0o600))
		}
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		ents, _ := os.ReadDir(pre)
		require.Len(t, ents, 3)
	})
}
