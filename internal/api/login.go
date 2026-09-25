package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxLoginBody = 4 << 10

// login is POST /api/auth/login. The password is verified even when the user
// name is wrong (no timing oracle). Ten failures from one IP in 15 minutes lock
// that IP out until the window ends (429); a success clears it.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	ip := s.clientIP(r)
	if locked, left := s.lock.Locked(ip); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(left/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "locked")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	acct, ok, err := s.db.Account(r.Context())
	if err != nil {
		s.log.Error("api: load account", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ok {
		s.lock.Fail(ip)
		writeError(w, http.StatusUnauthorized, "auth")
		return
	}
	s.verifier.SetSecret([]byte(acct.Secret))
	pwOK, busy := s.verifier.VerifyBusy(r.Context(), "web", body.Password, acct.PasswordHash)
	if busy {
		// says nothing about the password: not counted as a failure
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return
	}
	if !pwOK || !strings.EqualFold(strings.TrimSpace(body.Username), acct.Username) {
		s.lock.Fail(ip)
		writeError(w, http.StatusUnauthorized, "auth")
		return
	}
	s.lock.Clear(ip)
	val, err := newCookieValue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.now()
	if err := s.db.CreateSession(r.Context(), sessionID(val), now.Unix(), now.Add(sessionTTL).Unix(), r.UserAgent(), ip); err != nil {
		s.log.Error("api: create session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.setCookie(w, r, val)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if err := s.db.DeleteSession(r.Context(), sessionID(c.Value)); err != nil {
			s.log.Error("api: delete session", "err", err)
		}
	}
	s.clearCookie(w, r)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	acct, _, err := s.db.Account(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": acct.Username, "api_enabled": acct.APIPasswordHash != ""})
}
