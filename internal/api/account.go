package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/auth"
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
func (s *Server) checkCurrent(w http.ResponseWriter, r *http.Request, current string) bool {
	ip := s.clientIP(r)
	if ok, left := s.lock.Reserve(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(left/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "locked")
		return false
	}
	acct, ok, err := s.db.Account(r.Context())
	if err != nil || !ok {
		s.lock.Release(ip)
		if err == nil {
			err = errors.New("no account row")
		}
		s.serverError(w, "load account", err)
		return false
	}
	if acct.PasswordHash == "" {
		// No web password to prove (design §7.0): a verified Cloudflare Access
		// token on this request stands in for it; nothing else does.
		switch s.accessProof(r) {
		case proofOK:
			s.lock.Clear(ip)
			return true
		case proofNotConfigured:
			s.lock.Release(ip)
			writeErrorMsg(w, http.StatusForbidden, "access_not_configured",
				"this account has no web password and Cloudflare Access validation is off: set a password on the host with `kipple password`")
		case proofNoToken:
			s.lock.Release(ip)
			writeErrorMsg(w, http.StatusForbidden, "access_required",
				"this account has no web password: open Kipple through Cloudflare Access to change it")
		default: // a refused token stays counted
			writeErrorMsg(w, http.StatusForbidden, "access_required",
				"this account has no web password: open Kipple through Cloudflare Access to change it")
		}
		return false
	}
	s.verifier.SetSecret([]byte(acct.Secret))
	pwOK, busy := s.verifier.VerifyBusy(r.Context(), "web", current, acct.PasswordHash)
	if busy {
		s.lock.Release(ip)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return false
	}
	if !pwOK {
		writeError(w, http.StatusForbidden, "bad_password")
		return false
	}
	s.lock.Clear(ip)
	return true
}

func badNewPassword(w http.ResponseWriter, pw string) bool {
	return badLength(w, pw, minPasswordLen)
}

// badNewAPIPassword is badNewPassword for a chosen Reader API password, which
// guards the public ClientLogin and so needs auth.MinAPIPasswordLen (the UI only
// generates 24-character ones).
func badNewAPIPassword(w http.ResponseWriter, pw string) bool {
	return badLength(w, pw, auth.MinAPIPasswordLen)
}

func badLength(w http.ResponseWriter, pw string, min int) bool {
	if n := len(pw); n < min || n > maxPasswordLen {
		writeErrorMsg(w, http.StatusBadRequest, "bad_new_password",
			"the new password must be "+strconv.Itoa(min)+" to "+strconv.Itoa(maxPasswordLen)+" characters")
		return true
	}
	return false
}

// accountPassword is POST /api/account/password: `{current, new}` sets the web
// password, `{current, remove: true}` removes it (design §7.0). Every other
// session is signed out; the caller's stays.
//
// Removing is allowed only while Cloudflare Access validation is configured
// and this request carries a verified Access token: that proves passwordless
// sign-in works for the caller right now, so it can neither lock the owner out
// nor be done from a LAN address that bypasses Access.
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
		Remove  bool   `json:"remove"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if body.Remove {
		if body.New != "" {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", `send "new" or "remove": true, not both`)
			return
		}
		acct, ok, err := s.db.Account(r.Context())
		if err != nil || !ok {
			if err == nil {
				err = errors.New("no account row")
			}
			s.serverError(w, "load account", err)
			return
		}
		if acct.PasswordHash == "" {
			// Already none: nothing to change, and no reason to sign anyone out.
			w.Header().Set("Cache-Control", "private, no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch s.accessProof(r) {
		case proofOK:
		case proofNotConfigured:
			writeErrorMsg(w, http.StatusBadRequest, "access_not_configured",
				"a web password can only be removed when Cloudflare Access validation is configured (KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD)")
			return
		default:
			writeErrorMsg(w, http.StatusForbidden, "access_required",
				"open Kipple through Cloudflare Access to remove the web password")
			return
		}
	} else if badNewPassword(w, body.New) {
		return
	}
	if !s.checkCurrent(w, r, body.Current) {
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
	keep := ""
	if c, err := r.Cookie(cookieName); err == nil {
		keep = sessionID(c.Value)
	}
	if err := s.db.SetPasswordHash(r.Context(), hash, keep); err != nil {
		s.serverError(w, "set password", err)
		return
	}
	s.verifier.ClearMemo()
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
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
	if !s.checkCurrent(w, r, body.Current) {
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
