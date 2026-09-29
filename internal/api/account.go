package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

const (
	// minPasswordLen is the shortest password accepted (the owner chose 5; the design sets no
	// number). maxPasswordLen bounds the hashing input.
	minPasswordLen = auth.MinPasswordLen
	maxPasswordLen = auth.MaxPasswordLen
	apiPasswordLen = 24 // design §1: the UI generates 24 characters
)

// checkCurrent verifies the caller's current web password under the login
// lockout: the attempt is reserved first, a failure stays counted, a success
// clears the IP. It writes the error response itself. Both account endpoints
// prove the web password (the API password may not exist yet). An account
// without a web password proves itself with a verified Cloudflare Access token
// instead (design §7.0).
//
// removing (POST /api/account/password with remove:true) also requires a
// verified Access token, checked after the reservation so a locked-out address
// gets 429 and a refused token counts; on an account that already has no
// password it reports alreadyNone without proving anything, as there is
// nothing to change.
func (s *Server) checkCurrent(w http.ResponseWriter, r *http.Request, current string, removing bool) (alreadyNone, ok bool) {
	ip := s.clientIP(r)
	if ok, left := s.lock.Reserve(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(left/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "locked")
		return false, false
	}
	acct, ok, err := s.db.Account(r.Context())
	if err != nil || !ok {
		s.lock.Release(ip)
		if err == nil {
			err = errors.New("no account row")
		}
		s.serverError(w, "load account", err)
		return false, false
	}
	if acct.AuthMode == store.AuthOpen {
		// Open mode has no credential to prove (design 4.3): the session plus the
		// open gate stand in, so a Reader API password can only be made from
		// where open mode itself is allowed. Nothing to guess, so not counted.
		s.lock.Release(ip)
		if reason := s.signInRefusal(r); reason != "" {
			writeOpenRefused(w, reason)
			return false, false
		}
		return false, true
	}
	if acct.PasswordHash == "" {
		if removing {
			s.lock.Release(ip)
			return true, true
		}
		// No web password to prove: a verified Access token on this request
		// stands in for it; nothing else does.
		if p := s.accessProof(r); p != proofOK {
			s.writeProofError(w, ip, p, false)
			return false, false
		}
		s.lock.Clear(ip)
		return false, true
	}
	if removing {
		// Proves passwordless sign-in works for the caller right now, so the
		// removal can neither lock the owner out nor be made from an address
		// that bypasses Access.
		if p := s.accessProof(r); p != proofOK {
			s.writeProofError(w, ip, p, true)
			return false, false
		}
	}
	s.verifier.SetSecret([]byte(acct.Secret))
	pwOK, busy := s.verifier.VerifyBusy(r.Context(), "web", current, acct.PasswordHash)
	if busy {
		s.lock.Release(ip)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return false, false
	}
	if !pwOK {
		writeError(w, http.StatusForbidden, "bad_password")
		return false, false
	}
	s.lock.Clear(ip)
	return false, true
}

// badNewPassword applies the one web password rule the wizard and the
// environment use too (setup.CheckPassword: the length, and never the old
// example value) and answers 400 bad_new_password when it fails.
func badNewPassword(w http.ResponseWriter, pw string) bool {
	return badPassword(w, pw, minPasswordLen)
}

// badNewAPIPassword is badNewPassword for a chosen Reader API password, which
// guards the public ClientLogin and so needs auth.MinAPIPasswordLen (the UI only
// generates 24-character ones).
func badNewAPIPassword(w http.ResponseWriter, pw string) bool {
	return badPassword(w, pw, auth.MinAPIPasswordLen)
}

func badPassword(w http.ResponseWriter, pw string, min int) bool {
	if err := setup.CheckPassword("the new password", pw, min); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "bad_new_password", err.Error())
		return true
	}
	return false
}

// accountPassword is POST /api/account/password: `{current, new}` sets the web
// password, `{current, remove: true}` removes it (design §7.0) and
// `{current, open: true}` switches to open mode (no password at all). Every
// other session is signed out; the caller's stays.
//
// Removing is allowed only while Cloudflare Access validation is configured
// and this request carries a verified Access token: that proves passwordless
// sign-in works for the caller right now, so it can neither lock the owner out
// nor be done from a LAN address that bypasses Access. Open mode needs the
// current password and a request that passes the open gate, so it cannot be
// turned on from outside. In open mode `{new}` alone sets a password and
// returns to the standard mode (the session and same-origin are the proof).
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
		Remove  bool   `json:"remove"`
		Open    bool   `json:"open"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if n := btoi(body.Remove) + btoi(body.Open) + btoi(body.New != ""); n > 1 {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", `send one of "new", "remove": true or "open": true`)
		return
	}
	acct, exists, err := s.db.Account(r.Context())
	if err != nil || !exists {
		if err == nil {
			err = errors.New("no account row")
		}
		s.serverError(w, "load account", err)
		return
	}
	if acct.AuthMode == store.AuthOpen {
		s.accountPasswordOpen(w, r, body.New, body.Remove, body.Open)
		return
	}
	if body.Open {
		if acct.PasswordHash == "" {
			// A Cloudflare Access account: its proof is an Access header, which the
			// open gate refuses as forwarded, so the switch could never pass.
			writeErrorMsg(w, http.StatusBadRequest, "password_required",
				"set a web password first: open mode is switched on with the current password, from this computer or Tailscale")
			return
		}
		s.switchToOpen(w, r, body.Current)
		return
	}
	if !body.Remove && badNewPassword(w, body.New) {
		return
	}
	alreadyNone, ok := s.checkCurrent(w, r, body.Current, body.Remove)
	if !ok {
		return
	}
	if alreadyNone {
		// Nothing to change, and no reason to sign anyone out.
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	hash := "" // an empty hash is "no web password": the verifier never accepts it
	if !body.Remove {
		var err error
		if hash, err = auth.HashPassword(body.New); err != nil {
			s.serverError(w, "hash password", err)
			return
		}
	}
	s.setPassword(w, r, hash, store.AuthStandard)
}

// setPassword stores the web password hash and auth mode, signing out every
// other session, and answers 204.
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request, hash, mode string) {
	keep := ""
	if c, err := r.Cookie(cookieName); err == nil {
		keep = sessionID(c.Value)
	}
	if err := s.db.SetPasswordHash(r.Context(), hash, mode, keep); err != nil {
		s.serverError(w, "set password", err)
		return
	}
	s.verifier.ClearMemo()
	s.noteMode(r.Context(), func(sn *modeSnapshot) { sn.mode = mode })
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// switchToOpen is `{current, open: true}` on a password account: the current
// password and then the open gate.
func (s *Server) switchToOpen(w http.ResponseWriter, r *http.Request, current string) {
	if _, ok := s.checkCurrent(w, r, current, false); !ok {
		return
	}
	if reason := s.signInRefusal(r); reason != "" {
		writeOpenRefused(w, reason)
		return
	}
	s.setPassword(w, r, "", store.AuthOpen)
	s.log.Info("account switched to open mode (no password)")
}

// accountPasswordOpen is POST /api/account/password on an open-mode account:
// `{new}` sets a password and leaves open mode; `{open: true}` changes nothing.
func (s *Server) accountPasswordOpen(w http.ResponseWriter, r *http.Request, newPW string, remove, open bool) {
	switch {
	case open:
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	case remove:
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "open mode has no password to remove; set one first")
		return
	}
	if badNewPassword(w, newPW) {
		return
	}
	hash, err := auth.HashPassword(newPW)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	s.setPassword(w, r, hash, store.AuthStandard)
	s.log.Info("account left open mode: a web password is set")
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// accountAPIPassword is POST /api/account/api-password: exactly one of `new`
// and `generate:true`. It revokes the Reader token and clears the login memo.
func (s *Server) accountAPIPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current  string  `json:"current"`
		New      *string `json:"new"`
		Generate bool    `json:"generate"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if (body.New != nil) == body.Generate {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", `send exactly one of "new" or "generate": true`)
		return
	}
	if body.New != nil && badNewAPIPassword(w, *body.New) {
		return
	}
	if _, ok := s.checkCurrent(w, r, body.Current, false); !ok {
		return
	}
	var pw string
	if body.New != nil {
		pw = *body.New
	} else {
		var err error
		if pw, err = auth.GeneratePassword(apiPasswordLen); err != nil {
			s.serverError(w, "generate password", err)
			return
		}
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
	}
	if err := s.db.SetAPIPasswordHash(r.Context(), hash); err != nil {
		s.serverError(w, "set api password", err)
		return
	}
	s.verifier.ClearMemo()
	if s.opt.OnAPIPasswordChange != nil {
		s.opt.OnAPIPasswordChange()
	}
	if body.New != nil {
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"api_password": pw})
}
