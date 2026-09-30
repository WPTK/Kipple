package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

const (
	// setupCookieName is the setup session a claim starts: scoped to the setup
	// routes, SameSite=Strict, one hour.
	setupCookieName = "kipple_setup"
	setupCookiePath = "/api/setup"
	maxSetupBody    = 4 << 10
)

// registerSetup mounts the setup-mode routes (design 4.1). They exist only in
// a process that started without an account; each handler also checks the
// one-way flag, so once the account exists they answer 404 like any unknown
// /api/ route.
func (s *Server) registerSetup(mux *http.ServeMux) {
	if !s.opt.Setup.Pending() {
		return
	}
	mux.HandleFunc("GET /api/setup/state", s.setupState)
	mux.HandleFunc("POST /api/setup/claim", s.setupClaim)
	mux.HandleFunc("POST /api/setup/account", s.setupAccount)
}

// setupGone answers a setup route once setup has finished (or in a process
// that never was in setup mode) and reports true.
func (s *Server) setupGone(w http.ResponseWriter) bool {
	if s.opt.Setup.Pending() {
		return false
	}
	writeError(w, http.StatusNotFound, "not_found")
	return true
}

// instance is GET /api/instance: the one fact the signed-out app needs to pick
// its first screen. No version, no user name.
func (s *Server) instance(w http.ResponseWriter, r *http.Request) {
	var authMode any
	if !s.opt.Setup.Pending() {
		acct, ok, err := s.db.Account(r.Context())
		if err != nil {
			s.serverError(w, "instance", err)
			return
		}
		if ok {
			authMode = setup.DisplayMode(acct)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup": s.opt.Setup.Pending(), "auth": authMode})
}

// setupState is GET /api/setup/state.
func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	claimed := false
	if c, err := r.Cookie(setupCookieName); err == nil {
		claimed = s.opt.Setup.SessionOK(c.Value)
	}
	issued := s.opt.Setup.IssuedAt().UTC()
	snap := s.snapshot(r.Context())
	// Whether open mode would work from where this browser is: reason is the
	// open gate's answer as things are, lan_reason with "Also allow devices on
	// my local network" on (in a container even this computer needs that).
	orNull := func(reason string) any {
		if reason == "" {
			return nil
		}
		return reason
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"claimed": claimed,
		"access": map[string]bool{
			"enabled":  s.opt.Access != nil,
			"verified": s.opt.Access != nil && s.accessProof(r) == proofOK,
		},
		"open": map[string]any{
			"reason":     orNull(s.gateRefusal(r, snap, false, false)),
			"lan_reason": orNull(s.gateRefusal(r, snap, true, false)),
		},
		"token_hint":      "printed in the server log (standard error) at " + issued.Format(time.RFC3339) + "; run `kipple setup-token` to show it again",
		"token_issued_at": issued.Unix(),
	})
}

// setupClaim is POST /api/setup/claim {token}: a matching token starts the
// setup session (the kipple_setup cookie). Each attempt reserves a slot on the
// per-IP setup lockout (never the login's) before the token is checked, as the
// login does, so a parallel burst cannot exceed the limit; a wrong token keeps
// its reservation and counts towards the global rotation. Past the limit an
// address gets one check per lockedCheckEvery (the others are answered 429
// unchecked), and a wrong token there no longer counts towards the rotation
// (one noisy client cannot keep replacing the code). The right token is still
// accepted in that one check: behind Docker's port forwarding every client
// shares the gateway's address, and one noisy device must not lock the owner
// out of claiming for good, only make them wait up to a minute. At 120 bits the
// lockout and the rotation are about noise, not about guessing (design 5.1).
func (s *Server) setupClaim(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decodeJSON(w, r, &body, maxSetupBody, false) {
		return // nothing presented: not counted
	}
	if body.Token == "" {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ip := s.clientIP(r)
	reserved, left := s.setupLock.Reserve(ip)
	claim := s.opt.Setup.Claim
	if !reserved {
		if !s.setupSlow.allow(auth.RateKey(ip), s.now()) {
			w.Header().Set("Retry-After", strconv.Itoa(int(lockedCheckEvery/time.Second)))
			writeError(w, http.StatusTooManyRequests, "locked")
			return
		}
		claim = s.opt.Setup.ClaimUncounted // a locked address's noise never rotates the code
	}
	cookie, ok, err := claim(body.Token)
	switch {
	case err != nil:
		if reserved {
			s.setupLock.Release(ip) // says nothing about the token
		}
		if errors.Is(err, setup.ErrNotPending) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		s.serverError(w, "setup claim", err)
		return
	case !ok && !reserved:
		w.Header().Set("Retry-After", strconv.Itoa(int(left/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "locked")
		return
	case !ok:
		writeErrorMsg(w, http.StatusForbidden, "bad_token", "that is not the current setup code (`kipple setup-token` shows it)") // the reservation stays counted
		return
	}
	s.setupLock.Clear(ip)
	http.SetCookie(w, &http.Cookie{
		Name: setupCookieName, Value: cookie, Path: setupCookiePath, MaxAge: int(setup.SessionTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.scheme(r) == "https",
	})
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// lockedCheckEvery is how often a locked-out address may still have a setup
// code checked.
const lockedCheckEvery = time.Minute

// maxLockedTracked bounds lockedThrottle (like the lockout's own map).
const maxLockedTracked = 4096

// lockedThrottle spaces the checks of locked-out addresses: without it a
// locked address could still have every guess checked, only uncounted.
type lockedThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// allow reports whether key may have one code checked now, and records it. A
// full table is pruned of entries older than lockedCheckEvery; if it is still
// full the check is refused (fail closed until entries age out).
func (t *lockedThrottle) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if at, ok := t.last[key]; ok && now.Sub(at) < lockedCheckEvery && !now.Before(at) {
		return false
	}
	if _, ok := t.last[key]; !ok && len(t.last) >= maxLockedTracked {
		for k, at := range t.last {
			if now.Sub(at) >= lockedCheckEvery || now.Before(at) {
				delete(t.last, k)
			}
		}
		if len(t.last) >= maxLockedTracked {
			return false
		}
	}
	t.last[key] = now
	return true
}

// setupAccount is POST /api/setup/account: creates the one account and signs
// the browser in. Exactly one of password and passwordless ("access" or
// "open"); open needs acknowledge_open and must pass the open gate itself.
func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	c, err := r.Cookie(setupCookieName)
	if err != nil || !s.opt.Setup.SessionOK(c.Value) {
		writeErrorMsg(w, http.StatusUnauthorized, "setup_session", "enter the setup code first (the setup session is missing or expired)")
		return
	}
	var body struct {
		Username        string  `json:"username"`
		Password        *string `json:"password"`
		Passwordless    *string `json:"passwordless"`
		AcknowledgeOpen bool    `json:"acknowledge_open"`
		OpenLAN         bool    `json:"open_lan"`
	}
	if !decodeJSON(w, r, &body, maxSetupBody, false) {
		return
	}
	if !setup.ValidUsername(body.Username) {
		writeErrorMsg(w, http.StatusBadRequest, "bad_username", "the user name must be 1 to 64 characters of A-Z a-z 0-9 . _ -")
		return
	}
	if (body.Password != nil) == (body.Passwordless != nil) {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", `send exactly one of "password" or "passwordless"`)
		return
	}
	if body.OpenLAN && (body.Passwordless == nil || *body.Passwordless != "open") {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", `"open_lan" goes with "passwordless": "open" only`)
		return
	}
	na := setup.NewAccount{Username: body.Username, AuthMode: store.AuthStandard, CreatedVia: store.CreatedViaWizard}
	switch {
	case body.Password != nil:
		if err := setup.CheckPassword("the password", *body.Password, auth.MinPasswordLen); err != nil {
			writeErrorMsg(w, http.StatusBadRequest, "bad_new_password", err.Error())
			return
		}
		na.Password = *body.Password
	case *body.Passwordless == "open":
		if !body.AcknowledgeOpen {
			writeErrorMsg(w, http.StatusBadRequest, "ack_required", "confirm that anyone who can reach this address can read and change everything")
			return
		}
		if reason := s.gateRefusal(r, s.snapshot(r.Context()), body.OpenLAN, true); reason != "" {
			writeOpenRefused(w, reason)
			return
		}
		na.AuthMode, na.OpenLAN = store.AuthOpen, body.OpenLAN
	case *body.Passwordless == "access":
		// Same rule as design §7.0: an Access-only account is created only by
		// someone for whom Access sign-in demonstrably works on this request.
		switch s.accessProof(r) {
		case proofOK:
		case proofUnavailable:
			w.Header().Set("Retry-After", "5")
			writeErrorMsg(w, http.StatusServiceUnavailable, "access_unavailable",
				"the Cloudflare Access signing keys cannot be loaded right now; try again shortly")
			return
		default:
			writeErrorMsg(w, http.StatusForbidden, "access_required",
				"no password with Cloudflare Access needs Access configured (KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD) and Kipple opened through it")
			return
		}
	default:
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", `passwordless must be "access" or "open"`)
		return
	}
	created, acct, err := setup.CreateAccount(r.Context(), s.db, na)
	if err != nil || !created {
		// Whatever happened, a row that exists ends setup mode here and now: a
		// lost race, or an error reported after the insert committed.
		if a, ok, rerr := s.db.Account(context.WithoutCancel(r.Context())); rerr == nil && ok {
			s.finishSetup(r.Context(), a, false)
		}
		if err != nil {
			s.serverError(w, "setup: create account", err)
			return
		}
		// Lost the race to another token holder: the SPA reloads into sign-in.
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	s.finishSetup(r.Context(), acct, na.OpenLAN)
	mode := setup.DisplayMode(acct)
	s.log.Info("account created", "username", acct.Username, "reader_api", false,
		"created_via", store.CreatedViaWizard, "auth_mode", mode)
	http.SetCookie(w, &http.Cookie{
		Name: setupCookieName, Value: "", Path: setupCookiePath, MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.scheme(r) == "https",
	})
	if !s.startSession(w, r) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": acct.Username, "auth_mode": mode})
}

// finishSetup leaves setup mode once the account row exists: the flag flips,
// the token goes, and every cache that keyed on "no account" is dropped.
// openLAN is whether this request turned security.open_lan on with the account.
func (s *Server) finishSetup(ctx context.Context, acct store.Account, openLAN bool) {
	s.opt.Setup.Finish()
	s.verifier.SetSecret([]byte(acct.Secret))
	s.verifier.ClearMemo()
	if s.opt.OnAPIPasswordChange != nil {
		s.opt.OnAPIPasswordChange()
	}
	s.noteMode(ctx, func(sn *modeSnapshot) {
		sn.mode = acct.AuthMode
		sn.openLAN = sn.openLAN || openLAN
	})
}

// startSession mints a fresh session and sets its cookie, or answers the error
// and reports false.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) bool {
	val, err := newCookieValue()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return false
	}
	now := s.now()
	if err := s.db.CreateSession(r.Context(), sessionID(val), now.Unix(), now.Add(sessionTTL).Unix(), r.UserAgent(), s.clientIP(r)); err != nil {
		s.serverError(w, "create session", err) // 503 maintenance during a search-index rebuild
		return false
	}
	s.setCookie(w, r, val)
	return true
}

// authOpen is POST /api/auth/open: in open mode, a request that passes the open
// gate gets a session, exactly like a sign-in. Not counted against the login
// lockout: there is nothing to guess.
func (s *Server) authOpen(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	acct, ok, err := s.db.Account(r.Context())
	if err != nil {
		s.serverError(w, "auth open", err)
		return
	}
	if !ok || acct.AuthMode != store.AuthOpen {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if reason := s.signInRefusal(r); reason != "" {
		writeOpenRefused(w, reason)
		return
	}
	if !s.startSession(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// onboardingComplete is POST /api/onboarding/complete ("Finish" and "Skip for
// now"): idempotent.
func (s *Server) onboardingComplete(w http.ResponseWriter, r *http.Request) {
	if err := s.db.CompleteOnboarding(r.Context()); err != nil {
		s.serverError(w, "onboarding complete", err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}

// onboardingRestart is POST /api/onboarding/restart ("Run setup again", owner
// decision 4): it clears sys.setup_completed_at and nothing else.
func (s *Server) onboardingRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.db.RestartOnboarding(r.Context()); err != nil {
		s.serverError(w, "onboarding restart", err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
}
