package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
// An uploaded backup belongs to the browser that sent it (backup.Restorer).
// Before the file, POST /api/setup/restore/start gives the browser an owner
// key, set as an HttpOnly cookie and also returned to the page, and GET
// /api/setup/restore/cookie lets the page check that the cookie came back. The
// upload sends the key in a header, and it must match one of the request's
// cookies; from its first byte the upload is that key's, so the uploader can
// cancel it while it arrives, and every later route reads the caller's
// cookies. Anyone else who can reach setup sees no restore and cannot read,
// confirm or cancel the upload.
//
// Only keys this process made are accepted (they carry a MAC under a secret of
// the process), and start always makes a new one: it never hands back a value
// the browser sent. A cookie planted in the browser by someone else (a sibling
// subdomain can set one) therefore never becomes the page's key: the upload's
// header comes only from the page's own script, which read it from start.

const (
	// restoreCookie carries the owner key of a wizard upload.
	restoreCookie = "kipple_restore"
	// restoreKeyHeader carries the same key on the upload itself.
	restoreKeyHeader = "X-Kipple-Restore-Key"
)

// restoreOwner is the caller's owner key: the one of its restore cookies that
// owns the upload there is, "" when none does.
func (s *Server) restoreOwner(r *http.Request) string {
	var keys []string
	for _, c := range r.CookiesNamed(restoreCookie) {
		keys = append(keys, c.Value)
	}
	return s.restore.Owner(keys)
}

// hasRestoreCookie reports whether one of the request's restore cookies is key.
func hasRestoreCookie(r *http.Request, key string) bool {
	for _, c := range r.CookiesNamed(restoreCookie) {
		if c.Value == key {
			return true
		}
	}
	return false
}

// restoreKeyLen is the length of a key in bytes: 16 random, then 16 of MAC.
const restoreKeyLen = 32

// newRestoreKey makes an owner key: 16 random bytes and the first 16 bytes of
// their HMAC-SHA256 under the process's restoreSecret, in unpadded base64url.
func (s *Server) newRestoreKey() (string, error) {
	b := make([]byte, restoreKeyLen)
	if _, err := rand.Read(b[:16]); err != nil {
		return "", err
	}
	copy(b[16:], s.restoreMAC(b[:16]))
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) restoreMAC(nonce []byte) []byte {
	m := hmac.New(sha256.New, s.restoreSecret)
	m.Write(nonce)
	return m.Sum(nil)[:16]
}

// madeRestoreKey reports whether k is a key newRestoreKey made in this process.
func (s *Server) madeRestoreKey(k string) bool {
	b, err := base64.RawURLEncoding.DecodeString(k)
	if err != nil || len(b) != restoreKeyLen {
		return false
	}
	return hmac.Equal(b[16:], s.restoreMAC(b[:16]))
}

// restoreStart is POST /api/setup/restore/start: a new owner key for the next
// upload, as the cookie and as {"key"}. A browser that owns an upload arriving,
// being checked or ready gets 409 restore_busy and no key: it cancels that
// one first. Nothing is stored, so calling it costs nothing and holds nothing.
func (s *Server) restoreStart(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	if owner := s.restoreOwner(r); owner != "" {
		switch s.restore.Status(owner).State {
		case backup.RestoreUploading, backup.RestoreChecking, backup.RestoreReady:
			s.writeRestoreError(w, "restore start", backup.ErrRestoreBusy)
			return
		}
	}
	if s.restore.State() == backup.RestoreConfirmed {
		s.writeRestoreError(w, "restore start", backup.ErrRestorePending)
		return
	}
	key, err := s.newRestoreKey()
	if err != nil {
		s.serverError(w, "restore start: owner key", err)
		return
	}
	s.setRestoreCookie(w, r, key)
	writeJSON(w, http.StatusOK, map[string]string{"key": key})
}

// restoreKeyRefusal is why an upload's key will not do, "" when it will: not
// one start made, or no cookie with it (the browser did not keep start's).
func (s *Server) restoreKeyRefusal(w http.ResponseWriter, r *http.Request) bool {
	key := r.Header.Get(restoreKeyHeader)
	switch {
	case !s.madeRestoreKey(key):
		writeErrorMsg(w, http.StatusBadRequest, "restore_key_required", "Start the upload again from the setup page.")
	case !hasRestoreCookie(r, key):
		writeErrorMsg(w, http.StatusBadRequest, "cookies_required",
			"Kipple needs cookies to restore a backup: it keeps the upload for the browser that sent it. Allow cookies for this site, then try again.")
	default:
		return false
	}
	return true
}

// restoreCookieCheck is GET /api/setup/restore/cookie with the key from start
// in X-Kipple-Restore-Key: 204 when the cookie came back with it, else the
// refusal the upload would give. The page asks before it sends the file, so a
// browser that blocks cookies reads the reason instead of a connection cut
// while it is still sending.
func (s *Server) restoreCookieCheck(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	if s.restoreKeyRefusal(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// setRestoreCookie hands the owner key to the browser. It lives longer than
// any upload (the Restorer forgets an unconfirmed one after its TTL), and only
// the setup routes and GET /api/instance read it.
func (s *Server) setRestoreCookie(w http.ResponseWriter, r *http.Request, key string) {
	http.SetCookie(w, &http.Cookie{
		Name: restoreCookie, Value: key, Path: "/api/", MaxAge: int((24 * time.Hour) / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.scheme(r) == "https",
	})
}

// An upload holds the one restore slot while it arrives, so it must not be
// able to hold it for ever by sending slowly. These replace the server-wide
// ReadTimeout, which bounds the whole request and would stop any large file.
// The rate bounds the whole upload too: one that keeps it takes at most its
// size over the rate, plus the grace. Variables so tests can shorten them.
var (
	// restoreReadIdle is how long an upload may send nothing.
	restoreReadIdle = 2 * time.Minute
	// restoreMinRate is the slowest average an upload may keep (bytes per
	// second), judged from restoreRateGrace on.
	restoreMinRate   = 32 << 10
	restoreRateGrace = 2 * time.Minute
)

// restoreAnswerWait bounds writing the answer once the checks are done.
const restoreAnswerWait = 30 * time.Second

// deadlineReader bounds the reads of an upload: before each read it sets the
// connection's read deadline to the idle limit, and it refuses to go on once
// the average rate has fallen below the minimum. Either ends the upload with
// backup.ErrUploadTooSlow, which the Restorer keeps as the owner's failed
// state: a browser still sending may never read the answer, and its page then
// asks GET /api/setup/restore why. It removes the deadline once the whole body
// has arrived: from then on an expired deadline would cancel the request.
// stop (a cancel by the uploader, an account claim, shutdown) sets the
// deadline to now, so a read waiting on a stalled client returns at once, and
// refuses every later read.
type deadlineReader struct {
	r     io.Reader
	rc    *http.ResponseController
	start time.Time

	mu      sync.Mutex // orders stop against the deadline each read sets
	stopped bool
	left    int64
	got     int64
}

var errUploadStopped = errors.New("restore: upload stopped")

func (d *deadlineReader) Read(p []byte) (int, error) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return 0, errUploadStopped
	}
	now := time.Now()
	if el := now.Sub(d.start); el > restoreRateGrace && float64(d.got) < float64(restoreMinRate)*el.Seconds() {
		d.stopped = true // and the deadline stays in the past, so nothing more is read
		_ = d.rc.SetReadDeadline(now)
		d.mu.Unlock()
		return 0, backup.ErrUploadTooSlow
	}
	_ = d.rc.SetReadDeadline(now.Add(restoreReadIdle))
	d.mu.Unlock()
	n, err := d.r.Read(p)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.left -= int64(n)
	d.got += int64(n)
	if err != nil && !d.stopped && errors.Is(err, os.ErrDeadlineExceeded) {
		d.stopped = true // the idle limit: the deadline stays
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
	if s.restoreKeyRefusal(w, r) {
		// Before the body: close after the answer, as for the refusals below.
		// The page asks GET /api/setup/restore/cookie first, so it does not
		// rely on reading this one.
		w.Header().Set("Connection", "close")
		return
	}
	key := r.Header.Get(restoreKeyHeader)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // the server-wide one started with the request
	body := &deadlineReader{r: r.Body, rc: rc, left: r.ContentLength, start: time.Now()}
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
	st := s.restore.Status(s.restoreOwner(r))
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
	up, ticket, err := s.restore.Uploaded(s.restoreOwner(r)) // only this browser's checked backup
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
	b, err := s.restore.Feeds(s.restoreOwner(r))
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
	if err := s.restore.Cancel(s.restoreOwner(r)); err != nil {
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
