package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

const maxManifestBytes = 1 << 20

// maxSmallEntry caps every entry but kipple.db: they are read into memory.
const maxSmallEntry = 64 << 20

// restoreFreeBytes reports the free space where ExtractDB writes (a variable so
// tests can fake a full disk). When it fails the check is skipped: the write
// itself still fails cleanly on a full disk and the partial file is removed.
var restoreFreeBytes = diskFree

// readEntry reads a small entry fully, refusing one that claims to be larger
// than max.
func readEntry(ctx context.Context, f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("%s is unexpectedly large", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(&ctxReader{ctx, rc}, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is unexpectedly large", f.Name)
	}
	return b, nil
}

// Extracted is what Extract verified: the manifest, and feeds.opml (nil when
// the backup lists none). The other small files are checked as they stream by
// and not kept.
type Extracted struct {
	Manifest  Manifest
	FeedsOPML []byte
}

// ExtractDB is Extract for `kipple restore`: no cancellation, the OS free space.
func ExtractDB(src, dst string) (Manifest, error) {
	x, err := Extract(context.Background(), src, dst, restoreFreeBytes)
	return x.Manifest, err
}

// Extract opens the backup zip at src, verifies the manifest (format, app,
// sizes and SHA-256 of every file) and writes kipple.db to dst (which must not
// exist). The zip's directory is sized from its end record before it is
// parsed, so a zip with thousands of entries costs nothing. A volume without
// room for the declared database is an *UploadSpaceError; a nil free skips that
// check. dst is removed on any error, and ctx stops the work.
func Extract(ctx context.Context, src, dst string, free func(string) (uint64, error)) (x Extracted, err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if err := checkZipDirectory(src); err != nil {
		return Extracted{}, err
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return Extracted{}, fmt.Errorf("not a readable zip: %w", err)
	}
	defer zr.Close()
	// checkZipDirectory read the count the end record claims; this is what was
	// actually parsed.
	if len(zr.File) > MaxEntries {
		return Extracted{}, tooManyEntries(uint64(len(zr.File)))
	}
	byName := map[string]*zip.File{}
	for _, f := range zr.File {
		// A Kipple backup is flat. Restore never writes an entry by its own name,
		// so a path is harmless here, but it means the zip was not made by Kipple
		// (or was tampered with): refuse it rather than restore from it.
		if !flatName(f.Name) {
			return Extracted{}, fmt.Errorf("the zip contains the entry %q, which is not a plain file name: it is not a Kipple backup", f.Name)
		}
		if _, dup := byName[f.Name]; dup {
			return Extracted{}, fmt.Errorf("the zip lists %s twice", f.Name)
		}
		if !slices.Contains(backupFiles, f.Name) {
			return Extracted{}, fmt.Errorf("the zip contains %s, which is not part of a Kipple backup", f.Name)
		}
		byName[f.Name] = f
	}
	mfile, ok := byName[ManifestFile]
	if !ok {
		return Extracted{}, errors.New("this zip has no manifest.json: it is not a Kipple backup")
	}
	mb, err := readEntry(ctx, mfile, maxManifestBytes)
	if err != nil {
		return Extracted{}, err
	}
	mf := &x.Manifest
	if err := json.Unmarshal(mb, mf); err != nil {
		return Extracted{}, fmt.Errorf("manifest.json is not valid: %w", err)
	}
	if mf.App != "kipple" || mf.Format != ManifestFormat {
		return Extracted{}, fmt.Errorf("unsupported backup (app %q, format %d)", mf.App, mf.Format)
	}
	// Refused before anything is extracted. Inspect checks the database itself
	// again, so a manifest that understates this changes nothing.
	if mf.SchemaVersion > store.LatestVersion() {
		return Extracted{}, &NewerError{KippleVersion: mf.KippleVersion}
	}
	// The declared size is checked (and later matched against the zip entry and
	// the bytes actually written) before anything is extracted: a crafted or
	// damaged zip must not fill the disk under the live database.
	if mf.DBBytes < 0 || mf.DBBytes > DefaultMaxDBBytes {
		return Extracted{}, fmt.Errorf("kipple.db is declared as %d bytes, over the %d a restore accepts", mf.DBBytes, int64(DefaultMaxDBBytes))
	}
	if free != nil {
		if f, ferr := free(filepath.Dir(dst)); ferr == nil && uint64(mf.DBBytes) > f {
			return Extracted{}, &UploadSpaceError{Need: mf.DBBytes, Free: int64(min(f, 1<<62))}
		}
	}
	var sawDB bool
	listed := map[string]bool{}
	for _, e := range mf.Files {
		if listed[e.Name] || e.Name == ManifestFile {
			return Extracted{}, fmt.Errorf("manifest.json lists %s twice", e.Name)
		}
		listed[e.Name] = true
		f, ok := byName[e.Name]
		if !ok {
			return Extracted{}, fmt.Errorf("the manifest lists %s but the zip does not contain it", e.Name)
		}
		if int64(f.UncompressedSize64) != e.Bytes {
			return Extracted{}, fmt.Errorf("%s has the wrong size (manifest %d, zip %d)", e.Name, e.Bytes, f.UncompressedSize64)
		}
		if e.Name == DBFile {
			sawDB = true
			if e.SHA256 != mf.DBSHA256 || e.Bytes != mf.DBBytes {
				return Extracted{}, errors.New("manifest.json disagrees with itself about kipple.db")
			}
			if err := extract(ctx, f, e, dst); err != nil {
				return Extracted{}, err
			}
			continue
		}
		if e.Name == OPMLFile {
			b, err := readEntry(ctx, f, maxSmallEntry)
			if err != nil {
				return Extracted{}, err
			}
			if err := checkSum(e, bytes.NewReader(b)); err != nil {
				return Extracted{}, err
			}
			x.FeedsOPML = b
			continue
		}
		if err := checkEntry(ctx, f, e); err != nil {
			return Extracted{}, err
		}
	}
	if !sawDB {
		return Extracted{}, errors.New("the manifest does not list kipple.db")
	}
	return x, nil
}

// checkEntry streams a small entry through its checksum without keeping it.
func checkEntry(ctx context.Context, f *zip.File, e FileEntry) error {
	if e.Bytes > maxSmallEntry {
		return fmt.Errorf("%s is unexpectedly large", e.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return checkSum(e, &ctxReader{ctx, rc})
}

// checkSum reads r (at most e.Bytes+1 bytes) and compares its size and SHA-256
// with the manifest's.
func checkSum(e FileEntry, r io.Reader) error {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, e.Bytes+1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", e.Name, err)
	}
	if n != e.Bytes || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), e.SHA256) {
		return fmt.Errorf("checksum mismatch for %s: the backup is damaged", e.Name)
	}
	return nil
}

func tooManyEntries(n uint64) error {
	return fmt.Errorf("the zip holds %d files; a Kipple backup holds %d: it is not a Kipple backup", n, len(backupFiles))
}

// The zip end records (APPNOTE 4.3.14 to 4.3.16).
const (
	eocdSig      = 0x06054b50
	eocdLen      = 22
	zip64LocSig  = 0x07064b50
	zip64LocLen  = 20
	zip64EOCDSig = 0x06064b50
	zip64EOCDLen = 56
	// maxZipDirectory caps the central directory: a real backup's is a few
	// hundred bytes.
	maxZipDirectory = 64 << 10
)

// checkZipDirectory sizes the zip's central directory from its end record
// before archive/zip parses it, and pins it down: at most MaxEntries entries,
// at most maxZipDirectory bytes, and ending exactly where the end record (or
// the zip64 end record) begins, with nothing before the first entry. That last
// rule is what makes the count trustworthy: archive/zip reads directory
// headers from the directory's offset until one fails, whatever the count
// says, so a directory placed elsewhere could hold any number of them. The
// record is found as archive/zip finds it (the last signature whose comment
// fits), and the zip64 record is used under archive/zip's own rule: when the
// count, the size or the offset is saturated. Unsaturated, the directory may
// also end at a zip64 record that precedes the end record (a writer adds one
// when an entry needed zip64 fields, as a 4 GiB database does).
func checkZipDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	tail := min(size, eocdLen+0xffff)
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	at := -1
	for i := len(buf) - eocdLen; i >= 0; i-- {
		if binary.LittleEndian.Uint32(buf[i:]) == eocdSig {
			if n := int(binary.LittleEndian.Uint16(buf[i+20:])); i+eocdLen+n <= len(buf) {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return errors.New("not a readable zip: no end of central directory")
	}
	rec := buf[at:]
	eocdAt := uint64(size - tail + int64(at))
	count := uint64(binary.LittleEndian.Uint16(rec[10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(rec[12:]))
	dirOff := uint64(binary.LittleEndian.Uint32(rec[16:]))
	// A zip64 end record, located as archive/zip locates it: a locator just
	// before the end record, on disk 0 of 1. A writer adds one whenever an
	// entry needed zip64 fields, even with the plain record unsaturated.
	z64 := int64(-1)
	if eocdAt >= zip64LocLen {
		loc := make([]byte, zip64LocLen)
		if _, err := f.ReadAt(loc, int64(eocdAt-zip64LocLen)); err == nil && binary.LittleEndian.Uint32(loc) == zip64LocSig &&
			binary.LittleEndian.Uint32(loc[4:]) == 0 && binary.LittleEndian.Uint32(loc[16:]) == 1 {
			if off := binary.LittleEndian.Uint64(loc[8:]); off <= uint64(size-zip64EOCDLen) {
				z64 = int64(off)
			}
		}
	}
	ends := []uint64{eocdAt} // where the directory may end
	if z64 >= 0 {
		ends = append(ends, uint64(z64))
	}
	if count == 0xffff || dirSize == 0xffffffff || dirOff == 0xffffffff {
		// archive/zip's own rule for reading the zip64 record.
		bad := errors.New("not a readable zip: bad zip64 end record")
		if z64 < 0 {
			return bad
		}
		rec64 := make([]byte, zip64EOCDLen)
		if _, err := f.ReadAt(rec64, z64); err != nil || binary.LittleEndian.Uint32(rec64) != zip64EOCDSig {
			return bad
		}
		count = binary.LittleEndian.Uint64(rec64[32:])
		dirSize = binary.LittleEndian.Uint64(rec64[40:])
		dirOff = binary.LittleEndian.Uint64(rec64[48:])
		ends = []uint64{uint64(z64)}
	}
	if count > MaxEntries {
		return tooManyEntries(count)
	}
	if dirSize > maxZipDirectory {
		return fmt.Errorf("the zip's directory is %d bytes, far more than a Kipple backup's: it is not a Kipple backup", dirSize)
	}
	if dirOff > math.MaxInt64-dirSize || !slices.Contains(ends, dirOff+dirSize) {
		return errors.New("the zip's directory is not where its end record says: it is not a Kipple backup")
	}
	return nil
}

// flatName reports whether a zip entry name is a plain file name: not empty,
// no directory part (either slash), not "." or "..", no drive letter.
func flatName(n string) bool {
	return n != "" && n != "." && n != ".." && !strings.ContainsAny(n, `/\:`)
}

func extract(ctx context.Context, f *zip.File, e FileEntry, dst string) error {
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
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(&ctxReader{ctx, rc}, e.Bytes+1))
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
