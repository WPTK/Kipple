package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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
	ErrRestoreBusy    = &Refusal{"Another restore upload is in progress. Cancel it first, then try again."}
	ErrRestorePending = &Refusal{"A restore is waiting to be applied: Kipple is restarting to finish it."}
	ErrNoUpload       = &Refusal{"No checked backup is waiting, or it expired. Upload it again."}
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
	Logger    *slog.Logger
}

// Restorer holds the one restore intent of a process in setup mode. One upload
// (and its check) runs at a time, under a context of its own that Cancel, Drop
// and Close end.
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
	gen      int // bumped by each upload, discard and confirm, so stale work changes nothing
	closed   bool
}

type job struct {
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

// State is one of the Restore* states.
func (r *Restorer) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Status is the state with what goes with it.
func (r *Restorer) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{State: r.state, EstimateSeconds: r.estimate}
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
// while the body arrives.
func (r *Restorer) Upload(reqCtx context.Context, body io.Reader, size int64) (Upload, error) {
	if size > r.o.MaxBytes {
		return Upload{}, ErrUploadTooLarge
	}
	if size <= 0 {
		return Upload{}, ErrNotBackup
	}
	r.mu.Lock()
	switch {
	case r.closed:
		r.mu.Unlock()
		return Upload{}, context.Canceled
	case r.state == RestoreConfirmed:
		r.mu.Unlock()
		return Upload{}, ErrRestorePending
	case r.job != nil || r.state == RestoreUploading || r.state == RestoreChecking || r.state == RestoreReady:
		r.mu.Unlock()
		return Upload{}, ErrRestoreBusy
	}
	// none, or failed (a new upload replaces a refused one)
	r.gen++
	g := r.gen
	r.state, r.failErr, r.estimate, r.cur, r.feeds = RestoreUploading, nil, 0, nil, nil
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{cancel: cancel, done: make(chan struct{})}
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
	// Done here: an OPML file, or a refusal. Nothing is kept.
	removeStaged(r.o.DataDir)
	r.mu.Lock()
	if r.gen == g {
		r.state = RestoreNone
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
		if ctx.Err() != nil {
			return Upload{}, ctx.Err()
		}
		return Upload{}, ErrUploadCut
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
			if ctx.Err() != nil {
				return Upload{}, ctx.Err()
			}
			return Upload{}, ErrUploadCut
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
	f, err := os.OpenFile(r.path(UploadFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Upload{}, fmt.Errorf("restore: spool: %w", err)
	}
	_, err = f.Write(head)
	n := int64(len(head))
	if err == nil {
		var m int64
		m, err = io.Copy(f, io.LimitReader(src, size-n))
		n += m
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if ctx.Err() != nil {
		return Upload{}, ctx.Err()
	}
	if err != nil || n != size {
		return Upload{}, ErrUploadCut
	}
	return Upload{Kind: KindBackup}, nil
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
	case err != nil:
		removeStaged(r.o.DataDir)
		r.state, r.failErr = RestoreFailed, err
		r.log.Info("restore: the uploaded backup was refused", "err", err)
	default:
		r.state, r.cur, r.feeds, r.estimate = RestoreReady, &up, feeds, up.EstimateSeconds
		r.timer = time.AfterFunc(r.o.TTL, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.gen == g && r.state == RestoreReady {
				r.log.Info("restore: an unconfirmed upload expired and was deleted")
				r.discardLocked()
			}
		})
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
	feeds, ok := x.Files[OPMLFile]
	if !ok {
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
// and discards the upload: the rest of it will not be used.
func (r *Restorer) Feeds() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == RestoreConfirmed {
		return nil, ErrRestorePending
	}
	if r.state != RestoreReady {
		return nil, ErrNoUpload
	}
	b := r.feeds
	r.discardLocked()
	r.log.Info("restore: the feeds of the uploaded backup were taken; the upload was deleted")
	return b, nil
}

// Uploaded is the checked, unconfirmed upload, if there is one, and its ticket
// for Confirm.
func (r *Restorer) Uploaded() (up Upload, ticket int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != RestoreReady {
		return Upload{}, 0, false
	}
	return *r.cur, r.gen, true
}

// Cancel ends any upload short of a confirmed one: it stops one arriving or
// being checked (and waits briefly for it to clean up), removes a checked one,
// clears a failed one. Nothing to cancel is fine. A confirmed restore cannot be
// cancelled (ErrRestorePending).
func (r *Restorer) Cancel() error {
	j, err := r.drop()
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
func (r *Restorer) Drop() { _, _ = r.drop() }

func (r *Restorer) drop() (*job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == RestoreConfirmed {
		return nil, ErrRestorePending
	}
	j := r.job
	if j != nil {
		j.cancel() // the job removes its own files when it stops
	}
	r.discardLocked()
	return j, nil
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
		j.cancel()
		<-j.done
	}
}

// discardLocked forgets the upload. Its files are removed here only when no
// job runs; a running one removes them itself once it sees it was cancelled.
func (r *Restorer) discardLocked() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.gen++
	r.state, r.cur, r.feeds, r.failErr, r.estimate = RestoreNone, nil, nil, nil, 0
	if r.job == nil {
		removeStaged(r.o.DataDir)
	}
}

// Confirm prepares the staged database of the checked upload and writes the
// marker that makes the next start apply it. On the staged copy only: every
// web session is signed out; the address settings (HostSettings: they describe
// the old server, not the data) are cleared, so this server's own apply; and
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
