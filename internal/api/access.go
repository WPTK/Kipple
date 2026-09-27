package api

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/store"
)

// accessWarnEvery is the minimum gap between "token present but refused" warnings.
const accessWarnEvery = time.Hour

var accessWarn struct {
	mu   sync.Mutex
	last time.Time
}

// accessIdentity verifies the request's Cloudflare Access token (design §7.0).
// ok is true only when Access validation is configured, the token verifies
// (signature, iss, aud, exp, nbf) and it names a user by email; a service
// token has no email and never counts as a person signing in. With Access not
// configured it is always false.
func (s *Server) accessIdentity(r *http.Request) (access.Identity, bool) {
	if s.opt.Access == nil || r.Header.Get(access.Header) == "" {
		return access.Identity{}, false
	}
	id, err := s.opt.Access.VerifyRequest(r)
	if err != nil {
		// A refused token is either someone trying one on (the header is just
		// text) or a misconfigured AUD or team domain; warn now and then so the
		// second case is visible without letting the first flood the log.
		lvl := errors.Is(err, access.ErrIssuer) || errors.Is(err, access.ErrAudience) || errors.Is(err, access.ErrNoKeys)
		accessWarn.mu.Lock()
		now := s.now()
		warn := lvl && (accessWarn.last.IsZero() || now.Sub(accessWarn.last) >= accessWarnEvery)
		if warn {
			accessWarn.last = now
		}
		accessWarn.mu.Unlock()
		if warn {
			s.log.Warn("Cloudflare Access token refused; if this keeps happening, check KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD", "err", err, "ip", s.clientIP(r))
		} else {
			s.log.Debug("Cloudflare Access token refused", "err", err, "ip", s.clientIP(r))
		}
		return access.Identity{}, false
	}
	if id.Email == "" {
		return access.Identity{}, false
	}
	return id, true
}

// userInfo is the account summary of GET /api/auth/me and the bootstrap `user`
// object. access_email is the email of this request's verified Access token,
// null without one.
func (s *Server) userInfo(r *http.Request, acct store.Account) map[string]any {
	var email any
	if id, ok := s.accessIdentity(r); ok {
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
