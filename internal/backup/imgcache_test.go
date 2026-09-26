package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The image cache is disposable: no export and no nightly snapshot may carry it.
func TestBackupsExcludeTheImageCache(t *testing.T) {
	db := openDB(t)
	seed(t, db, 20)
	data := filepath.Dir(db.BackupDir())
	shard := filepath.Join(data, "imgcache", "v1", "ab")
	require.NoError(t, os.MkdirAll(shard, 0o700))
	big := bytes.Repeat([]byte{7}, 3<<20)
	require.NoError(t, os.WriteFile(filepath.Join(shard, "ab0123456789abcdef0123456789abcd"), big, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(data, "imgcache", "index.db"), []byte("index"), 0o600))

	m := newManager(t, db)
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	body := readAll(t, d)
	require.NoError(t, d.Close())
	require.Less(t, len(body), len(big), "the export is far smaller than the cached bytes")
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	for _, f := range zr.File {
		require.NotContains(t, strings.ToLower(f.Name), "imgcache", f.Name)
	}

	// The nightly snapshot and the pre-migration copies live in the backup directory,
	// which does not contain the cache; health counts the two separately.
	_, err = db.WriteSnapshot(context.Background(), time.Now().Unix(), false)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(db.BackupDir(), "imgcache"))
	err = filepath.Walk(db.BackupDir(), func(p string, _ os.FileInfo, err error) error {
		require.NoError(t, err)
		require.NotContains(t, p, "imgcache")
		return nil
	})
	require.NoError(t, err)
	u := db.DiskUsage()
	require.Less(t, u.BackupBytes, int64(len(big)), "backup_bytes does not include the cache")
	require.GreaterOrEqual(t, u.ImgcacheBytes, int64(len(big)+len("index")))
}
