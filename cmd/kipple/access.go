package main

import (
	"context"
	"log/slog"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/config"
)

// accessVerifier builds the Cloudflare Access token verifier (design §7.0), or
// nil when KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD are unset: then no
// request is ever Access-verified and a web password is always required. When
// it is configured the key set is loaded once in the background, so a wrong
// team domain is logged at startup rather than on the first sign-in.
func accessVerifier(cfg config.Config, logger *slog.Logger) (*access.Verifier, error) {
	if !cfg.AccessEnabled() {
		return nil, nil
	}
	v, err := access.New(cfg.AccessTeamDomain, cfg.AccessAUD, access.Options{Logger: logger})
	if err != nil {
		return nil, err
	}
	logger.Info("Cloudflare Access token validation on", "issuer", v.Issuer())
	go func() {
		// Bounded by the verifier's own fetch timeout; a failure is logged there.
		_ = v.Prefetch(context.Background())
	}()
	return v, nil
}
