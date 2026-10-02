package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

const maxLoginBody = 4 << 10

// login is POST /api/auth/login. The password is verified even when the user
// name is wrong (no timing oracle). An account without a password signs in only
// with a verified Cloudflare Access token (design §7.0). Ten failures from one IP in 15 minutes lock
// that IP out until the window ends (429); a success clears it.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	ip := s.clientIP(r)
	// The attempt is reserved before the password is checked (atomically with
	// the lock test), so a parallel burst cannot exceed the limit.
	if ok, left := s.lock.Reserve(ip); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(left/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "locked")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLoginBody)).Decode(&body); err != nil {
		s.lock.Release(ip)
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	acct, ok, err := s.db.Account(r.Context())
	if err != nil {
		s.log.Error("api: load account", "err", err)
		s.lock.Release(ip)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "auth")
		return
	}
	if acct.AuthMode == store.AuthOpen {
		// Open mode has no password: a stale login form must not look like a
		// failed sign-in. The app calls POST /api/auth/open instead.
		s.lock.Release(ip)
		writeErrorMsg(w, http.StatusConflict, "open_mode", "this Kipple has no password; it signs in without one")
		return
	}
	userOK := strings.EqualFold(strings.TrimSpace(body.Username), acct.Username)
	if acct.PasswordHash == "" {
		// A passwordless account (design §7.0): the one way in is a verified
		// Cloudflare Access token on this very request. Without Access validation
		// configured nothing verifies, so the account cannot sign in at all (the
		// port may be reachable on the LAN without Access in front).
		switch p := s.accessProof(r); {
		case p == proofOK && userOK: // any password sent is ignored: there is none
		case body.Password != "":
			// A password sent to an account that has none, whatever token came
			// with it: checked against the decoy hash and refused exactly like a
			// wrong password on an account with one (same cost, same answer,
			// counted), so a password guess cannot tell the two kinds apart.
			if _, busy := s.verifier.VerifyBusy(r.Context(), "web", body.Password, decoyHash); busy {
				s.lock.Release(ip)
				w.Header().Set("Retry-After", "5")
				writeError(w, http.StatusServiceUnavailable, "busy")
				return
			}
			writeError(w, http.StatusUnauthorized, "auth") // the reservation stays counted as the failure
			return
		case p == proofOK || p == proofRefused:
			writeError(w, http.StatusUnauthorized, "auth") // the reservation stays counted as the failure
			return
		case p == proofUnavailable:
			s.lock.Release(ip) // says nothing about the token
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "access_unavailable")
			return
		default: // no usable token and no password: nothing presented, not counted
			s.lock.Release(ip)
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
	} else {
		// An account with a password always needs it: an Access token never
		// stands in for a password that is set.
		s.verifier.SetSecret([]byte(acct.Secret))
		if !s.passwordOK(w, r, ip, body.Password, acct.PasswordHash) {
			return
		}
		if !userOK {
			writeError(w, http.StatusUnauthorized, "auth") // the reservation stays counted as the failure
			return
		}
	}
	s.lock.Clear(ip)
	if !s.startSession(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// passwordOK checks a sign-in password against hash and, when it does not
// match, writes the answer and settles the lockout reservation: an empty
// password (nothing presented: a submit before typing) and a busy verifier are
// not counted, a wrong password stays counted.
func (s *Server) passwordOK(w http.ResponseWriter, r *http.Request, ip, pw, hash string) bool {
	if pw == "" {
		s.lock.Release(ip)
		writeError(w, http.StatusUnauthorized, "auth")
		return false
	}
	ok, busy := s.verifier.VerifyBusy(r.Context(), "web", pw, hash)
	if busy {
		// says nothing about the password: not counted as a failure
		s.lock.Release(ip)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "auth") // the reservation stays counted as the failure
	}
	return ok
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
		s.serverError(w, "me", err)
		return
	}
	info, err := s.userInfo(r, acct, true)
	if err != nil {
		s.serverError(w, "me", err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}
