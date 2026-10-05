package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

const maxSetupBody = 4 << 10

// registerSetup mounts the one setup route. It exists only in a process that
// started without an account; the handler also checks the one-way flag, so once
// the account exists it answers 404 like any unknown /api/ route.
func (s *Server) registerSetup(mux *http.ServeMux) {
	if !s.opt.Setup.Pending() {
		return
	}
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

// instance is GET /api/instance: the facts the signed-out app needs to pick its
// first screen. No version, no user name. While Kipple has no account it also
// says what the account form can offer from where this browser is: whether
// Cloudflare Access sign-in works here, and whether open mode would (reason is
// the open gate's answer as things are, null when it would let this browser in).
func (s *Server) instance(w http.ResponseWriter, r *http.Request) {
	if s.opt.Setup.Pending() {
		snap := s.snapshot(r.Context())
		orNull := func(reason string) any {
			if reason == "" {
				return nil
			}
			return reason
		}
		openReason, _ := s.wizardOpenRefusal(r, snap, false)
		writeJSON(w, http.StatusOK, map[string]any{
			"setup": true, "auth": nil,
			"access": map[string]bool{
				"enabled":  s.opt.Access != nil,
				"verified": s.opt.Access != nil && s.accessProof(r) == proofOK,
			},
			"open": map[string]any{
				"reason": orNull(openReason),
			},
		})
		return
	}
	var authMode any
	acct, ok, err := s.db.Account(r.Context())
	if err != nil {
		s.serverError(w, "instance", err)
		return
	}
	if ok {
		authMode = setup.DisplayMode(acct)
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup": false, "auth": authMode})
}

// setupAccount is POST /api/setup/account: creates the one account, if there is
// none yet, and signs the browser in. There is no secret: an unclaimed Kipple is
// "no account row", and whoever inserts that row first (the insert has exactly
// one winner) owns it. The guards are the Host gate (421, in front of every
// handler), same-origin plus X-Kipple-Client, and one claim at a time. Exactly one of password and passwordless ("access" or
// "open"); open needs acknowledge_open and must pass the open gate itself.
func (s *Server) setupAccount(w http.ResponseWriter, r *http.Request) {
	if s.setupGone(w) {
		return
	}
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "origin")
		return
	}
	var body struct {
		Username        string  `json:"username"`
		Password        *string `json:"password"`
		Passwordless    *string `json:"passwordless"`
		AcknowledgeOpen bool    `json:"acknowledge_open"`
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
		reason, remember := s.wizardOpenRefusal(r, s.snapshot(r.Context()), true)
		if reason != "" {
			writeOpenRefused(w, reason)
			return
		}
		na.AuthMode, na.AllowHost = store.AuthOpen, remember
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
	// One claim is hashed and inserted at a time: the hash is the expensive part,
	// so a flood of claims costs one hash, not one per request: the first
	// creates the account and every claim queued behind it finds setup over and
	// answers 409 at once. No per-address counting (behind a shared gateway that
	// would lock the owner out, #156).
	select {
	case s.setupSlot <- struct{}{}:
		defer func() { <-s.setupSlot }()
	case <-r.Context().Done():
		return // the client went away; nobody reads an answer
	}
	if !s.opt.Setup.Pending() { // the slot's previous holder won
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	created, acct, err := setup.CreateAccount(r.Context(), s.db, na)
	if err != nil || !created {
		// Whatever happened, a row that exists ends setup mode here and now: a
		// lost race, or an error reported after the insert committed.
		if a, ok, rerr := s.db.Account(context.WithoutCancel(r.Context())); rerr == nil && ok {
			s.finishSetup(r.Context(), a, "")
		}
		if errors.Is(err, store.ErrAllowedHostsFull) {
			writeErrorMsg(w, http.StatusConflict, "allowed_hosts_full", fmt.Sprintf(
				"open mode needs %q in Allowed host names, which already has %d names: open Kipple by its IP address or localhost, or remove a name from KIPPLE_ALLOWED_HOSTS or security.allowed_hosts",
				na.AllowHost, store.MaxAllowedHosts))
			return
		}
		if err != nil {
			s.serverError(w, "setup: create account", err)
			return
		}
		// Lost the race to another claimer: no session; the SPA reloads into sign-in.
		writeErrorMsg(w, http.StatusConflict, "already_set_up", "Kipple was set up a moment ago; sign in instead")
		return
	}
	s.finishSetup(r.Context(), acct, na.AllowHost)
	mode := setup.DisplayMode(acct)
	s.log.Info("account created", "username", acct.Username, "reader_api", false,
		"created_via", store.CreatedViaWizard, "auth_mode", mode, "client", s.clientIP(r))
	if !s.startSession(w, r) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": acct.Username, "auth_mode": mode})
}

// finishSetup leaves setup mode once the account row exists: the flag flips (and
// the background work starts), and every cache that keyed on "no account" is
// dropped. allowed ("" = none) is the name the account's transaction added to
// security.allowed_hosts, carried into the cached snapshot even if re-reading
// it fails.
func (s *Server) finishSetup(ctx context.Context, acct store.Account, allowed string) {
	s.opt.Setup.Finish()
	s.verifier.SetSecret([]byte(acct.Secret))
	s.verifier.ClearMemo()
	if s.opt.OnAPIPasswordChange != nil {
		s.opt.OnAPIPasswordChange()
	}
	s.noteMode(ctx, func(sn *modeSnapshot) {
		sn.mode = acct.AuthMode
		if allowed != "" {
			sn.allowed = append(sn.allowed, allowed)
		}
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
	if !ok {
		writeErrorMsg(w, http.StatusConflict, "setup_required", "Kipple has no account yet; create it first")
		return
	}
	if acct.AuthMode != store.AuthOpen {
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
