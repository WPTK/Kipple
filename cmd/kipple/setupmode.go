package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// startSetupMode derives the mode from the database (docs/setup-wizard-design.md
// 3.1): with an account the process is in normal mode for good; without one it
// is in setup mode, which is only "no account row" (there is no setup code).
// then runs once, when the account appears: it starts the background work, so
// nothing fetches before an account exists. A `setup-token` file left by a
// Kipple before 0.7 is removed at every start (the code is gone; the step goes
// at 1.0).
func startSetupMode(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger, then func()) (*setup.Manager, error) {
	if err := setup.RemoveStaleTokenFile(cfg.DataDir); err != nil {
		logger.Warn("cannot remove a stale setup token file", "err", err)
	}
	_, exists, err := db.Account(ctx)
	if err != nil {
		return nil, fmt.Errorf("read account: %w", err)
	}
	if exists {
		return nil, nil
	}
	logger.Info("no account yet: open Kipple in a browser to create it (it listens on " + cfg.Addr + "), or set KIPPLE_USERNAME and KIPPLE_PASSWORD and restart")
	return setup.NewPending(then), nil
}

// listenTCP is net.Listen (a seam for tests).
var listenTCP = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// listen binds addr. A failure names the address and KIPPLE_ADDR, the one way
// to choose another (there is no automatic fallback port).
func listen(addr string) (net.Listener, error) {
	ln, err := listenTCP(addr)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on %q (set KIPPLE_ADDR to use another address): %w", addr, err)
	}
	return ln, nil
}

// allowedHosts is the Host gate's configured list: KIPPLE_ALLOWED_HOSTS plus the
// host of KIPPLE_PUBLIC_URL.
func allowedHosts(cfg config.Config) []string {
	out := append([]string(nil), cfg.AllowedHosts...)
	if cfg.PublicURL != "" {
		if u, err := url.Parse(cfg.PublicURL); err == nil {
			if e, err := setup.CheckHostEntry(u.Hostname()); err == nil {
				out = append(out, e)
			}
		}
	}
	return out
}
