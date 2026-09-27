package api

import (
	"net/http"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/store"
)

// accessProof is what a request can show for an account without a web
// password (design §7.0). It is the one place that rule lives: the login, the
// account endpoints' current-password check and the password removal all ask
// here.
type accessProof int

const (
	// proofOK: a verified Cloudflare Access token naming a user by email.
	proofOK accessProof = iota
	// proofNotConfigured: Access validation is off, so nothing can stand in
	// for a password (the port may be reachable without Access in front).
	proofNotConfigured
	// proofNoToken: the request carries no token; nothing was presented, so
	// nothing is counted against the login lockout.
	proofNoToken
	// proofRefused: a token was presented and refused (forged, expired, for
	// another application or team, or a service token with no email); counted
	// like a wrong password.
	proofRefused
)

// accessProof verifies the request's Access token, waiting for a key-set fetch
// when needed (a sign-in or an account change).
func (s *Server) accessProof(r *http.Request) accessProof {
	if s.opt.Access == nil {
		return proofNotConfigured
	}
	if r.Header.Get(access.Header) == "" {
		return proofNoToken
	}
	id, err := s.opt.Access.VerifyRequest(r)
	if err != nil || id.Email == "" {
		return proofRefused
	}
	return proofOK
}

// userInfo is the account summary of GET /api/auth/me and the bootstrap `user`
// object. access_email is the email of this request's verified Access token,
// null without one. It is for display, so it never waits on the network: a
// token the cached key set cannot verify shows as null while a background
// refresh runs.
func (s *Server) userInfo(r *http.Request, acct store.Account) map[string]any {
	var email any
	if id, err := s.opt.Access.VerifyRequestCached(r); err == nil && id.Email != "" {
		email = id.Email
	}
	return map[string]any{
		"username":       acct.Username,
		"api_enabled":    acct.APIPasswordHash != "",
		"password_set":   acct.PasswordHash != "",
		"access_enabled": s.opt.Access != nil,
		"access_email":   email,
	}
}
