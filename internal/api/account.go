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
// prove the web password (the API password may not exist yet).
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
	if n := len(pw); n < minPasswordLen || n > maxPasswordLen {
		writeErrorMsg(w, http.StatusBadRequest, "bad_new_password",
			"the new password must be "+strconv.Itoa(minPasswordLen)+" to "+strconv.Itoa(maxPasswordLen)+" characters")
		return true
	}
	return false
}

// accountPassword is POST /api/account/password. Every other session is signed
// out; the caller's stays.
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeBody(w, r, &body, false) || badNewPassword(w, body.New) || !s.checkCurrent(w, r, body.Current) {
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		s.serverError(w, "hash password", err)
		return
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
	if body.New != nil && badNewPassword(w, *body.New) {
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
