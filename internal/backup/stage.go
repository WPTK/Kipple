package backup

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

// A restore from the setup wizard (docs/design.md §2.6) in three steps: an
// upload is spooled, then verified in the background and its database staged
// in the data directory; a confirm edits the staged copy and writes the marker,
// and the process exits; the next start applies it (ApplyStaged) before the
// database opens. Only a confirmed restore (the marker) is ever applied, so a
// forgotten upload never surprises anyone later: it expires, and the next start
// deletes it.
const (
	// UploadFile is where an upload (and `kipple restore -`) spools the file.
	UploadFile = "restore-upload.tmp"
	// StagedFile is the verified database of an upload.
	StagedFile = "restore-staged.db"
	// MarkerFile confirms StagedFile; it is written last.
	MarkerFile = "restore-pending.json"

	// DefaultUploadTTL is how long a checked, unconfirmed upload is kept.
	DefaultUploadTTL = time.Hour
	// MaxUploadBytes caps an upload: the largest database a restore accepts,
	// stored, plus room for the zip's own records and small files.
	MaxUploadBytes = DefaultMaxDBBytes + 64<<20

	// uploadSpaceSlack is kept free beyond the spooled zip.
	uploadSpaceSlack = 16 << 20
	// What the upgrade of an older backup needs at the next start, on top of
	// the staged database itself (store's own check, sized for a migration
	// that rebuilds a table): the safety copy and the rebuild.
	upgradeSpaceNum, upgradeSpaceDen = 31, 10
	upgradeSpaceSlack                = 64 << 20

	maxFeedsOPML = opml.MaxFileBytes
	sniffBytes   = 64 << 10
)

// Restore states, as GET /api/setup/restore and GET /api/instance report them.
const (
	RestoreNone      = "none"
	RestoreUploading = "uploading" // the body is arriving
	RestoreChecking  = "checking"  // a zip arrived and is being verified
	RestoreReady     = "ready"     // verified and staged, waiting for a confirm
	RestoreFailed    = "failed"    // the verification refused it (Status.Err says why)
	RestoreConfirmed = "confirmed" // the next start applies it
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
	ErrRestoreBusy = &Refusal{"Another restore upload is in progress. Cancel it first, then try again."}
	// ErrRestoreElsewhere: the upload belongs to another browser (see
	// Restorer), which alone can see, confirm or cancel it.
	ErrRestoreElsewhere = &Refusal{"Another browser is uploading or restoring a backup. Kipple stops an upload that stalls or crawls, deletes a backup nobody confirms within an hour, and clears both when it restarts."}
	ErrRestorePending   = &Refusal{"A restore is waiting to be applied: Kipple is restarting to finish it."}
	ErrNoUpload         = &Refusal{"No checked backup is waiting, or it expired. Upload it again."}
	ErrNotBackup        = &Refusal{"This is not a Kipple backup zip or an OPML file."}
	ErrUploadTooLarge   = &Refusal{"This file is larger than the " + human(MaxUploadBytes) + " a restore accepts."}
	ErrOPMLTooLarge     = &Refusal{"This OPML file is larger than the " + human(opml.MaxFileBytes) + " an import accepts."}
	ErrUploadCut        = &Refusal{"The upload stopped before the whole file arrived. Try again."}
	// ErrDiskFull: the volume filled up while the upload was being kept.
	ErrDiskFull = &Refusal{"The disk is full, so the upload could not be kept. Free some space and try again."}
	// ErrUploadTooSlow: the body reader stopped an upload that sent too slowly
	// (the API's limits). Unlike the other upload refusals it is kept as the
	// owner's failed state, because the browser, still sending, may never read
	// the answer.
	ErrUploadTooSlow = &Refusal{"The upload was too slow and was stopped. Try again on a faster connection, or restore on the server with kipple restore."}
)

// BadUploadError is a zip that is damaged, tampered with or not made by Kipple.
type BadUploadError struct{ Err error }

func (e *BadUploadError) Error() string {
	return "This backup cannot be restored: " + e.Err.Error() + "."
}

func (e *BadUploadError) Unwrap() error { return e.Err }

// UploadSpaceError refuses a restore the volume has no room for.
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

// Upload is what an upload found: the kind at once, and for a backup the rest
// once it is checked.
type Upload struct {
	Kind string // KindBackup or KindOPML
	// Feeds is the subscription count, for both kinds.
	Feeds int64
	// The rest describes a checked backup.
	Manifest        Manifest
	Info            DBInfo
	Account         BackupAccount
	EstimateSeconds int
}

// Status is where the restore stands.
type Status struct {
	State string
	// Refusal is why a new upload from this browser would be refused (ErrRestorePending, ErrRestoreBusy or
	// ErrRestoreElsewhere), nil when it would be taken. A browser whose upload connection was cut by the refusal
	// reads its reason here.
	Refusal error
	// Summary is the checked backup (ready and confirmed).
	Summary *Upload
	// Err is why the check refused the upload (failed).
	Err error
	// EstimateSeconds is how long applying it should take, once the backup's
	// manifest has been read (0 before).
	EstimateSeconds int
}

// RestorerOptions configures NewRestorer. DataDir is required.
type RestorerOptions struct {
	DataDir string
	// TTL is how long a checked, unconfirmed upload is kept (default
	// DefaultUploadTTL).
	TTL time.Duration
	// MaxBytes caps an upload (default MaxUploadBytes).
	MaxBytes int64
	// FreeBytes reports the free space on the volume of a directory (tests
	// inject; default the OS call).
	FreeBytes func(dir string) (uint64, error)
	// Live is the running instance's database: its address settings (server.*
	// and security.*) are kept through a restore. Nil keeps none.
	Live   *sql.DB
	Logger *slog.Logger
}

// Restorer holds the one restore intent of a process in setup mode. One upload
// (and its check) runs at a time, under a context of its own that Cancel, Drop
// and Close end.
//
// An upload belongs to the browser that sent it: Upload takes an owner key (a
// random value the server gave that browser before it sent the file), and
// every later call brings the caller's key. Only the owner sees
// the summary, takes the feeds, confirms or cancels; to anyone else an upload
// reads as none, and acting on it is ErrRestoreElsewhere.
type Restorer struct {
	o   RestorerOptions
	log *slog.Logger

	mu       sync.Mutex
	state    string
	job      *job // the upload or check running, if any; it alone touches the files then
	cur      *Upload
	feeds    []byte // the backup's feeds.opml
	failErr  error
	estimate int
	timer    *time.Timer
	gen      int    // bumped by each upload, discard and confirm, so stale work changes nothing
	owner    string // the owner key of the upload; "" with none
	closed   bool
}

type job struct {
	cancel context.CancelFunc
	stop   func() // unblocks a read of the body that is waiting (nil: none)
	done   chan struct{}
}

// end cancels the job and unblocks its read, if one waits.
func (j *job) end() {
	j.cancel()
	if j.stop != nil {
		j.stop()
	}
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

// State is one of the Restore* states as the server holds it, whoever the
// upload belongs to (Status is what one browser sees).
func (r *Restorer) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// mine reports whether owner is the key of the current upload. Under mu.
func (r *Restorer) mine(owner string) bool {
	return r.owner != "" && subtle.ConstantTimeCompare([]byte(r.owner), []byte(owner)) == 1
}

// othersLocked reports whether there is an upload that owner may not see or
// touch: one arriving, being checked or ready that another browser sent. A
// confirmed restore is everyone's to wait for, and another browser's refused
// upload is nothing to anyone else: it reads as none and a new upload
// replaces it. Under mu.
func (r *Restorer) othersLocked(owner string) bool {
	switch r.state {
	case RestoreUploading, RestoreChecking, RestoreReady:
		return !r.mine(owner)
	}
	return false
}

// refusalLocked is why a new upload from owner would be refused now, or nil: a restore already confirmed, this
// browser\'s own upload still running, or another browser\'s. Upload and Status share it, so the answer a refused upload
// gets and the one a status request gives are one. Under mu.
func (r *Restorer) refusalLocked(owner string) error {
	switch {
	case r.state == RestoreConfirmed:
		return ErrRestorePending
	case r.job != nil || r.state == RestoreUploading || r.state == RestoreChecking || r.state == RestoreReady:
		if r.mine(owner) {
			return ErrRestoreBusy
		}
		return ErrRestoreElsewhere
	}
	return nil
}

// othersFailedLocked reports a refused upload that is not owner's. Under mu.
func (r *Restorer) othersFailedLocked(owner string) bool {
	return r.state == RestoreFailed && !r.mine(owner)
}

// Status is the state with what goes with it, as the browser with this owner
// key sees it: another browser's upload reads as none, and only the owner
// sees the summary and the refusal.
func (r *Restorer) Status(owner string) Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.othersLocked(owner) || r.othersFailedLocked(owner) {
		// Reads as none; Refusal only says that a new upload would be refused, which the refused upload already says.
		return Status{State: RestoreNone, Refusal: r.refusalLocked(owner)}
	}
	st := Status{State: r.state, EstimateSeconds: r.estimate, Refusal: r.refusalLocked(owner)}
	if !r.mine(owner) {
		return st // none, or confirmed by another browser
	}
	if r.cur != nil && (r.state == RestoreReady || r.state == RestoreConfirmed) {
		up := *r.cur
		st.Summary = &up
	}
	if r.state == RestoreFailed {
		st.Err = r.failErr
	}
	return st
}

func (r *Restorer) path(name string) string { return filepath.Join(r.o.DataDir, name) }

// Upload reads one file of size bytes from body, which must deliver exactly
// that many. The first bytes decide, before anything is stored: an OPML file
// is read and counted (nothing is kept, the state goes back to none) and
// returned at once; anything but a zip is ErrNotBackup. A zip is spooled to
// the data directory and Upload returns Kind KindBackup as soon as the body has
// arrived: the check (manifest and checksums, the schema against a fresh
// database of its version, the integrity checks) runs in the background, and
// Status reports checking, then ready or failed. reqCtx ends the upload only
// while the body arrives. stop (may be nil) must make a read of body that is
// waiting return at once: Cancel, Drop and Close call it, since a client that
// stalls would otherwise hold the upload until its read times out. owner is
// the key every later call must bring to see or act on this upload; it must
// not be empty.
func (r *Restorer) Upload(reqCtx context.Context, owner string, body io.Reader, size int64, stop func()) (Upload, error) {
	if owner == "" {
		return Upload{}, errors.New("restore: an upload needs an owner key")
	}
	if size > r.o.MaxBytes {
		return Upload{}, ErrUploadTooLarge
	}
	if size <= 0 {
		return Upload{}, ErrNotBackup
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Upload{}, context.Canceled
	}
	if err := r.refusalLocked(owner); err != nil {
		r.mu.Unlock()
		return Upload{}, err
	}
	// none, or failed (a new upload replaces a refused one)
	r.gen++
	g := r.gen
	r.state, r.failErr, r.estimate, r.cur, r.feeds, r.owner = RestoreUploading, nil, 0, nil, nil, owner
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, stop: stop, done: make(chan struct{})}
	r.job = j
	r.mu.Unlock()

	stopReq := context.AfterFunc(reqCtx, cancel) // a client that goes away while sending ends it
	up, err := r.receive(ctx, body, size)
	stopReq()
	if err == nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && up.Kind == KindBackup {
		r.mu.Lock()
		if r.gen == g {
			r.state = RestoreChecking
			r.mu.Unlock()
			go r.check(ctx, j, g)
			return up, nil
		}
		r.mu.Unlock()
		err = context.Canceled
	}
	// Done here: an OPML file, or a refusal. Nothing is kept but a refusal for
	// slowness, as the owner's failed state (see ErrUploadTooSlow).
	removeStaged(r.o.DataDir)
	r.mu.Lock()
	if errors.Is(err, ErrUploadTooSlow) && r.gen == g {
		r.failLocked(g, err)
	} else {
		r.state, r.owner = RestoreNone, "" // the job held the slot until now: nothing else can have started
	}
	r.job = nil
	r.mu.Unlock()
	cancel()
	close(j.done)
	return up, err
}

// receive sniffs the first bytes, then counts an OPML file or spools a zip.
func (r *Restorer) receive(ctx context.Context, body io.Reader, size int64) (Upload, error) {
	src := &ctxReader{ctx, body}
	head := make([]byte, min(size, sniffBytes))
	if _, err := io.ReadFull(src, head); err != nil {
		return Upload{}, ended(ctx, err)
	}
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
	case opml.LooksLikeOPML(head):
		if size > maxFeedsOPML {
			return Upload{}, ErrOPMLTooLarge
		}
		b := make([]byte, size)
		copy(b, head)
		if _, err := io.ReadFull(src, b[len(head):]); err != nil {
			return Upload{}, ended(ctx, err)
		}
		doc, err := opml.Parse(bytes.NewReader(b))
		if err != nil {
			return Upload{}, ErrNotBackup
		}
		return Upload{Kind: KindOPML, Feeds: int64(len(doc.Feeds))}, nil
	default:
		return Upload{}, ErrNotBackup
	}
	// A zip. Its database's size is known only from its manifest, checked
	// before extracting; here only the zip itself must fit.
	if err := r.needSpace(size + uploadSpaceSlack); err != nil {
		return Upload{}, err
	}
	removeStaged(r.o.DataDir) // a leftover of an upload this process lost track of
	f, err := openSpool(r.path(UploadFile))
	if err != nil {
		return Upload{}, fmt.Errorf("restore: spool: %w", err)
	}
	sp := &spoolWriter{w: f}
	_, err = sp.Write(head)
	n := int64(len(head))
	if err == nil {
		var m int64
		m, err = io.Copy(sp, io.LimitReader(src, size-n))
		n += m
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &spoolError{cerr}
	}
	if err != nil || n != size || ctx.Err() != nil {
		return Upload{}, ended(ctx, err)
	}
	return Upload{Kind: KindBackup}, nil
}

// ended is the refusal for a body that stopped early. ErrUploadTooSlow comes
// first: when the connection's read deadline expires, net/http cancels the
// request, and with it ctx, before the reader's error gets here, and the
// reason must survive that. Else ctx's own error (the upload was cancelled),
// else a failure to write the spool file (disk full, I/O) with its real cause,
// else ErrUploadCut.
func ended(ctx context.Context, err error) error {
	var se *spoolError
	switch {
	case errors.Is(err, ErrUploadTooSlow):
		return ErrUploadTooSlow
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &se) && diskFull(se.err):
		return ErrDiskFull
	case errors.As(err, &se):
		return fmt.Errorf("restore: spool: %w", se.err)
	}
	return ErrUploadCut
}

// check verifies a spooled zip in the background and records the outcome,
// unless the upload was cancelled meanwhile.
func (r *Restorer) check(ctx context.Context, j *job, g int) {
	up, feeds, err := r.verify(ctx, g)
	r.mu.Lock()
	r.job = nil
	switch {
	case r.gen != g || ctx.Err() != nil:
		removeStaged(r.o.DataDir) // cancelled: the files are this job's to remove
		r.state, r.owner = RestoreNone, ""
	case err != nil:
		removeStaged(r.o.DataDir)
		r.failLocked(g, err)
		r.log.Info("restore: the uploaded backup was refused", "err", err)
	default:
		r.state, r.cur, r.feeds, r.estimate = RestoreReady, &up, feeds, up.EstimateSeconds
		r.expireLocked(g)
		r.log.Info("restore: backup uploaded and checked", "kipple_version", up.Manifest.KippleVersion,
			"created_at", up.Manifest.CreatedAt, "feeds", up.Info.Feeds, "items", up.Info.Items, "db_bytes", up.Manifest.DBBytes)
	}
	r.mu.Unlock()
	j.cancel()
	close(j.done)
}

func (r *Restorer) verify(ctx context.Context, g int) (Upload, []byte, error) {
	spool, staged := r.path(UploadFile), r.path(StagedFile)
	defer os.Remove(spool)
	x, err := Extract(ctx, spool, staged, r.o.FreeBytes)
	if err != nil {
		var (
			newer *NewerError
			space *UploadSpaceError
		)
		if errors.As(err, &newer) || errors.As(err, &space) || ctx.Err() != nil {
			return Upload{}, nil, err
		}
		return Upload{}, nil, &BadUploadError{err}
	}
	_ = os.Remove(spool) // the zip is not needed any more: free its space now
	mf := x.Manifest
	older := mf.SchemaVersion < store.LatestVersion()
	r.mu.Lock()
	if r.gen == g {
		r.estimate = EstimateSeconds(mf.DBBytes, older)
	}
	r.mu.Unlock()
	feeds := x.FeedsOPML
	if feeds == nil {
		return Upload{}, nil, &BadUploadError{errors.New("it holds no feeds.opml")}
	}
	info, acct, err := checkStaged(ctx, staged, mf.KippleVersion)
	if err != nil {
		return Upload{}, nil, err
	}
	older = info.SchemaVersion < store.LatestVersion()
	if older {
		if err := r.needSpace(mf.DBBytes*upgradeSpaceNum/upgradeSpaceDen + upgradeSpaceSlack); err != nil {
			return Upload{}, nil, err
		}
	}
	return Upload{
		Kind: KindBackup, Feeds: info.Feeds, Manifest: mf, Info: info, Account: acct,
		EstimateSeconds: EstimateSeconds(mf.DBBytes, older),
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

// Feeds hands out the checked backup's feeds.opml (the "feeds only" choice)
// to its owner and discards the upload: the rest of it will not be used.
func (r *Restorer) Feeds(owner string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.state == RestoreConfirmed:
		return nil, ErrRestorePending
	case r.othersLocked(owner):
		return nil, ErrRestoreElsewhere
	case r.state != RestoreReady:
		return nil, ErrNoUpload
	}
	b := r.feeds
	r.discardLocked()
	r.log.Info("restore: the feeds of the uploaded backup were taken; the upload was deleted")
	return b, nil
}

// Uploaded is the owner's checked, unconfirmed upload and its ticket for
// Confirm: ErrRestorePending once confirmed, ErrRestoreElsewhere for another
// browser's upload, ErrNoUpload without a checked one.
func (r *Restorer) Uploaded(owner string) (up Upload, ticket int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.state == RestoreConfirmed:
		return Upload{}, 0, ErrRestorePending
	case r.othersLocked(owner):
		return Upload{}, 0, ErrRestoreElsewhere
	case r.state != RestoreReady:
		return Upload{}, 0, ErrNoUpload
	}
	return *r.cur, r.gen, nil
}

// Cancel ends the owner's upload short of a confirmed one: it stops one
// arriving or being checked (unblocking its read) and waits briefly for it to
// clean up, removes a checked one, clears a failed one. Nothing to cancel is
// fine. A confirmed restore cannot be cancelled (ErrRestorePending), nor can
// another browser's upload (ErrRestoreElsewhere).
func (r *Restorer) Cancel(owner string) error {
	j, err := r.drop(&owner)
	if j != nil {
		select {
		case <-j.done:
		case <-time.After(10 * time.Second):
		}
	}
	return err
}

// Drop is Cancel without the wait: an account was created, so no restore can
// follow.
func (r *Restorer) Drop() { _, _ = r.drop(nil) }

// drop ends the upload of owner, or anyone's when owner is nil.
func (r *Restorer) drop(owner *string) (*job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == RestoreConfirmed {
		return nil, ErrRestorePending
	}
	if owner != nil && r.othersLocked(*owner) {
		return nil, ErrRestoreElsewhere
	}
	if owner != nil && r.othersFailedLocked(*owner) {
		return nil, nil // nothing of theirs to cancel
	}
	j := r.job
	if j != nil {
		// The job removes its own files and reports none once it has stopped:
		// until then it holds the slot, and the state says so (uploading or
		// checking), so the state and a busy refusal always agree.
		j.end()
		r.gen++
		return j, nil
	}
	r.discardLocked()
	return nil, nil
}

// Close stops the expiry timer and any upload or check, and waits for it. The
// files stay: the next start applies a confirmed restore and deletes anything
// else.
func (r *Restorer) Close() {
	r.mu.Lock()
	r.closed = true
	if r.timer != nil {
		r.timer.Stop()
	}
	j := r.job
	r.mu.Unlock()
	if j != nil {
		j.end()
		<-j.done
	}
}

// failLocked records the refusal of upload g as its owner's failed state,
// which expires like a ready upload. Under mu.
func (r *Restorer) failLocked(g int, err error) {
	r.state, r.failErr = RestoreFailed, err
	r.expireLocked(g)
}

// expireLocked discards upload g, ready or failed, TTL after now unless it
// has moved on (a confirm, a cancel, a new upload). It is set once per
// upload, when it becomes ready or failed: nothing a caller does renews it.
// Under mu.
func (r *Restorer) expireLocked(g int) {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.o.TTL, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.gen == g && (r.state == RestoreReady || r.state == RestoreFailed) {
			r.log.Info("restore: an unconfirmed or refused upload expired and was deleted")
			r.discardLocked()
		}
	})
}

// discardLocked forgets the upload and removes its files. Only with no job
// running (drop handles a running one).
func (r *Restorer) discardLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.gen++
	r.state, r.cur, r.feeds, r.failErr, r.estimate, r.owner = RestoreNone, nil, nil, nil, 0, ""
	removeStaged(r.o.DataDir)
}

// Confirm prepares the staged database of the checked upload and writes the
// marker that makes the next start apply it. On the staged copy only: every
// web session is signed out; the server settings (ServerSettingPrefixes: they
// describe the old server, not the data) are replaced by this server's own; and
// when passwordHash is not empty it becomes the web password, in standard
// sign-in. The caller must hold setup mode (no account row) for the whole
// call. ticket is Uploaded's: the upload that was looked at must be the one
// confirmed. ErrNoUpload without that checked upload, ErrRestorePending when
// already confirmed.
func (r *Restorer) Confirm(ctx context.Context, ticket int, passwordHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.state == RestoreConfirmed:
		return ErrRestorePending
	case r.state != RestoreReady || r.gen != ticket:
		return ErrNoUpload
	}
	staged := r.path(StagedFile)
	if err := prepareStaged(ctx, staged, passwordHash, r.o.Live); err != nil {
		return err
	}
	mf := r.cur.Manifest
	if err := writeMarker(r.path(MarkerFile), marker{KippleVersion: mf.KippleVersion, CreatedAt: mf.CreatedAt, Username: r.cur.Account.Username}, false); err != nil {
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

// spoolError marks a failure of the spool file, as opposed to the body.
type spoolError struct{ err error }

func (e *spoolError) Error() string { return e.err.Error() }
func (e *spoolError) Unwrap() error { return e.err }

// openSpool creates the spool file. A variable so a test can make it fail.
var openSpool = func(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// diskFull reports a write that failed because the volume is full: ENOSPC, or
// on Windows ERROR_DISK_FULL and ERROR_HANDLE_DISK_FULL.
func diskFull(err error) bool {
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	var en syscall.Errno
	return runtime.GOOS == "windows" && errors.As(err, &en) && (en == 112 || en == 39)
}

// spoolWriter writes the spool file and tells its failures from the body's.
type spoolWriter struct{ w io.Writer }

func (w *spoolWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil {
		err = &spoolError{err}
	}
	return n, err
}
