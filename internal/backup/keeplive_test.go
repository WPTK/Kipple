package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// stagedLibrary puts a real library (with an account) at dir/restore-staged.db and a marker.
func stagedLibrary(t *testing.T, dir string, settings map[string]any) {
	t.Helper()
	other := t.TempDir()
	library(t, other, true, settings)
	require.NoError(t, os.Rename(filepath.Join(other, "kipple.db"), filepath.Join(dir, StagedFile)))
	require.NoError(t, writeMarker(filepath.Join(dir, MarkerFile), marker{}, false))
}

func execSQL(t *testing.T, path, q string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(q)
	require.NoError(t, err)
}

func countRows(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
	return n
}

func preDirs(dir string) []string {
	d, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
	return d
}

// An empty database (what setup mode creates) is deleted, never kept, so it
// cannot push a real safety copy out of the newest three.
func TestEmptyLiveDatabaseTakesNoSafetyCopy(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	library(t, dir, true, nil)
	stagedLibrary(t, dir, nil)
	first, err := ApplyStaged(dir, now, time.UTC) // the real library goes to pre-restore
	require.NoError(t, err)
	require.NotEmpty(t, first.Pre)
	for i := range KeepPreRestore + 2 {
		require.NoError(t, os.Remove(filepath.Join(dir, "kipple.db")))
		library(t, dir, false, nil) // setup mode's empty database
		stagedLibrary(t, dir, nil)
		done, err := ApplyStaged(dir, now.Add(time.Duration(i+1)*time.Minute), time.UTC)
		require.NoError(t, err)
		require.Empty(t, done.Pre, "an empty database is not kept")
	}
	require.DirExists(t, first.Pre, "the real library's copy is still there")
	require.Len(t, preDirs(dir), 1)
}

// keepLive decides from what it reads: only a database that is provably empty
// is deleted; everything else is kept, as one file when it could be opened.
func TestKeepLive(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	liveDB := func(dir string) string { return filepath.Join(dir, "kipple.db") }

	t.Run("committed data still in the -wal is checkpointed and moves as one file", func(t *testing.T) {
		src := t.TempDir()
		ctx := context.Background()
		db, err := store.Open(ctx, store.Options{Path: liveDB(src), Logger: quiet})
		require.NoError(t, err)
		_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
		require.NoError(t, err)
		// A crash image: the files as they are while the server still has the
		// database open, so the account lives only in the -wal.
		dir := t.TempDir()
		for _, s := range []string{"", "-wal", "-shm"} {
			b, err := os.ReadFile(liveDB(src) + s)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(liveDB(dir)+s, b, 0o600))
		}
		require.NoError(t, db.Close())
		fi, err := os.Stat(liveDB(dir) + "-wal")
		require.NoError(t, err)
		require.NotZero(t, fi.Size(), "the committed data is in the -wal")
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		ents, _ := os.ReadDir(pre)
		require.Len(t, ents, 1, "one file: nothing is left of the -wal or -shm")
		require.Equal(t, "kipple.db", ents[0].Name())
		require.Equal(t, 1, countRows(t, filepath.Join(pre, "kipple.db"), "account"), "the account came with it")
		require.NoFileExists(t, liveDB(dir)+"-wal")
	})
	t.Run("feeds but no account is kept", func(t *testing.T) {
		dir := t.TempDir()
		library(t, dir, false, nil)
		execSQL(t, liveDB(dir), `INSERT INTO feeds (url, url_key, host, title) VALUES ('http://a.test/rss', 'a.test/rss', 'a.test', 'A')`)
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		require.NotEmpty(t, pre)
		require.Equal(t, 1, countRows(t, filepath.Join(pre, "kipple.db"), "feeds"))
	})
	t.Run("an unreadable account table is kept", func(t *testing.T) {
		dir := t.TempDir()
		library(t, dir, true, nil)
		execSQL(t, liveDB(dir), `DROP TABLE account`)
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		require.NotEmpty(t, pre)
		require.FileExists(t, filepath.Join(pre, "kipple.db"))
	})
	t.Run("a file that is not a database is kept with its -wal and -shm", func(t *testing.T) {
		dir := t.TempDir()
		for _, s := range []string{"", "-wal", "-shm"} {
			require.NoError(t, os.WriteFile(liveDB(dir)+s, []byte("not a database"+s), 0o600))
		}
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		for _, s := range []string{"", "-wal", "-shm"} {
			b, err := os.ReadFile(filepath.Join(pre, "kipple.db"+s))
			require.NoError(t, err)
			require.Equal(t, "not a database"+s, string(b))
		}
		ents, _ := os.ReadDir(pre)
		require.Len(t, ents, 3, "no copy of the -wal or -shm is left over")
	})
	t.Run("a provably empty database is deleted and leaves no directory", func(t *testing.T) {
		dir := t.TempDir()
		library(t, dir, false, nil)
		pre, _, err := keepLive(dir, now)
		require.NoError(t, err)
		require.Empty(t, pre)
		require.NoFileExists(t, liveDB(dir))
		require.Empty(t, preDirs(dir))
	})
	t.Run("copies a crash left are cleaned", func(t *testing.T) {
		dir := t.TempDir()
		old := filepath.Join(dir, "backup", "pre-restore-20260101-000000Z")
		require.NoError(t, os.MkdirAll(old, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(old, "kipple.db"), []byte("x"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(old, copyPrefix+"123"), []byte("stale"), 0o600))
		library(t, dir, true, nil)
		_, _, err := keepLive(dir, now)
		require.NoError(t, err)
		require.NoFileExists(t, filepath.Join(old, copyPrefix+"123"))
		require.FileExists(t, filepath.Join(old, "kipple.db"))
	})
}

// A restore keeps the address this instance answers at: the backup's own
// address settings are dropped and the live instance's are carried into the
// staged copy (the same helper a reset uses).
func TestRestoreKeepsTheLiveAddressSettings(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	// The instance a reset left: empty, with the kept address.
	library(t, dir, false, map[string]any{
		store.SettingPublicURL:    "https://rss.example.test",
		store.SettingAllowedHosts: []string{"rss.example.test"},
	})
	stagedLibrary(t, dir, map[string]any{
		store.SettingPublicURL:      "https://old.example.test",
		store.SettingTrustedProxies: []string{"10.0.0.1"},
		"ui.theme":                  "paper",
	})
	live := openLive(t, dir)
	require.NoError(t, prepareStaged(context.Background(), filepath.Join(dir, StagedFile), "", live.Reader()))
	require.NoError(t, live.Close())
	done, err := ApplyStaged(dir, now, time.UTC)
	require.NoError(t, err)
	require.True(t, done.Restored)
	restored := openLive(t, dir)
	sec, err := restored.SecuritySettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, "https://rss.example.test", sec.PublicURL, "this instance's address, not the backup's")
	require.Equal(t, []string{"rss.example.test"}, sec.AllowedHosts)
	require.Empty(t, sec.TrustedProxies, "the backup's proxies describe the old server")
	_, ok, err := restored.Account(context.Background())
	require.NoError(t, err)
	require.True(t, ok, "the backup's library came")
}
