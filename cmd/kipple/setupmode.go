package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// setupOut receives the setup banner (a seam for tests).
var setupOut io.Writer = os.Stderr

// startSetupMode derives the mode from the database (docs/setup-wizard-design.md
// 3.1): with an account the process is in normal mode for good and a setup
// token file left by an earlier run is removed; without one it enters setup
// mode (a fresh token, its file, and the banner once the port is known).
func startSetupMode(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) (*setup.Manager, error) {
	_, exists, err := db.Account(ctx)
	if err != nil {
		return nil, fmt.Errorf("read account: %w", err)
	}
	if exists {
		if err := setup.RemoveTokenFile(cfg.DataDir); err != nil {
			logger.Warn("cannot remove a stale setup token file", "err", err)
		}
		return nil, nil
	}
	m := setup.New(setup.Options{DataDir: cfg.DataDir, Out: setupOut, Logger: logger})
	if err := m.Begin(); err != nil {
		return nil, err
	}
	return m, nil
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

// runSetupToken implements `kipple setup-token`: it prints the pending setup
// code again (for logs that rotated away), or says there is none.
func runSetupToken(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: kipple setup-token")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tok, ok, err := setup.ReadToken(cfg.DataDir)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no setup pending: Kipple already has an account, or `kipple serve` has not started yet")
		return nil
	}
	fmt.Println(tok)
	return nil
}
