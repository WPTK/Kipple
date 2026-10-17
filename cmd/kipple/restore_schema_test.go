package main

import (
	"archive/zip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
)

// withSchemaChange returns the database inside zipPath with change applied, as a bare file, and a
// backup zip around it whose manifest checksums match (a self-consistent zip, not a damaged one).
func withSchemaChange(t *testing.T, zipPath, change string) (bare, zipped string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer zr.Close()
	ents := map[string][]byte{}
	var order []string
	for _, e := range zr.File {
		rc, err := e.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		ents[e.Name] = b
		order = append(order, e.Name)
	}

	out := t.TempDir()
	bare = filepath.Join(out, "crafted.db")
	require.NoError(t, os.WriteFile(bare, ents[backup.DBFile], 0o600))
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(bare)+"?_pragma=journal_mode(DELETE)")
	require.NoError(t, err)
	_, err = raw.Exec(change)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	db, err := os.ReadFile(bare)
	require.NoError(t, err)

	sum := sha256.Sum256(db)
	h := hex.EncodeToString(sum[:])
	var mf backup.Manifest
	require.NoError(t, json.Unmarshal(ents[backup.ManifestFile], &mf))
	mf.DBSHA256, mf.DBBytes = h, int64(len(db))
	for i := range mf.Files {
		if mf.Files[i].Name == backup.DBFile {
			mf.Files[i].SHA256, mf.Files[i].Bytes = h, int64(len(db))
		}
	}
	ents[backup.DBFile] = db
	ents[backup.ManifestFile], err = json.Marshal(mf)
	require.NoError(t, err)

	zipped = filepath.Join(out, "crafted.zip")
	f, err := os.Create(zipped)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	for _, name := range order {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(ents[name])
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return bare, zipped
}

// The command checks a backup's schema the way the web restore does: a database holding anything a
// fresh Kipple database of its version does not is refused, from a zip or a bare file, before
// anything runs on it, and nothing in the data directory changes.
func TestRestoreRefusesASchemaKippleNeverCreates(t *testing.T) {
	for name, change := range map[string]string{
		// Would fire on the sessions the restore signs out.
		"trigger": "CREATE TRIGGER extra AFTER DELETE ON sessions BEGIN DELETE FROM items; END",
		"view":    "CREATE VIEW extra AS SELECT 1",
		"table":   "CREATE TABLE extra (a)",
		"changed trigger": "DROP TRIGGER folders_keep_default; " +
			"CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders BEGIN DELETE FROM items; END",
	} {
		t.Run(name, func(t *testing.T) {
			dir := newData(t, 5)
			bare, zipped := withSchemaChange(t, export(t, dir), change)
			for _, src := range []string{zipped, bare} {
				_, err := doRestore(dir, src, true)
				require.Error(t, err, src)
				require.Regexp(t, `never creates|has a changed`, err.Error())
				require.Equal(t, 5, countItems(t, dir), "the live database is untouched")
				require.Equal(t, 1, countSessions(t, dir), "its sessions too")
				require.NoFileExists(t, filepath.Join(dir, "restore-tmp.db"))
				require.NoDirExists(t, filepath.Join(dir, "backup", "pre-restore-20260925-120000Z"))
			}
			// The crafted file itself is never written to (the command works on a copy).
			require.Equal(t, 5, scalarFile(t, bare), "the crafted copy still holds its items")
		})
	}
}

// scalarFile counts the items in a database file without changing it.
func scalarFile(t *testing.T, path string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=trusted_schema(0)")
	require.NoError(t, err)
	defer raw.Close()
	var n int
	require.NoError(t, raw.QueryRow("SELECT count(*) FROM items").Scan(&n))
	return n
}
