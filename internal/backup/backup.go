// Package backup builds the downloadable backup export and reads it back for
// `kipple restore` (docs/research/backend-additions-round2.md §6).
//
// An export is a zip of a consistent SQLite snapshot (VACUUM INTO on the
// snapshot pool: it holds only a read snapshot, so fetch commits and API writes
// carry on), the subscription list as OPML, the settings as JSON, a manifest
// with checksums, and a RESTORE.txt. It is built into <data>/backup/export/,
// handed out once through a short-lived single-use token, and deleted after the
// download, on expiry, on failure and at startup.
package backup

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

// Defaults for Options.
const (
	DefaultTTL          = 5 * time.Minute
	DefaultMaxDBBytes   = 4 << 30
	DefaultBuildTimeout = 10 * time.Minute

	// A backup needs the snapshot copy (about the database size) plus the zip
	// (at most that): 2.2x, plus a little slack for the small files.
	spaceNum, spaceDen = 22, 10
	spaceSlack         = 16 << 20

	dirName = "export"
)

// Errors Create and Take return; the API maps them to statuses.
var (
	// ErrBusy: a snapshot or another export is running. Retry shortly.
	ErrBusy = errors.New("backup: another snapshot or export is running")
	// ErrTooLarge: the database is over Options.MaxDBBytes.
	ErrTooLarge = errors.New("backup: database is larger than the export limit")
	// ErrBadToken: unknown, expired or already used (deliberately not told apart).
	ErrBadToken = errors.New("backup: download link expired or already used")
)

// NoSpaceError is the free-space refusal.
type NoSpaceError struct{ Need, Free int64 }

func (e *NoSpaceError) Error() string {
	return fmt.Sprintf("not enough free disk space for a backup: need about %s, %s free", human(e.Need), human(e.Free))
}

func human(n int64) string {
	const mb = 1 << 20
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", (n+mb-1)/mb)
}

// Warning is what the export dialog shows: what a backup file contains that is
// sensitive.
const Warning = "This file is a complete copy of your Kipple database. It contains your web and Reader API " +
	"password hashes, the account secret that signs Reader API tokens and image links, hashed session " +
	"identifiers, and any HTTP Basic logins (user:password) saved for feeds, which are stored as plain text. " +
	"Keep it private and store it only where nobody else can open it. Restoring it brings those logins back."

// Options configures New. Only DB is required.
type Options struct {
	DB      *store.DB
	Logger  *slog.Logger
	Version string // Kipple's version, recorded in the manifest
	// Dir holds the temporary export files. Default <backup dir>/export.
	Dir string
	// TTL is how long a finished export stays downloadable (default 5 min).
	TTL time.Duration
	// MaxDBBytes refuses a larger database (default 4 GiB).
	MaxDBBytes int64
	// BuildTimeout bounds one Create (default 10 min).
	BuildTimeout time.Duration
	// FreeBytes reports the free space on the volume of a directory
	// (tests inject; default the OS call).
	FreeBytes func(dir string) (uint64, error)
	// Now defaults to time.Now.
	Now func() time.Time
}

// Manager builds exports and hands them out once.
type Manager struct {
	o   Options
	log *slog.Logger

	mu  sync.Mutex
	cur *pending
}

type pending struct {
	token    string
	path     string
	filename string
	bytes    int64
	expires  time.Time
	timer    *time.Timer
}

// Export is a finished export awaiting its one download.
type Export struct {
	Token     string
	Filename  string
	Bytes     int64
	ExpiresAt time.Time
	Manifest  Manifest
}

// New returns a Manager and removes anything a crashed process left in the
// export directory.
func New(o Options) *Manager {
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.MaxDBBytes <= 0 {
		o.MaxDBBytes = DefaultMaxDBBytes
	}
	if o.BuildTimeout <= 0 {
		o.BuildTimeout = DefaultBuildTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FreeBytes == nil {
		o.FreeBytes = diskFree
	}
	if o.Dir == "" {
		o.Dir = filepath.Join(o.DB.BackupDir(), dirName)
	}
	m := &Manager{o: o, log: o.Logger}
	if m.log == nil {
		m.log = slog.Default()
	}
	m.cleanDir()
	return m
}

// BuildTimeout is how long one Create may take.
func (m *Manager) BuildTimeout() time.Duration { return m.o.BuildTimeout }

// TTL is how long an export stays downloadable.
func (m *Manager) TTL() time.Duration { return m.o.TTL }

// cleanDir empties the export directory (startup, and before a build).
func (m *Manager) cleanDir() {
	entries, err := os.ReadDir(m.o.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(m.o.Dir, e.Name()))
	}
}

// Close drops the pending export (and its file) and stops its expiry timer.
func (m *Manager) Close() {
	m.mu.Lock()
	m.discardLocked()
	m.mu.Unlock()
}

func (m *Manager) discardLocked() {
	if m.cur == nil {
		return
	}
	m.cur.timer.Stop()
	_ = os.Remove(m.cur.path)
	m.cur = nil
}

// Create builds one export. It refuses (ErrBusy) while another snapshot or
// export runs, when the volume lacks the space (*NoSpaceError), or when the
// database is over the size limit (ErrTooLarge). A previous unclaimed export is
// discarded first. The returned token is valid once, for Options.TTL.
func (m *Manager) Create(ctx context.Context) (Export, error) {
	release, err := m.o.DB.TrySnapshot()
	if err != nil {
		return Export{}, ErrBusy
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, m.o.BuildTimeout)
	defer cancel()
	began := time.Now()

	m.mu.Lock()
	m.discardLocked() // its file counts against the free space below
	m.mu.Unlock()

	size := m.o.DB.DiskSize()
	if size > m.o.MaxDBBytes {
		return Export{}, ErrTooLarge
	}
	if err := os.MkdirAll(m.o.Dir, 0o755); err != nil {
		return Export{}, fmt.Errorf("backup: export dir: %w", err)
	}
	m.cleanDir()
	need := size*spaceNum/spaceDen + spaceSlack
	freeU, err := m.o.FreeBytes(m.o.Dir)
	if err != nil {
		return Export{}, fmt.Errorf("backup: free space: %w", err)
	}
	free := int64(min(freeU, 1<<62))
	if free < need {
		return Export{}, &NoSpaceError{Need: need, Free: free}
	}

	now := m.o.Now()
	stamp := fmt.Sprintf("%d", now.UnixNano())
	snapPath := filepath.Join(m.o.Dir, "export-"+stamp+".db")
	partial := filepath.Join(m.o.Dir, "export-"+stamp+".zip.partial")
	final := filepath.Join(m.o.Dir, "export-"+stamp+".zip")
	defer func() {
		for _, p := range []string{snapPath, snapPath + "-wal", snapPath + "-shm", snapPath + "-journal", partial} {
			_ = os.Remove(p)
		}
	}()

	if err := m.o.DB.SnapshotTo(ctx, snapPath); err != nil {
		return Export{}, err
	}
	manifest, err := m.buildZip(ctx, snapPath, partial, now)
	if err != nil {
		return Export{}, err
	}
	if err := os.Rename(partial, final); err != nil {
		return Export{}, fmt.Errorf("backup: publish: %w", err)
	}
	st, err := os.Stat(final)
	if err != nil {
		_ = os.Remove(final)
		return Export{}, err
	}

	tok, err := newToken()
	if err != nil {
		_ = os.Remove(final)
		return Export{}, err
	}
	name := "kipple-backup-" + now.Format("20060102-150405") + ".zip"
	p := &pending{token: tok, path: final, filename: name, bytes: st.Size(), expires: now.Add(m.o.TTL)}
	m.mu.Lock()
	m.discardLocked()
	m.cur = p
	p.timer = time.AfterFunc(m.o.TTL, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.cur == p {
			m.discardLocked()
		}
	})
	m.mu.Unlock()

	m.log.Info("backup: export ready", "bytes", st.Size(), "db_bytes", manifest.DBBytes, "items", manifest.Items,
		"duration", time.Since(began).String())
	return Export{Token: tok, Filename: name, Bytes: st.Size(), ExpiresAt: p.expires, Manifest: manifest}, nil
}

func newToken() (string, error) {
	b := make([]byte, 16) // 128 bits
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Download is the single claim on an export.
type Download struct {
	File     *os.File
	Filename string
	Bytes    int64
	path     string
}

// Close closes and deletes the file: an export is downloaded once, and a
// failed transfer is retried with a new export.
func (d *Download) Close() error {
	err := d.File.Close()
	_ = os.Remove(d.path)
	return err
}

// Take claims the export for token, once: the token is spent whether or not the
// transfer completes. ErrBadToken for an unknown, expired or spent token.
func (m *Manager) Take(token string) (*Download, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.cur
	if p == nil || subtle.ConstantTimeCompare([]byte(p.token), []byte(token)) != 1 || !m.o.Now().Before(p.expires) {
		return nil, ErrBadToken
	}
	p.timer.Stop()
	m.cur = nil
	f, err := os.Open(p.path)
	if err != nil {
		_ = os.Remove(p.path)
		return nil, fmt.Errorf("backup: open export: %w", err)
	}
	return &Download{File: f, Filename: p.filename, Bytes: p.bytes, path: p.path}, nil
}

// settingsDoc is settings.json: a readable copy of the settings table; the
// database is authoritative.
type settingsDoc struct {
	Format   int                        `json:"format"`
	Note     string                     `json:"note"`
	Settings map[string]json.RawMessage `json:"settings"`
}

func readSettings(ctx context.Context, db *sql.DB) ([]byte, error) {
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM settings WHERE key NOT LIKE 'sys.%' ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	doc := settingsDoc{Format: 1, Note: "A readable copy. kipple.db is authoritative; restore uses only that.", Settings: map[string]json.RawMessage{}}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		if json.Valid([]byte(v)) {
			doc.Settings[k] = json.RawMessage(v)
		} else {
			b, _ := json.Marshal(v)
			doc.Settings[k] = b
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "  ")
}

// buildZip inspects the snapshot, reads the OPML and settings from it (so all
// four files describe the same instant) and writes the zip to out.
func (m *Manager) buildZip(ctx context.Context, snapPath, out string, now time.Time) (Manifest, error) {
	db, err := openFile(snapPath)
	if err != nil {
		return Manifest{}, err
	}
	info, err := inspect(ctx, db, false)
	if err != nil {
		db.Close()
		return Manifest{}, fmt.Errorf("backup: the snapshot failed its check: %w", err)
	}
	var opmlBuf strings.Builder
	if err := opml.ExportFrom(ctx, db, &opmlBuf); err != nil {
		db.Close()
		return Manifest{}, err
	}
	settings, err := readSettings(ctx, db)
	if err != nil {
		db.Close()
		return Manifest{}, fmt.Errorf("backup: read settings: %w", err)
	}
	// Close before hashing: nothing may touch the file afterwards.
	if err := db.Close(); err != nil {
		return Manifest{}, err
	}

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Manifest{}, err
	}
	failed := true
	defer func() {
		f.Close()
		if failed {
			_ = os.Remove(out)
		}
	}()
	zw := zip.NewWriter(f)
	mf := Manifest{
		Format: ManifestFormat, App: "kipple", KippleVersion: m.o.Version, SchemaVersion: info.SchemaVersion,
		ApplicationID: store.ApplicationID, CreatedAt: now.UTC().Format(time.RFC3339),
		Feeds: info.Feeds, Items: info.Items, Starred: info.Starred,
	}
	add := func(name string, src io.Reader) (FileEntry, error) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return FileEntry{}, err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), &ctxReader{ctx, src})
		return FileEntry{Name: name, Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
	}

	dbf, err := os.Open(snapPath)
	if err != nil {
		return Manifest{}, err
	}
	e, err := add(DBFile, dbf)
	dbf.Close()
	if err != nil {
		return Manifest{}, fmt.Errorf("backup: write %s: %w", DBFile, err)
	}
	mf.DBSHA256, mf.DBBytes = e.SHA256, e.Bytes
	mf.Files = append(mf.Files, e)
	for _, x := range []struct {
		name string
		body string
	}{{OPMLFile, opmlBuf.String()}, {SettingsFile, string(settings)}, {ReadmeFile, restoreText}} {
		e, err := add(x.name, strings.NewReader(x.body))
		if err != nil {
			return Manifest{}, fmt.Errorf("backup: write %s: %w", x.name, err)
		}
		mf.Files = append(mf.Files, e)
	}
	mb, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if _, err := add(ManifestFile, strings.NewReader(string(mb)+"\n")); err != nil {
		return Manifest{}, err
	}
	if err := zw.Close(); err != nil {
		return Manifest{}, err
	}
	if err := f.Sync(); err != nil {
		return Manifest{}, err
	}
	failed = false
	return mf, nil
}

// ctxReader stops a long copy when the build's context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
