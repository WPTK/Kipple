package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// goodBackup returns the bytes of a real backup zip.
func goodBackup(t *testing.T) []byte {
	t.Helper()
	db := openDB(t)
	seed(t, db, 5)
	m := newManager(t, db)
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	defer d.Close()
	return readAll(t, d)
}

// A manifest (and zip entry) declaring a database over DefaultMaxDBBytes is
// refused before a byte is written, although the entry holds almost nothing.
func TestExtractRefusesOversizedDeclaredDB(t *testing.T) {
	good := goodBackup(t)
	zr, err := zip.NewReader(bytes.NewReader(good), int64(len(good)))
	require.NoError(t, err)
	const huge = int64(DefaultMaxDBBytes) + 1
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		switch f.Name {
		case DBFile:
			// A stored entry whose header claims huge bytes; its data is a stub.
			w, err := zw.CreateRaw(&zip.FileHeader{Name: DBFile, Method: zip.Store,
				CompressedSize64: 4, UncompressedSize64: uint64(huge)})
			require.NoError(t, err)
			_, _ = w.Write([]byte("junk"))
			continue
		case ManifestFile:
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			var mf Manifest
			require.NoError(t, json.Unmarshal(b, &mf))
			mf.DBBytes = huge
			for i := range mf.Files {
				if mf.Files[i].Name == DBFile {
					mf.Files[i].Bytes = huge
				}
			}
			b, _ = json.Marshal(mf)
			w, _ := zw.Create(f.Name)
			_, _ = w.Write(b)
			continue
		}
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		w, _ := zw.Create(f.Name)
		_, _ = w.Write(b)
	}
	require.NoError(t, zw.Close())
	src := filepath.Join(t.TempDir(), "huge.zip")
	require.NoError(t, os.WriteFile(src, buf.Bytes(), 0o600))
	out := filepath.Join(t.TempDir(), "o.db")
	_, err = ExtractDB(src, out)
	require.ErrorContains(t, err, "a restore accepts")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))
}

// A database larger than the free space at the destination is refused before
// extracting; with room it restores.
func TestExtractRefusesWhenDiskTooSmall(t *testing.T) {
	good := goodBackup(t)
	src := filepath.Join(t.TempDir(), "good.zip")
	require.NoError(t, os.WriteFile(src, good, 0o600))
	dir := t.TempDir()
	var asked string
	restoreFreeBytes = func(d string) (uint64, error) { asked = d; return 1024, nil }
	t.Cleanup(func() { restoreFreeBytes = diskFree })

	out := filepath.Join(dir, "o.db")
	_, err := ExtractDB(src, out)
	require.ErrorContains(t, err, "not enough free space")
	require.Equal(t, dir, asked)
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr))

	restoreFreeBytes = func(string) (uint64, error) { return 1 << 40, nil }
	_, err = ExtractDB(src, out)
	require.NoError(t, err)
}
