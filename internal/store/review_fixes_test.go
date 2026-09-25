package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiskUsageSumsTheBackupTreeRecursively(t *testing.T) {
	e := newEnv(t)
	dir := e.db.backupDir
	write := func(rel string, n int) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, make([]byte, n), 0o600))
	}
	base := e.db.DiskUsage().BackupBytes
	write("snap.db", 100)
	write("pre-restore-20260101/kipple.db", 1000)
	write("export/export-1.zip", 10000)
	require.EqualValues(t, base+11100, e.db.DiskUsage().BackupBytes)
}

func TestPatchFeedURLToAnotherHostDropsHTTPAuth(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	auth := func() string {
		return scalar[string](t, e.db.Reader(), "SELECT COALESCE(http_auth, '') FROM feeds WHERE id = ?", id)
	}
	patch := func(u string, cols map[string]any) {
		_, err := e.db.PatchFeed(e.ctx, id, FeedPatch{URL: &u, Cols: cols})
		require.NoError(t, err)
	}
	e.exec("UPDATE feeds SET http_auth = 'u:p' WHERE id = ?", id)

	patch("https://a.example/other", nil) // same host: kept
	require.Equal(t, "u:p", auth())
	patch("https://b.example/feed", nil) // another host: dropped
	require.Equal(t, "", auth())

	e.exec("UPDATE feeds SET http_auth = 'u:p' WHERE id = ?", id)
	patch("https://c.example/feed", map[string]any{"http_auth": "x:y"}) // set in the same patch: wins
	require.Equal(t, "x:y", auth())
}
