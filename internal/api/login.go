package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

const maxLoginBody = 4 << 10

// login is POST /api/auth/login. The password is verified even when the user
// name is wrong (no timing oracle). An account without a password signs in only
// with a verified Cloudflare Access token (design §7.0). Wrong passwords are
// slowed per client, not counted against anyone else (auth.FailureTracker): five
// are free, then each wait doubles from two seconds to a minute, and a right
// password clears it. Clients are told apart by auth.ClientIP, so with the proxy
// list set correctly a stranger cannot affect the owner at all; only people who
// really share one address (an unlisted proxy, a Docker gateway, CGNAT) share a
// budget, and a flood there can make sign-in answer 503 busy (never 429).
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	t, ok := s.admit(w, r)
	if !ok {
		return
	}
	defer t.end()
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
		writeError(w, http.StatusUnauthorized, "auth")
		return
	}
	if acct.AuthMode == store.AuthOpen {
		// Open mode has no password: a stale login form must not look like a
		// failed sign-in. The app calls POST /api/auth/open instead.
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
				w.Header().Set("Retry-After", "5")
				writeError(w, http.StatusServiceUnavailable, "busy")
				return
			}
			t.fail()
			writeError(w, http.StatusUnauthorized, "auth")
			return
		case p == proofOK || p == proofRefused:
			t.fail()
			writeError(w, http.StatusUnauthorized, "auth")
			return
		case p == proofUnavailable: // says nothing about the token: not counted
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "access_unavailable")
			return
		default: // no usable token and no password: nothing presented, not counted
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
	} else {
		// An account with a password always needs it: an Access token never
		// stands in for a password that is set.
		s.verifier.SetSecret([]byte(acct.Secret))
		if !s.passwordOK(w, r, t, body.Password, acct.PasswordHash) {
			return
		}
		if !userOK {
			t.fail()
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
	}
	t.prove()
	if !s.startSession(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// passwordOK checks a sign-in password against hash and, when it does not
// match, writes the answer and counts it: an empty password (nothing presented:
// a submit before typing) and a busy verifier are not counted, a wrong password is.
func (s *Server) passwordOK(w http.ResponseWriter, r *http.Request, t *try, pw, hash string) bool {
	if pw == "" {
		writeError(w, http.StatusUnauthorized, "auth")
		return false
	}
	ok, busy := s.verifier.VerifyBusy(r.Context(), "web", pw, hash)
	if busy {
		// says nothing about the password: not counted as a failure
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return false
	}
	if !ok {
		t.fail()
		writeError(w, http.StatusUnauthorized, "auth")
	}
	return ok
}

// try is one admitted password attempt of a client: the web sign-in and the
// account endpoints that prove the current password share one budget per client.
type try struct {
	s      *Server
	ip     string
	failed bool
	proved bool
}

// admit waits (bounded) for the client's turn before any password is hashed.
// When the turn does not come (too many of that client's attempts already
// waiting, or the wait ran out) it answers 503 busy, the same answer as a busy
// verifier, and counts nothing; the caller then returns. After true the caller
// ends the attempt exactly once.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) (*try, bool) {
	ip := s.clientIP(r)
	if !s.fails.Acquire(r.Context(), ip) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "busy")
		return nil, false
	}
	return &try{s: s, ip: ip}, true
}

// fail records that this attempt presented a wrong password (or token).
func (t *try) fail() { t.failed = true }

// prove records that the password (or Access token) was verified: the
// client's failures are cleared.
func (t *try) prove() { t.proved = true }

// end finishes the attempt; only a failed one is counted, a proved one clears.
func (t *try) end() {
	t.s.fails.Finish(t.ip, t.failed)
	if t.proved {
		t.s.fails.Forget(t.ip)
	}
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
	info, err := s.userInfo(r, acct, true)
	if err != nil {
		s.serverError(w, "me", err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}
