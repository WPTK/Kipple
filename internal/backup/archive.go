package backup

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Names inside the zip.
const (
	DBFile       = "kipple.db"
	OPMLFile     = "feeds.opml"
	SettingsFile = "settings.json"
	ReadmeFile   = "RESTORE.txt"
	ManifestFile = "manifest.json"

	// ManifestFormat is the manifest layout version.
	ManifestFormat = 1
)

// FileEntry is one file's size and checksum in the manifest.
type FileEntry struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Manifest is manifest.json. Files lists every file except the manifest itself.
type Manifest struct {
	Format        int         `json:"format"`
	App           string      `json:"app"`
	KippleVersion string      `json:"kipple_version"`
	SchemaVersion int         `json:"schema_version"`
	ApplicationID int         `json:"application_id"`
	CreatedAt     string      `json:"created_at"`
	DBSHA256      string      `json:"db_sha256"`
	DBBytes       int64       `json:"db_bytes"`
	Feeds         int64       `json:"feeds"`
	Items         int64       `json:"items"`
	Starred       int64       `json:"starred"`
	Files         []FileEntry `json:"files"`
}

const restoreText = `Kipple backup
=============

This zip holds a consistent copy of your Kipple database (kipple.db), your
subscriptions as OPML (feeds.opml), a readable copy of your settings
(settings.json) and manifest.json with a SHA-256 for every file.

kipple.db is the only file a restore uses. The rest is for humans: feeds.opml
imports into any feed reader.

To restore, stop Kipple, then run
    kipple restore <this file> --yes
(with Docker, from where you keep this file, with the kipple service stopped:
    docker compose run --rm -T --no-deps kipple restore - --yes < <this file>)
and start Kipple again. The current database is moved to backup/pre-restore-*
first. Every web session is signed out by a restore.

This file contains your password hashes and any feed logins. Keep it private.
`

const maxManifestBytes = 1 << 20

// readEntry reads a small entry fully, refusing one that claims to be larger
// than max.
func readEntry(f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("%s is unexpectedly large", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is unexpectedly large", f.Name)
	}
	return b, nil
}

// ExtractDB opens the backup zip at src, verifies the manifest (format, app,
// sizes and SHA-256 of every file) and writes kipple.db to dst (which must not
// exist). It returns the manifest. dst is removed on any error.
func ExtractDB(src, dst string) (mf Manifest, err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	zr, err := zip.OpenReader(src)
	if err != nil {
		return Manifest{}, fmt.Errorf("not a readable zip: %w", err)
	}
	defer zr.Close()
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		if _, dup := byName[f.Name]; dup {
			return Manifest{}, fmt.Errorf("the zip lists %s twice", f.Name)
		}
		byName[f.Name] = f
	}
	mfile, ok := byName[ManifestFile]
	if !ok {
		return Manifest{}, errors.New("this zip has no manifest.json: it is not a Kipple backup")
	}
	mb, err := readEntry(mfile, maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	if err := json.Unmarshal(mb, &mf); err != nil {
		return Manifest{}, fmt.Errorf("manifest.json is not valid: %w", err)
	}
	if mf.App != "kipple" || mf.Format != ManifestFormat {
		return Manifest{}, fmt.Errorf("unsupported backup (app %q, format %d)", mf.App, mf.Format)
	}
	var sawDB bool
	for _, e := range mf.Files {
		f, ok := byName[e.Name]
		if !ok {
			return Manifest{}, fmt.Errorf("the manifest lists %s but the zip does not contain it", e.Name)
		}
		if int64(f.UncompressedSize64) != e.Bytes {
			return Manifest{}, fmt.Errorf("%s has the wrong size (manifest %d, zip %d)", e.Name, e.Bytes, f.UncompressedSize64)
		}
		if e.Name == DBFile {
			sawDB = true
			if e.SHA256 != mf.DBSHA256 || e.Bytes != mf.DBBytes {
				return Manifest{}, errors.New("manifest.json disagrees with itself about kipple.db")
			}
			if err := extract(f, e, dst); err != nil {
				return Manifest{}, err
			}
			continue
		}
		b, err := readEntry(f, 64<<20)
		if err != nil {
			return Manifest{}, err
		}
		sum := sha256.Sum256(b)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), e.SHA256) {
			return Manifest{}, fmt.Errorf("checksum mismatch for %s: the backup is damaged", e.Name)
		}
	}
	if !sawDB {
		return Manifest{}, errors.New("the manifest does not list kipple.db")
	}
	return mf, nil
}

func extract(f *zip.File, e FileEntry, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, e.Bytes+1))
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", e.Name, err)
	}
	if n != e.Bytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), e.SHA256) {
		return fmt.Errorf("checksum mismatch for %s: the backup is damaged", e.Name)
	}
	return nil
}
