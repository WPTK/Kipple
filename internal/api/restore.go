package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/setup"
)

// Restore in the setup wizard (docs/design.md §2.6 and §7.1e): upload a backup
// zip (or an OPML file), then confirm it, or take only its feeds, or cancel.
// The routes exist only in setup mode and share the setup guards: the Host gate,
// same-origin and X-Kipple-Client. A confirmed restore is applied by the next
// start, so a confirm answers 202 and shuts the process down (Options.Restart).

const (
	// restoreReadIdle is how long an upload may send nothing before it is cut
	// off. It replaces the server-wide ReadTimeout, which bounds the whole
	// request and would stop any large file.
	restoreReadIdle = 2 * time.Minute
	// restoreAnswerWait bounds writing the answer once the checks are done.
	restoreAnswerWait = 30 * time.Second
)

// deadlineReader moves the connection's read deadline forward before each read
// of an upload, and removes it once the whole body has arrived: from then on
// the checks run, and an expired deadline would cancel the request.
type deadlineReader struct {
	r    io.Reader
	rc   *http.ResponseController
	left int64
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	_ = d.rc.SetReadDeadline(time.Now().Add(restoreReadIdle))
	n, err := d.r.Read(p)
	d.left -= int64(n)
	if d.left <= 0 || err != nil {
		_ = d.rc.SetReadDeadline(time.Time{})
	}
	return n, err
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
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server-wide one started with the request
	body := &deadlineReader{r: r.Body, rc: rc, left: r.ContentLength}
	up, err := s.restore.Upload(r.Context(), body, r.ContentLength)
	_ = rc.SetWriteDeadline(time.Now().Add(restoreAnswerWait))
	if err != nil {
		if body.left > 0 {
			// Refused before the whole file arrived (too large, no room): say so
			// and close the connection after the answer. net/http then shuts down
			// its sending side and waits briefly before closing, which gives a
			// browser still sending the best chance to read the answer instead of
			// a reset; it cannot be guaranteed while the client keeps sending.
			w.Header().Set("Connection", "close")
		}
		s.writeRestoreError(w, "restore upload", err)
		_ = rc.Flush()
		return
	}
	if !s.opt.Setup.Pending() {
		// An account was created while the file arrived: a restore cannot follow.
		s.restore.Drop()
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	if up.Kind == backup.KindOPML {
		writeJSON(w, http.StatusOK, map[string]any{"kind": up.Kind, "feeds": up.Feeds})
		return
	}
	state, reason := s.restorePasswordState(r, up.Account)
	writeJSON(w, http.StatusOK, map[string]any{
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
	})
}

// restorePasswordState says how the backup's account signs in ("password",
// "none_access" or "open") and, when that will not work from where this
// request comes, why a new password is needed: "access_unavailable" (no
// password, and Cloudflare Access is not verified for this request) or
// "open_refused" (open mode, and the open gate refuses this request).
func (s *Server) restorePasswordState(r *http.Request, a backup.BackupAccount) (state, reason string) {
	switch {
	case a.Open:
		if s.gateRefusal(r, s.snapshot(r.Context()), true) != "" {
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
	up, ticket, ok := s.restore.Uploaded()
	if !ok {
		if s.restore.State() == backup.RestoreConfirmed {
			s.writeRestoreError(w, "restore confirm", backup.ErrRestorePending)
			return
		}
		s.writeRestoreError(w, "restore confirm", backup.ErrNoUpload)
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

// restoreFeeds is GET /api/setup/restore/feeds: the uploaded backup's
// feeds.opml (the "feeds only" choice). The upload is discarded.
func (s *Server) restoreFeeds(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	b, err := s.restore.Feeds()
	if err != nil {
		s.writeRestoreError(w, "restore feeds", err)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// restoreCancel is DELETE /api/setup/restore: removes an unconfirmed upload, or
// stops one still arriving. Idempotent.
func (s *Server) restoreCancel(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	if err := s.restore.Cancel(); err != nil {
		s.writeRestoreError(w, "restore cancel", err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// writeRestoreError maps a Restorer error to its answer.
func (s *Server) writeRestoreError(w http.ResponseWriter, what string, err error) {
	var (
		space *backup.UploadSpaceError
		newer *backup.NewerError
		bad   *backup.BadUploadError
	)
	switch {
	case errors.Is(err, context.Canceled):
		// Cancelled from another tab (DELETE), or the client went away and reads nothing.
		writeErrorMsg(w, http.StatusConflict, "restore_cancelled", "The upload was cancelled.")
	case errors.Is(err, backup.ErrRestoreBusy):
		writeErrorMsg(w, http.StatusConflict, "restore_busy", err.Error())
	case errors.Is(err, backup.ErrRestorePending):
		writeErrorMsg(w, http.StatusConflict, "restore_pending", err.Error())
	case errors.Is(err, backup.ErrNoUpload):
		status := http.StatusConflict
		if what == "restore feeds" {
			status = http.StatusNotFound
		}
		writeErrorMsg(w, status, "no_upload", err.Error())
	case errors.Is(err, backup.ErrUploadTooLarge), errors.Is(err, backup.ErrOPMLTooLarge):
		writeErrorMsg(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, backup.ErrNotBackup):
		writeErrorMsg(w, http.StatusBadRequest, "not_a_backup", err.Error())
	case errors.Is(err, backup.ErrUploadCut):
		writeErrorMsg(w, http.StatusBadRequest, "upload_incomplete", err.Error())
	case errors.As(err, &space):
		writeErrorMsg(w, http.StatusInsufficientStorage, "no_space", err.Error())
	case errors.As(err, &newer):
		writeErrorMsg(w, http.StatusUnprocessableEntity, "newer_kipple", err.Error())
	case errors.As(err, &bad):
		writeErrorMsg(w, http.StatusBadRequest, "bad_backup", err.Error())
	default:
		s.serverError(w, what, err)
	}
}
