package api

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/setup"
)

// Restore in the setup wizard (docs/design.md §2.6 and §7.1e): upload a backup
// zip (checked in the background; GET /api/setup/restore follows it) or an OPML
// file, then confirm it, or take only its feeds, or cancel.
// The routes exist only in setup mode and share the setup guards: the Host gate,
// same-origin and X-Kipple-Client. A confirmed restore is applied by the next
// start, so a confirm answers 202 and shuts the process down (Options.Restart).
//
// An uploaded backup belongs to the page that sent it (backup.Restorer). The
// page makes a random owner key, keeps it in its own origin's storage, and
// sends it in the X-Kipple-Restore-Key header with the upload and with every
// later restore call, GET /api/instance included. From its first byte the
// upload is that key's, so the uploader can cancel it while it arrives; a
// call without the key sees no restore and cannot read, confirm or cancel it.
//
// The key travels only in a header that the page's own script sets. A cookie
// would also be sent by the browser when someone else put it there (a sibling
// subdomain can set cookies for this host), and then the owner's page would
// show and confirm an upload it did not start; nothing outside the page can
// set this header.

// restoreKeyHeader carries the owner key.
const restoreKeyHeader = "X-Kipple-Restore-Key"

// restoreKeyBytes is the size of an owner key: 32 random bytes, sent as 43
// characters of unpadded base64url.
const restoreKeyBytes = 32

// restoreKey is the caller's owner key, "" when it sent none or one of the
// wrong form. The decoding is strict, so one key is one string.
func restoreKey(r *http.Request) string {
	k := r.Header.Get(restoreKeyHeader)
	b, err := base64.RawURLEncoding.Strict().DecodeString(k)
	if err != nil || len(b) != restoreKeyBytes {
		return ""
	}
	return k
}

// An upload holds the one restore slot while it arrives. The slot is first
// come, as setup is: whoever can reach setup can hold it for a while, and the
// bounds below keep that while short and finite. They replace the server-wide
// ReadTimeout, which bounds the whole request and would stop any large file.
// Variables so tests can shorten them.
var (
	// restoreReadIdle is how long an upload may send nothing.
	restoreReadIdle = 2 * time.Minute
	// restoreMinRate is the slowest average an upload may keep (bytes per
	// second), judged from restoreRateGrace on.
	restoreMinRate   = 32 << 10
	restoreRateGrace = 2 * time.Minute
	// An upload must also end within restoreMaxFloor, or within its size at
	// restoreMaxRate when that is longer, but never past restoreMaxHold
	// (restoreDeadline).
	restoreMaxFloor = 2 * time.Hour
	restoreMaxRate  = 128 << 10
	restoreMaxHold  = 4 * time.Hour
)

// restoreDeadline is how long an upload of size bytes may take at most. The
// size is only what the client declares, so it can stretch the allowance up to
// restoreMaxHold and no further.
func restoreDeadline(size int64) time.Duration {
	return min(restoreMaxHold, max(restoreMaxFloor, time.Duration(size/int64(restoreMaxRate))*time.Second))
}

// restoreAnswerWait bounds writing the answer once the checks are done.
const restoreAnswerWait = 30 * time.Second

// deadlineReader bounds the reads of an upload: before each read it sets the
// connection's read deadline to the idle limit or the end of the whole upload
// (restoreDeadline), whichever comes first, and it refuses to go on once the
// average rate has fallen below the minimum. Any of these ends the upload with
// backup.ErrUploadTooSlow, which the Restorer keeps as the owner's failed
// state: a browser still sending may never read the answer, and its page then
// asks GET /api/setup/restore why. It removes the deadline once the whole body
// has arrived: from then on an expired deadline would cancel the request.
// stop (a cancel by the uploader, an account claim, shutdown) sets the
// deadline to now, so a read waiting on a stalled client returns at once, and
// refuses every later read.
type deadlineReader struct {
	r   io.Reader
	rc  *http.ResponseController
	end time.Time // the whole upload's deadline
	// start is when the upload began.
	start time.Time

	mu      sync.Mutex // orders stop against the deadline each read sets
	stopped bool
	left    int64
	got     int64
}

var errUploadStopped = errors.New("restore: upload stopped")

func newDeadlineReader(r io.Reader, rc *http.ResponseController, size int64) *deadlineReader {
	now := time.Now()
	return &deadlineReader{r: r, rc: rc, left: size, start: now, end: now.Add(restoreDeadline(size))}
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return 0, errUploadStopped
	}
	now := time.Now()
	if el := now.Sub(d.start); !now.Before(d.end) || (el > restoreRateGrace && float64(d.got) < float64(restoreMinRate)*el.Seconds()) {
		d.stopped = true // and the deadline stays in the past, so nothing more is read
		_ = d.rc.SetReadDeadline(now)
		d.mu.Unlock()
		return 0, backup.ErrUploadTooSlow
	}
	dl := now.Add(restoreReadIdle)
	if d.end.Before(dl) {
		dl = d.end
	}
	_ = d.rc.SetReadDeadline(dl)
	d.mu.Unlock()
	n, err := d.r.Read(p)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.left -= int64(n)
	d.got += int64(n)
	if err != nil && !d.stopped && errors.Is(err, os.ErrDeadlineExceeded) {
		d.stopped = true // the idle limit or the end: the deadline stays
		return n, backup.ErrUploadTooSlow
	}
	if (d.left <= 0 || err != nil) && !d.stopped {
		_ = d.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

func (d *deadlineReader) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	_ = d.rc.SetReadDeadline(time.Now())
}

// unread reports whether part of the body was never read.
func (d *deadlineReader) unread() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.left > 0
}

// restoreUpload is POST /api/setup/restore/upload: the raw file as the body,
// with its Content-Length.
func (s *Server) restoreUpload(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	if r.ContentLength < 0 {
		writeErrorMsg(w, http.StatusLengthRequired, "length_required", "Send the file with its length (Content-Length).")
		return
	}
	key := restoreKey(r)
	if key == "" {
		// Before the body: close the connection after the answer, as below.
		w.Header().Set("Connection", "close")
		writeErrorMsg(w, http.StatusBadRequest, "restore_key_required", "Start the upload again from the setup page.")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server-wide one started with the request
	body := newDeadlineReader(r.Body, rc, r.ContentLength)
	up, err := s.restore.Upload(r.Context(), key, body, r.ContentLength, body.stop)
	_ = rc.SetWriteDeadline(time.Now().Add(restoreAnswerWait))
	if err != nil {
		if body.unread() {
			// Refused before the whole file arrived (not a backup, too large, no
			// room): say so and close the connection after the answer. net/http
			// then shuts down its sending side and waits briefly before closing,
			// which gives a browser still sending the best chance to read the
			// answer instead of a reset; it cannot be guaranteed while the client
			// keeps sending.
			w.Header().Set("Connection", "close")
		}
		switch {
		case !s.opt.Setup.Pending():
			// An account was created while the file arrived, which cancelled it.
			writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		default:
			s.writeRestoreError(w, "restore upload", err)
		}
		_ = rc.Flush()
		return
	}
	if !s.opt.Setup.Pending() {
		// An account was created as the file arrived: a restore cannot follow.
		s.restore.Drop()
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	if up.Kind == backup.KindOPML {
		writeJSON(w, http.StatusOK, map[string]any{"kind": up.Kind, "feeds": up.Feeds})
		return
	}
	// A zip: it is checked in the background; GET /api/setup/restore follows it,
	// for this browser only.
	writeJSON(w, http.StatusAccepted, map[string]string{"state": backup.RestoreChecking})
}

// restoreStatus is GET /api/setup/restore: where the restore stands, with the
// checked backup's summary when it is ready and the refusal when it failed.
func (s *Server) restoreStatus(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	st := s.restore.Status(restoreKey(r))
	out := map[string]any{"state": st.State, "summary": nil, "error": nil, "estimate_seconds": st.EstimateSeconds}
	if st.Summary != nil { // ready and confirmed
		out["summary"] = s.restoreSummary(r, *st.Summary)
	}
	if st.Err != nil {
		_, code, msg := restoreErrorInfo(st.Err)
		if code == "" {
			s.log.Error("restore check", "err", st.Err)
			code, msg = "internal", "The backup could not be checked. Try again."
		}
		out["error"] = map[string]string{"code": code, "message": msg}
	}
	writeJSON(w, http.StatusOK, out)
}

// restoreSummary is what the wizard shows about a checked backup.
func (s *Server) restoreSummary(r *http.Request, up backup.Upload) map[string]any {
	state, reason := s.restorePasswordState(r, up.Account)
	return map[string]any{
		"kind":                up.Kind,
		"kipple_version":      up.Manifest.KippleVersion,
		"created_at":          up.Manifest.CreatedAt,
		"feeds":               up.Info.Feeds,
		"items":               up.Info.Items,
		"starred":             up.Info.Starred,
		"username":            up.Account.Username,
		"password_state":      state,
		"needs_new_password":  reason != "",
		"new_password_reason": reason,
		"estimate_seconds":    up.EstimateSeconds,
	}
}

// restorePasswordState says how the backup's account signs in ("password",
// "none_access" or "open") and, when that will not work from where this
// request comes, why a new password is needed: "access_unavailable" (no
// password, and Cloudflare Access is not verified for this request) or
// "open_refused" (open mode, and the open gate refuses this request).
func (s *Server) restorePasswordState(r *http.Request, a backup.BackupAccount) (state, reason string) {
	switch {
	case a.Open:
		// The network part of the open gate, as GET /api/instance reports it:
		// a status poll is a GET and carries no Origin for the browser part.
		if s.gateRefusal(r, s.snapshot(r.Context()), false) != "" {
			return "open", "open_refused"
		}
		return "open", ""
	case !a.HasPassword:
		if s.reach.Access() == nil || s.accessProof(r) != proofOK {
			return "none_access", "access_unavailable"
		}
		return "none_access", ""
	}
	return "password", ""
}

// restoreConfirm is POST /api/setup/restore/confirm {"new_password": "..."}:
// under the setup slot (so it and an account creation exclude each other), with
// still no account, it prepares the staged database, writes the marker, answers
// 202 and shuts Kipple down; the next start applies the restore.
func (s *Server) restoreConfirm(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	var body struct {
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &body, maxSetupBody, true) {
		return
	}
	if body.NewPassword != "" {
		if err := setup.CheckPassword("the new password", body.NewPassword, auth.MinPasswordLen); err != nil {
			writeErrorMsg(w, http.StatusBadRequest, "bad_new_password", err.Error())
			return
		}
	}
	select {
	case s.setupSlot <- struct{}{}:
		defer func() { <-s.setupSlot }()
	case <-r.Context().Done():
		return
	}
	if !s.opt.Setup.Pending() {
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	if _, exists, err := s.db.Account(r.Context()); err != nil {
		s.serverError(w, "restore confirm", err)
		return
	} else if exists {
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	up, ticket, err := s.restore.Uploaded(restoreKey(r)) // only this browser's checked backup
	if err != nil {
		s.writeRestoreError(w, "restore confirm", err)
		return
	}
	if _, reason := s.restorePasswordState(r, up.Account); reason != "" && body.NewPassword == "" {
		msg := "This backup's account signs in with Cloudflare Access, which does not work for this address yet. Set a new password to sign in with."
		if reason == "open_refused" {
			msg = "This backup's account has no password, and Kipple cannot let this browser in without one from where it is. Set a new password to sign in with."
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password_required", "reason": reason, "message": msg})
		return
	}
	var hash string
	if body.NewPassword != "" {
		var err error
		if hash, err = auth.HashPassword(body.NewPassword); err != nil {
			s.serverError(w, "restore confirm: hash", err)
			return
		}
	}
	if err := s.restore.Confirm(r.Context(), ticket, hash); err != nil {
		s.writeRestoreError(w, "restore confirm", err)
		return
	}
	s.log.Info("restore confirmed; restarting to apply it", "username", up.Account.Username,
		"new_password", hash != "", "client", s.clientIP(r))
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "estimate_seconds": up.EstimateSeconds})
	_ = http.NewResponseController(w).Flush()
	if s.opt.Restart != nil {
		s.opt.Restart()
	}
}

// restoreFeeds is GET /api/setup/restore/feeds: the checked backup's
// feeds.opml (the "feeds only" choice). The upload is discarded.
func (s *Server) restoreFeeds(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	b, err := s.restore.Feeds(restoreKey(r))
	if err != nil {
		s.writeRestoreError(w, "restore feeds", err)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// restoreCancel is DELETE /api/setup/restore: stops an upload arriving or being
// checked, removes a checked one, clears a failed one. Idempotent; 409 only once
// confirmed.
func (s *Server) restoreCancel(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	if err := s.restore.Cancel(restoreKey(r)); err != nil {
		s.writeRestoreError(w, "restore cancel", err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// writeRestoreError answers a Restorer error.
func (s *Server) writeRestoreError(w http.ResponseWriter, what string, err error) {
	status, code, msg := restoreErrorInfo(err)
	if code == "" {
		s.serverError(w, what, err)
		return
	}
	if code == "no_upload" && what == "restore feeds" {
		status = http.StatusNotFound
	}
	writeErrorMsg(w, status, code, msg)
}

// restoreErrorInfo maps a Restorer error to its status, code and message; code
// is "" for an unexpected error.
func restoreErrorInfo(err error) (status int, code, msg string) {
	var (
		space *backup.UploadSpaceError
		newer *backup.NewerError
		bad   *backup.BadUploadError
	)
	switch {
	case errors.Is(err, context.Canceled):
		// Cancelled (DELETE from another tab), or the client went away.
		return http.StatusConflict, "restore_cancelled", "The upload was cancelled."
	case errors.Is(err, backup.ErrRestoreBusy):
		return http.StatusConflict, "restore_busy", err.Error()
	case errors.Is(err, backup.ErrRestoreElsewhere):
		return http.StatusConflict, "restore_elsewhere", err.Error()
	case errors.Is(err, backup.ErrRestorePending):
		return http.StatusConflict, "restore_pending", err.Error()
	case errors.Is(err, backup.ErrNoUpload):
		return http.StatusConflict, "no_upload", err.Error()
	case errors.Is(err, backup.ErrUploadTooLarge), errors.Is(err, backup.ErrOPMLTooLarge):
		return http.StatusRequestEntityTooLarge, "too_large", err.Error()
	case errors.Is(err, backup.ErrNotBackup):
		return http.StatusBadRequest, "not_a_backup", err.Error()
	case errors.Is(err, backup.ErrUploadCut):
		return http.StatusBadRequest, "upload_incomplete", err.Error()
	case errors.Is(err, backup.ErrUploadTooSlow):
		return http.StatusRequestTimeout, "upload_too_slow", err.Error()
	case errors.As(err, &space):
		return http.StatusInsufficientStorage, "no_space", err.Error()
	case errors.As(err, &newer):
		return http.StatusUnprocessableEntity, "newer_kipple", err.Error()
	case errors.As(err, &bad):
		return http.StatusBadRequest, "bad_backup", err.Error()
	}
	return http.StatusInternalServerError, "", ""
}
