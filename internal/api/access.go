package api

import (
	"errors"
	"net/http"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// accessProof is what a request can show for an account without a web
// password (design §7.0). It is the one place that rule lives: the login and
// the account endpoints' current-password check (which also gates removing
// the password) ask here.
type accessProof int

const (
	// proofOK: a verified Cloudflare Access token naming a user by email.
	proofOK accessProof = iota
	// proofNotConfigured: Access validation is off, so nothing can stand in
	// for a password (the port may be reachable without Access in front).
	proofNotConfigured
	// proofNone: nothing verifiable was presented: no token, or one naming a
	// key id the team's key set does not have even after a fresh fetch. Not
	// counted as a failed sign-in: it cannot succeed.
	proofNone
	// proofUnavailable: the team's key set cannot be loaded, or the token's
	// key id is not in it yet and a refetch is not due (a rotation), so the
	// token cannot be checked right now. Says nothing about the token:
	// answered 503 and not counted, like a busy password verifier.
	proofUnavailable
	// proofRefused: a token was presented and refused (bad signature, expired,
	// for another application or team, or a service token with no email);
	// counted like a wrong password.
	proofRefused
)

// accessProof verifies the request's Access token, waiting for a key-set fetch
// when needed (a sign-in or an account change; the wait ends with the request).
func (s *Server) accessProof(r *http.Request) accessProof {
	v := s.reach.Access()
	if v == nil {
		return proofNotConfigured
	}
	id, err := v.VerifyRequest(r)
	switch {
	case err == nil && id.Email != "":
		return proofOK
	case err == nil:
		return proofRefused // a service token: a machine, not the owner
	case errors.Is(err, access.ErrNoToken), errors.Is(err, access.ErrUnknownKey):
		return proofNone
	case errors.Is(err, access.ErrNoKeys), errors.Is(err, access.ErrKeyPending):
		// Includes a request that ended while waiting for the fetch: it is gone,
		// and nothing about its token was decided.
		return proofUnavailable
	default:
		return proofRefused
	}
}

// writeProofError answers a proof other than proofOK for an account endpoint
// (403 unless the keys are unavailable) and counts the attempt: only a refused
// token is a failure. removing selects the wording of the Access-off case.
func (s *Server) writeProofError(w http.ResponseWriter, t *try, p accessProof, removing bool) {
	if p == proofRefused {
		t.fail()
	}
	switch p {
	case proofNotConfigured:
		if removing {
			writeErrorMsg(w, http.StatusBadRequest, "access_not_configured",
				"a web password can only be removed when Cloudflare Access validation is on (Settings, Account & Devices, Address and access)")
			return
		}
		writeErrorMsg(w, http.StatusForbidden, "access_not_configured",
			"this account has no web password and Cloudflare Access validation is off: set a password on the host with `kipple password`")
	case proofUnavailable:
		w.Header().Set("Retry-After", "5")
		writeErrorMsg(w, http.StatusServiceUnavailable, "access_unavailable",
			"the Cloudflare Access signing keys cannot be loaded right now; try again shortly")
	default:
		writeErrorMsg(w, http.StatusForbidden, "access_required",
			"open Kipple through Cloudflare Access to do this")
	}
}

// userInfo is the account summary of the bootstrap `user` object and, with
// withEmail, of GET /api/auth/me. access_email (me only) is the email of this
// request's verified Access token, or null. It is for display, so it never
// waits on the network: a token the cached key set cannot verify shows as null
// while a background refresh runs. The bootstrap leaves the email out: that
// response is kept in the offline cache, where a sign-in identity has no
// business, and the display needs it live anyway.
//
// auth_mode is "password", "access" (no web password, Cloudflare Access) or
// "open"; setup_pending is true until onboarding finished or was skipped.
func (s *Server) userInfo(r *http.Request, acct store.Account, withEmail bool) (map[string]any, error) {
	pending, err := s.db.SetupPending(r.Context())
	if err != nil {
		return nil, err
	}
	m := map[string]any{
		"username":       acct.Username,
		"api_enabled":    acct.APIPasswordHash != "",
		"password_set":   acct.PasswordHash != "",
		"access_enabled": s.reach.Access() != nil,
		"auth_mode":      setup.DisplayMode(acct),
		"setup_pending":  pending,
	}
	if withEmail {
		var email any
		if id, err := s.reach.Access().VerifyRequestCached(r); err == nil && id.Email != "" {
			email = id.Email
		}
		m["access_email"] = email
	}
	return m, nil
}

// decoyHash is a real argon2id hash, with the parameters HashPassword uses, of
// a random password that was thrown away when it was made: nobody knows it. A
// password sent to an account without one is checked against it, so that
// attempt costs and counts exactly like a wrong password on an account with
// one (a test keeps its parameters in step with HashPassword). It is a
// constant, so there is no first-use cost and nothing that can fail.
const decoyHash = "$argon2id$v=19$m=19456,t=2,p=1$8GXK7Fmv/IBLsFEZ+pCh3w$nKusdTHMYdD9Ero6pzliMlMxMWEd5+miRvkIbvsdw4A" // gitleaks:allow (a decoy; its password was never kept)
