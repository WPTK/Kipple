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
	userOK := strings.EqualFold(strings.TrimSpace(body.Username), acct.Username)
	if acct.PasswordHash == "" {
		// A passwordless account (design §7.0): the one way in is a verified
		// Cloudflare Access token on this very request. Without Access validation
		// configured nothing verifies, so the account cannot sign in at all (the
		// port may be reachable on the LAN without Access in front). Any password
		// sent is ignored: there is none to check.
		switch p := s.accessProof(r); {
		case p == proofOK && userOK:
		case p == proofOK || p == proofRefused:
			writeError(w, http.StatusUnauthorized, "auth") // the reservation stays counted as the failure
			return
		default: // no token, or Access off: nothing was presented, nothing is counted
			s.lock.Release(ip)
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
	} else {
		// An account with a password always needs it: an Access token never
		// stands in for a password that is set.
		if body.Password == "" {
			// Nothing presented (a submit before typing, a passwordless try on an
			// account that has a password): refused without counting.
			s.lock.Release(ip)
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
		s.verifier.SetSecret([]byte(acct.Secret))
		pwOK, busy := s.verifier.VerifyBusy(r.Context(), "web", body.Password, acct.PasswordHash)
		if busy {
			// says nothing about the password: not counted as a failure
			s.lock.Release(ip)
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "busy")
			return
		}
		if !pwOK || !userOK {
			// the reservation stays counted as the failure
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
	}
	s.lock.Clear(ip)
	val, err := newCookieValue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	now := s.now()
	if err := s.db.CreateSession(r.Context(), sessionID(val), now.Unix(), now.Add(sessionTTL).Unix(), r.UserAgent(), ip); err != nil {
		s.serverError(w, "create session", err) // 503 maintenance during a search-index rebuild
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
	writeJSON(w, http.StatusOK, s.userInfo(r, acct))
}
