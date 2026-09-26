package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goodZip builds a minimal valid backup zip around db, with extra entries.
func goodZip(db []byte, extra map[string][]byte) []byte {
	sum := sha256.Sum256(db)
	h := hex.EncodeToString(sum[:])
	mf := Manifest{Format: ManifestFormat, App: "kipple", DBSHA256: h, DBBytes: int64(len(db)),
		Files: []FileEntry{{Name: DBFile, Bytes: int64(len(db)), SHA256: h}}}
	for n, b := range extra {
		s := sha256.Sum256(b)
		mf.Files = append(mf.Files, FileEntry{Name: n, Bytes: int64(len(b)), SHA256: hex.EncodeToString(s[:])})
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(n string, b []byte) {
		w, _ := zw.Create(n)
		_, _ = w.Write(b)
	}
	mb, _ := json.Marshal(mf)
	put(ManifestFile, mb)
	put(DBFile, db)
	for n, b := range extra {
		put(n, b)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// checkExtract runs ExtractDB on data inside a fresh directory and asserts the
// invariants: it only ever creates the destination file, on error it leaves
// nothing behind, and on success the file is the size the manifest promised.
func checkExtract(t *testing.T, data []byte) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "in", "backup.zip")
	dstDir := filepath.Join(root, "out")
	if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dstDir, "kipple.db")
	mf, err := ExtractDB(src, dst)

	var found []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	for _, p := range found {
		if p != src && p != dst {
			t.Fatalf("unexpected file written: %s", p)
		}
	}
	st, statErr := os.Stat(dst)
	if err != nil {
		if statErr == nil {
			t.Fatalf("error %v but the destination exists", err)
		}
		return
	}
	if statErr != nil || st.Size() != mf.DBBytes {
		t.Fatalf("success but destination is wrong (stat %v, want %d bytes)", statErr, mf.DBBytes)
	}
}

// FuzzExtractDBBytes: arbitrary bytes as a backup zip.
func FuzzExtractDBBytes(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("PK\x05\x06" + string(make([]byte, 18))))
	f.Add(goodZip([]byte("SQLite format 3\x00 data"), nil))
	f.Add(goodZip([]byte("x"), map[string][]byte{"feeds.opml": []byte("<opml/>"), "../evil": []byte("z")}))
	f.Fuzz(func(t *testing.T, data []byte) { checkExtract(t, data) })
}

// FuzzExtractDBEntries: a well-formed zip whose entry names and contents come
// from the fuzzer (path traversal, absolute paths, duplicate names, a database
// entry that disagrees with its manifest).
func FuzzExtractDBEntries(f *testing.F) {
	f.Add("kipple.db", []byte("abc"), "../../evil", []byte("x"), int64(3))
	f.Add("kipple.db", []byte(""), "/abs/path", []byte(""), int64(0))
	f.Add("kipple.db", []byte("abc"), "kipple.db", []byte("abd"), int64(3))
	f.Add("kipple.db", []byte("abc"), "C:\\win\\x", []byte("y"), int64(99))
	f.Fuzz(func(t *testing.T, dbName string, db []byte, name string, content []byte, claimed int64) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		s := sha256.Sum256(db)
		h := hex.EncodeToString(s[:])
		c := sha256.Sum256(content)
		mf := Manifest{Format: ManifestFormat, App: "kipple", DBSHA256: h, DBBytes: int64(len(db)),
			Files: []FileEntry{{Name: dbName, Bytes: int64(len(db)), SHA256: h}, {Name: name, Bytes: claimed, SHA256: hex.EncodeToString(c[:])}}}
		mb, _ := json.Marshal(mf)
		for _, e := range []struct {
			n string
			b []byte
		}{{ManifestFile, mb}, {dbName, db}, {name, content}} {
			if w, err := zw.Create(e.n); err == nil {
				_, _ = w.Write(e.b)
			}
		}
		_ = zw.Close()
		checkExtract(t, buf.Bytes())
	})
}
