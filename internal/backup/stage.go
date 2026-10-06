package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

// A restore from the setup wizard (docs/design.md §2.6) in three steps: an
// upload is verified and its database staged in the data directory; a confirm
// edits the staged copy and writes the marker, and the process exits; the next
// start applies it (ApplyStaged) before the database opens. Only a confirmed
// restore (the marker) is ever applied, so a forgotten upload never surprises
// anyone later: it expires, and the next start deletes it.
const (
	// UploadFile is where an upload (and `kipple restore -`) spools the file.
	UploadFile = "restore-upload.tmp"
	// StagedFile is the verified database of an upload.
	StagedFile = "restore-staged.db"
	// MarkerFile confirms StagedFile; it is written last.
	MarkerFile = "restore-pending.json"

	// DefaultUploadTTL is how long an unconfirmed upload is kept.
	DefaultUploadTTL = time.Hour
	// MaxUploadBytes caps an upload: the largest database a restore accepts,
	// stored, plus room for the zip's own records and small files.
	MaxUploadBytes = DefaultMaxDBBytes + 64<<20

	// An upload needs room for the zip, the database extracted from it and,
	// for a backup of an older Kipple, the safety copy and the upgrade on the
	// next start: about four times the zip, checked before the first byte is
	// stored.
	uploadSpaceFactor = 4
	uploadSpaceSlack  = 16 << 20
	// What the upgrade of an older backup needs at the next start, on top of
	// the staged database itself (store's own check, sized for a migration
	// that rebuilds a table): the safety copy and the rebuild.
	upgradeSpaceNum, upgradeSpaceDen = 31, 10
	upgradeSpaceSlack                = 64 << 20

	maxFeedsOPML = opml.MaxFileBytes
	sniffBytes   = 64 << 10
)

// Restore states, as GET /api/instance reports them.
const (
	RestoreNone      = "none"
	RestoreUploaded  = "uploaded"
	RestoreConfirmed = "confirmed"
)

// Upload kinds.
const (
	KindBackup = "backup"
	KindOPML   = "opml"
)

// Refusal is a restore refusal whose text is written for the person restoring.
type Refusal struct{ Msg string }

func (e *Refusal) Error() string { return e.Msg }

// Refusals of the Restorer; the API maps each to a status.
var (
	ErrRestoreBusy    = &Refusal{"Another restore upload is in progress. Cancel it first, then try again."}
	ErrRestorePending = &Refusal{"A restore is waiting to be applied: Kipple is restarting to finish it."}
	ErrNoUpload       = &Refusal{"No backup has been uploaded, or it expired. Upload it again."}
	ErrNotBackup      = &Refusal{"This is not a Kipple backup zip or an OPML file."}
	ErrUploadTooLarge = &Refusal{"This file is larger than the " + human(MaxUploadBytes) + " a restore accepts."}
	ErrOPMLTooLarge   = &Refusal{"This OPML file is larger than the " + human(opml.MaxFileBytes) + " an import accepts."}
	ErrUploadCut      = &Refusal{"The upload stopped before the whole file arrived. Try again."}
)

// BadUploadError is a zip that is damaged, tampered with or not made by Kipple.
type BadUploadError struct{ Err error }

func (e *BadUploadError) Error() string {
	return "This backup cannot be restored: " + e.Err.Error() + "."
}

func (e *BadUploadError) Unwrap() error { return e.Err }

// UploadSpaceError refuses an upload the volume has no room for.
type UploadSpaceError struct{ Need, Free int64 }

func (e *UploadSpaceError) Error() string {
	return fmt.Sprintf("Not enough free disk space to restore this backup: it needs about %s and %s is free. Free some space and try again.",
		human(e.Need), human(e.Free))
}

// BackupAccount is the account row of an uploaded backup.
type BackupAccount struct {
	Username    string
	HasPassword bool // a web password is set
	Open        bool // open mode: no password at all
}

// Upload is what an upload found.
type Upload struct {
	Kind string // KindBackup or KindOPML
	// Feeds is the subscription count, for both kinds.
	Feeds int64
	// The rest describes a backup.
	Manifest        Manifest
	Info            DBInfo
	Account         BackupAccount
	EstimateSeconds int
}

// RestorerOptions configures NewRestorer. DataDir is required.
type RestorerOptions struct {
	DataDir string
	// TTL is how long an unconfirmed upload is kept (default DefaultUploadTTL).
	TTL time.Duration
	// MaxBytes caps an upload (default MaxUploadBytes).
	MaxBytes int64
	// FreeBytes reports the free space on the volume of a directory (tests
	// inject; default the OS call).
	FreeBytes func(dir string) (uint64, error)
	Logger    *slog.Logger
}

// Restorer holds the one restore intent of a process in setup mode: none, an
// upload (verified and staged, unconfirmed) or a confirmed restore. One upload
// runs at a time.
type Restorer struct {
	o   RestorerOptions
	log *slog.Logger

	mu        sync.Mutex
	state     string
	uploading *inflight
	cur       *Upload
	feeds     []byte // the backup's feeds.opml
	timer     *time.Timer
	gen       int // bumped by each upload and discard, so a stale expiry does nothing
}

type inflight struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRestorer returns a Restorer with no upload. Leftover files of an earlier
// process are ApplyStaged's to handle, at start.
func NewRestorer(o RestorerOptions) *Restorer {
	if o.TTL <= 0 {
		o.TTL = DefaultUploadTTL
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxUploadBytes
	}
	if o.FreeBytes == nil {
		o.FreeBytes = diskFree
	}
	r := &Restorer{o: o, log: o.Logger, state: RestoreNone}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r
}

// State is RestoreNone, RestoreUploaded or RestoreConfirmed. An upload still
// arriving is RestoreNone.
func (r *Restorer) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *Restorer) path(name string) string { return filepath.Join(r.o.DataDir, name) }

// Upload reads one file of size bytes from body, which must deliver exactly
// that many. A Kipple backup zip is verified (manifest and checksums, the
// schema against a fresh database of its version, the integrity checks) and
// its database staged; the zip itself is deleted at once. An OPML file is only
// counted: nothing of it is kept. Anything else is ErrNotBackup.
func (r *Restorer) Upload(ctx context.Context, body io.Reader, size int64) (Upload, error) {
	r.mu.Lock()
	switch {
	case r.state == RestoreConfirmed:
		r.mu.Unlock()
		return Upload{}, ErrRestorePending
	case r.uploading != nil || r.state == RestoreUploaded:
		r.mu.Unlock()
		return Upload{}, ErrRestoreBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	fl := &inflight{cancel: cancel, done: make(chan struct{})}
	r.uploading = fl
	r.mu.Unlock()

	up, feeds, err := r.receive(ctx, body, size)

	r.mu.Lock()
	defer func() {
		r.uploading = nil
		r.mu.Unlock()
		cancel()
		close(fl.done)
	}()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err() // cancelled (Cancel or Discard) as it finished
	}
	if err != nil || up.Kind != KindBackup {
		removeStaged(r.o.DataDir)
		return up, err
	}
	r.gen++
	gen := r.gen
	r.state, r.cur, r.feeds = RestoreUploaded, &up, feeds
	r.timer = time.AfterFunc(r.o.TTL, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.gen == gen && r.state == RestoreUploaded {
			r.log.Info("restore: an unconfirmed upload expired and was deleted")
			r.discardLocked()
		}
	})
	r.log.Info("restore: backup uploaded", "kipple_version", up.Manifest.KippleVersion, "created_at", up.Manifest.CreatedAt,
		"feeds", up.Info.Feeds, "items", up.Info.Items, "db_bytes", up.Manifest.DBBytes)
	return up, nil
}

// receive spools, sniffs and checks one upload. It leaves at most the staged
// database behind (on success, for a backup).
func (r *Restorer) receive(ctx context.Context, body io.Reader, size int64) (Upload, []byte, error) {
	if size > r.o.MaxBytes {
		return Upload{}, nil, ErrUploadTooLarge
	}
	if size <= 0 {
		return Upload{}, nil, ErrNotBackup
	}
	spool := r.path(UploadFile)
	removeStaged(r.o.DataDir) // a leftover of an upload this process lost track of
	defer os.Remove(spool)
	if err := r.needSpace(size*uploadSpaceFactor + uploadSpaceSlack); err != nil {
		return Upload{}, nil, err
	}
	f, err := os.OpenFile(spool, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Upload{}, nil, fmt.Errorf("restore: spool: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(&ctxReader{ctx, body}, size))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if ctx.Err() != nil {
		return Upload{}, nil, ctx.Err()
	}
	if err != nil || n != size {
		return Upload{}, nil, ErrUploadCut
	}

	head, err := readHead(spool)
	if err != nil {
		return Upload{}, nil, err
	}
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return r.receiveBackup(ctx, spool)
	case opml.LooksLikeOPML(head):
		if size > maxFeedsOPML {
			return Upload{}, nil, ErrOPMLTooLarge
		}
		b, err := os.ReadFile(spool)
		if err != nil {
			return Upload{}, nil, err
		}
		doc, err := opml.Parse(bytes.NewReader(b))
		if err != nil {
			return Upload{}, nil, ErrNotBackup
		}
		return Upload{Kind: KindOPML, Feeds: int64(len(doc.Feeds))}, nil, nil
	}
	return Upload{}, nil, ErrNotBackup
}

func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return head[:n], nil
}

func (r *Restorer) receiveBackup(ctx context.Context, spool string) (Upload, []byte, error) {
	staged := r.path(StagedFile)
	mf, err := ExtractDB(spool, staged)
	if err != nil {
		var newer *NewerError
		if errors.As(err, &newer) {
			return Upload{}, nil, err
		}
		return Upload{}, nil, &BadUploadError{err}
	}
	feeds, err := readVerified(spool, mf, OPMLFile, maxFeedsOPML)
	if err != nil {
		return Upload{}, nil, &BadUploadError{err}
	}
	_ = os.Remove(spool) // the zip is not needed any more: free its space now

	info, acct, err := checkStaged(ctx, staged, mf.KippleVersion)
	if err != nil {
		return Upload{}, nil, err
	}
	if info.SchemaVersion < store.LatestVersion() {
		if err := r.needSpace(mf.DBBytes*upgradeSpaceNum/upgradeSpaceDen + upgradeSpaceSlack); err != nil {
			return Upload{}, nil, err
		}
	}
	return Upload{
		Kind: KindBackup, Feeds: info.Feeds, Manifest: mf, Info: info, Account: acct,
		EstimateSeconds: EstimateSeconds(mf.DBBytes, info.SchemaVersion < store.LatestVersion()),
	}, feeds, nil
}

// needSpace refuses when the data directory's volume has less than need free.
// When free space cannot be read the check is skipped: the writes still fail
// cleanly on a full disk.
func (r *Restorer) needSpace(need int64) error {
	freeU, err := r.o.FreeBytes(r.o.DataDir)
	if err != nil {
		return nil
	}
	free := int64(min(freeU, 1<<62))
	if free < need {
		return &UploadSpaceError{Need: need, Free: free}
	}
	return nil
}

// readVerified reads one small entry of the backup zip and checks it against
// the manifest.
func readVerified(src string, mf Manifest, name string, max int64) ([]byte, error) {
	var want *FileEntry
	for i := range mf.Files {
		if mf.Files[i].Name == name {
			want = &mf.Files[i]
		}
	}
	if want == nil {
		return nil, fmt.Errorf("the manifest does not list %s", name)
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		b, err := readEntry(f, max)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), want.SHA256) {
			return nil, fmt.Errorf("checksum mismatch for %s: the backup is damaged", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("the zip does not contain %s", name)
}

// openUntrusted opens a database that came from outside: no -wal or -shm
// beside it, and trusted_schema off, so a trigger or view in it cannot call a
// function with side effects.
func openUntrusted(path string) (*sql.DB, error) {
	return openDSN(path, url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(DELETE)", "trusted_schema(0)"}})
}

// checkStaged verifies an extracted database: a Kipple database of a version
// this binary knows, holding nothing a fresh database of that version does not
// (CheckSchema), passing the integrity checks, with an account row.
func checkStaged(ctx context.Context, path, kippleVersion string) (DBInfo, BackupAccount, error) {
	db, err := openUntrusted(path)
	if err != nil {
		return DBInfo{}, BackupAccount{}, err
	}
	defer db.Close()
	bad := func(err error) (DBInfo, BackupAccount, error) {
		var newer *NewerError
		if errors.As(err, &newer) {
			newer.KippleVersion = kippleVersion
			return DBInfo{}, BackupAccount{}, newer
		}
		return DBInfo{}, BackupAccount{}, &BadUploadError{err}
	}
	var appID, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return bad(fmt.Errorf("kipple.db is not a readable database: %w", err))
	}
	if appID != store.ApplicationID {
		return bad(errors.New("kipple.db is not a Kipple database"))
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return bad(err)
	}
	switch {
	case version > store.LatestVersion():
		return bad(&NewerError{})
	case version < 1:
		return bad(errors.New("kipple.db is empty"))
	}
	// Before anything else runs on it: the integrity checks below write to the
	// search index.
	if err := CheckSchema(ctx, db, version); err != nil {
		return bad(err)
	}
	info, err := inspect(ctx, db, true)
	if err != nil {
		return bad(err)
	}
	acct, err := readAccount(ctx, db)
	if err != nil {
		return bad(err)
	}
	return info, acct, nil
}

// CheckSchema compares the schema of db (at version) with a fresh database of
// the same version and refuses any table, index, trigger or view the fresh one
// does not have, and any trigger or view whose definition differs. Tables and
// indexes are matched by name: their text changes with how a migration was
// worded, while code that runs by itself lives only in triggers and views.
// SQLite's own objects (sqlite_*) are left alone.
func CheckSchema(ctx context.Context, db *sql.DB, version int) error {
	want, err := store.SchemaAt(ctx, version)
	if err != nil {
		return err
	}
	have, err := store.ReadSchema(ctx, db)
	if err != nil {
		return err
	}
	ref := map[string]store.SchemaObject{}
	for _, o := range want {
		ref[o.Type+"\x00"+o.Name] = o
	}
	for _, o := range have {
		if strings.HasPrefix(o.Name, "sqlite_") {
			continue
		}
		w, ok := ref[o.Type+"\x00"+o.Name]
		switch {
		case !ok:
			return fmt.Errorf("kipple.db holds the %s %q, which Kipple never creates", o.Type, o.Name)
		case !strings.EqualFold(w.Table, o.Table):
			return fmt.Errorf("kipple.db has the %s %q on the wrong table", o.Type, o.Name)
		case (o.Type == "trigger" || o.Type == "view") && squash(o.SQL) != squash(w.SQL):
			return fmt.Errorf("kipple.db has a changed %s %q", o.Type, o.Name)
		}
	}
	return nil
}

// squash collapses runs of white space, so a definition compares by its tokens.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func readAccount(ctx context.Context, db *sql.DB) (BackupAccount, error) {
	var modes int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('account') WHERE name = 'auth_mode'").Scan(&modes); err != nil {
		return BackupAccount{}, err
	}
	mode := "'" + store.AuthStandard + "'" // before account modes existed every account was standard
	if modes > 0 {
		mode = "auth_mode"
	}
	var a BackupAccount
	var hash, m string
	err := db.QueryRowContext(ctx, "SELECT username, password_hash, "+mode+" FROM account WHERE id = 1").Scan(&a.Username, &hash, &m)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupAccount{}, errors.New("it holds no account")
	}
	if err != nil {
		return BackupAccount{}, err
	}
	a.HasPassword = hash != ""
	a.Open = m == store.AuthOpen
	return a, nil
}

// EstimateSeconds is how long the restart that applies a restore of dbBytes
// should take, from the measured times in docs/performance.md: about 16 s per
// GB to restore and, for a backup of an older Kipple, about 7.5 s per GB for
// the safety copy and the upgrade. Rounded up, at least 30 s.
func EstimateSeconds(dbBytes int64, older bool) int {
	perGB := 16.0
	if older {
		perGB += 7.5
	}
	s := int(math.Ceil(float64(dbBytes) / (1 << 30) * perGB))
	return max(s, 30)
}

// Feeds hands out the uploaded backup's feeds.opml (the "feeds only" choice)
// and discards the upload: the rest of it will not be used.
func (r *Restorer) Feeds() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == RestoreConfirmed {
		return nil, ErrRestorePending
	}
	if r.state != RestoreUploaded {
		return nil, ErrNoUpload
	}
	b := r.feeds
	r.discardLocked()
	r.log.Info("restore: the feeds of the uploaded backup were taken; the upload was deleted")
	return b, nil
}

// Uploaded is the unconfirmed upload, if there is one, and its ticket for
// Confirm.
func (r *Restorer) Uploaded() (up Upload, ticket int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != RestoreUploaded {
		return Upload{}, 0, false
	}
	return *r.cur, r.gen, true
}

// Cancel removes an unconfirmed upload, or stops one still arriving and waits
// briefly for it to clean up. Nothing to cancel is fine. A confirmed restore
// cannot be cancelled (ErrRestorePending).
func (r *Restorer) Cancel() error {
	fl, err := r.drop()
	if fl != nil {
		select {
		case <-fl.done:
		case <-time.After(10 * time.Second):
		}
	}
	return err
}

// Drop is Cancel without the wait: an account was created, so no restore can
// follow.
func (r *Restorer) Drop() { _, _ = r.drop() }

func (r *Restorer) drop() (*inflight, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == RestoreConfirmed {
		return nil, ErrRestorePending
	}
	if r.state == RestoreUploaded {
		r.discardLocked()
	}
	if r.uploading != nil {
		r.uploading.cancel()
	}
	return r.uploading, nil
}

// Close stops the expiry timer and any upload still arriving. The files stay:
// the next start applies a confirmed restore and deletes anything else.
func (r *Restorer) Close() {
	r.mu.Lock()
	if r.timer != nil {
		r.timer.Stop()
	}
	fl := r.uploading
	r.mu.Unlock()
	if fl != nil {
		fl.cancel()
		<-fl.done
	}
}

func (r *Restorer) discardLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.gen++
	r.state, r.cur, r.feeds = RestoreNone, nil, nil
	removeStaged(r.o.DataDir)
}

// Confirm prepares the staged database of the upload and writes the marker that
// makes the next start apply it. On the staged copy only: every web session is
// signed out; the address settings (public URL, allowed host names and trusted
// proxies, which describe the old server, not the data) are cleared, so this server's own
// apply; and when passwordHash is not empty it becomes the web password, in
// standard sign-in. The caller must hold setup mode (no account row) for the
// whole call. ticket is Uploaded's: the upload that was looked at must be the
// one confirmed. ErrNoUpload without that upload, ErrRestorePending when
// already confirmed.
func (r *Restorer) Confirm(ctx context.Context, ticket int, passwordHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.state == RestoreConfirmed:
		return ErrRestorePending
	case r.state != RestoreUploaded || r.gen != ticket:
		return ErrNoUpload
	}
	staged := r.path(StagedFile)
	if err := prepareStaged(ctx, staged, passwordHash); err != nil {
		return err
	}
	mf := r.cur.Manifest
	if err := writeMarker(r.path(MarkerFile), marker{KippleVersion: mf.KippleVersion, CreatedAt: mf.CreatedAt, Username: r.cur.Account.Username}); err != nil {
		return err
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.gen++
	r.state, r.feeds = RestoreConfirmed, nil
	r.log.Info("restore: confirmed; it is applied when Kipple starts again", "username", r.cur.Account.Username)
	return nil
}

// HostSettings are the settings a restore clears: they describe the server a
// backup was made on, not the library (the trusted proxies also decide who may
// claim to be which client, so a stale list must not carry over).
var HostSettings = []string{store.SettingPublicURL, store.SettingAllowedHosts, store.SettingTrustedProxies}

func prepareStaged(ctx context.Context, path, passwordHash string) error {
	db, err := openUntrusted(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return fmt.Errorf("restore: sign out sessions: %w", err)
	}
	for _, k := range HostSettings {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", k); err != nil {
			return fmt.Errorf("restore: clear %s: %w", k, err)
		}
	}
	if passwordHash != "" {
		var modes int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('account') WHERE name = 'auth_mode'").Scan(&modes); err != nil {
			return err
		}
		q := "UPDATE account SET password_hash = ?, updated_at = unixepoch() WHERE id = 1"
		args := []any{passwordHash}
		if modes > 0 {
			q = "UPDATE account SET password_hash = ?, auth_mode = ?, updated_at = unixepoch() WHERE id = 1"
			args = append(args, store.AuthStandard)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("restore: set the new password: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("restore: the backup holds no account")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.Close()
}

// marker is MarkerFile: what was confirmed, for the log of the start that
// applies it. Its existence is the confirmation.
type marker struct {
	KippleVersion string `json:"kipple_version"`
	CreatedAt     string `json:"created_at"`
	Username      string `json:"username"`
}

// writeMarker writes path atomically (a temporary file, synced, renamed) so a
// crash leaves either no marker or a whole one.
func writeMarker(path string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("restore: marker: %w", err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("restore: marker: %w", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes renames in dir durable where the OS allows it (not on Windows).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// removeStaged deletes the files of an upload: the spool, the staged database
// and anything SQLite left beside it.
func removeStaged(dataDir string) {
	staged := filepath.Join(dataDir, StagedFile)
	for _, p := range []string{filepath.Join(dataDir, UploadFile), staged, staged + "-journal", staged + "-wal", staged + "-shm", filepath.Join(dataDir, MarkerFile+".tmp")} {
		_ = os.Remove(p)
	}
}

// Applied is what ApplyStaged did.
type Applied struct {
	// Restored: a confirmed restore was installed now.
	Restored bool
	// Pre is the directory the replaced database went to ("" when there was none).
	Pre string
	// KippleVersion, CreatedAt and Username describe the backup, from the marker.
	KippleVersion, CreatedAt, Username string
}

// ApplyStaged finishes a restore confirmed in the setup wizard. Run it at start
// under the data lock, before the database is opened. It is idempotent, so a
// crash at any point is finished or cleaned up by the next start:
//
//   - marker and staged database: the staged one replaces kipple.db (the old one
//     goes to backup/pre-restore-*, as with `kipple restore`), then the marker is
//     removed;
//   - marker without a staged database: the swap was done and only the marker
//     was left: it is removed;
//   - staged database (or a spooled upload) without a marker: an upload that was
//     never confirmed, or expired with the process: deleted.
//
// A failed swap puts the old database back and keeps the marker, so the next
// start tries again. local is the server's zone (PrunePreRestore).
func ApplyStaged(dataDir string, now time.Time, local *time.Location) (Applied, error) {
	mpath := filepath.Join(dataDir, MarkerFile)
	b, err := os.ReadFile(mpath)
	if errors.Is(err, fs.ErrNotExist) {
		removeStaged(dataDir)
		return Applied{}, nil
	}
	if err != nil {
		return Applied{}, fmt.Errorf("restore: read %s: %w", MarkerFile, err)
	}
	var m marker
	_ = json.Unmarshal(b, &m) // only for the log; the file's existence is the confirmation
	out := Applied{KippleVersion: m.KippleVersion, CreatedAt: m.CreatedAt, Username: m.Username}
	staged := filepath.Join(dataDir, StagedFile)
	if _, err := os.Stat(staged); err == nil {
		pre, err := Swap(dataDir, staged, now)
		if err != nil {
			return Applied{}, fmt.Errorf("restore: %w (it is tried again at the next start)", err)
		}
		syncDir(dataDir)
		out.Restored, out.Pre = true, pre
		PrunePreRestore(filepath.Join(dataDir, "backup"), local)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Applied{}, fmt.Errorf("restore: %w", err)
	}
	if err := os.Remove(mpath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return out, fmt.Errorf("restore: remove %s: %w", MarkerFile, err)
	}
	removeStaged(dataDir)
	return out, nil
}

// DiscardStaged deletes a restore staged in the setup wizard, confirmed or not,
// and reports whether a confirmed one was dropped. `kipple restore` runs it
// after its own swap: that restore is the newer decision.
func DiscardStaged(dataDir string) bool {
	err := os.Remove(filepath.Join(dataDir, MarkerFile))
	removeStaged(dataDir)
	return err == nil
}
