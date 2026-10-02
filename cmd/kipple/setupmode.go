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
	"syscall"

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

// serveAddr is the address serve listens on and whether the 1138 fallback
// applies (docs/setup-wizard-design.md 8.3). KIPPLE_ADDR always wins; unset, it
// is 1919 with the 1138 fallback. Since 0.6.0 nothing else decides it (the pre-0.5
// default 7080 is no longer kept for old databases).
func serveAddr(cfg config.Config) (addr string, fallback bool) {
	if cfg.AddrSet {
		return cfg.Addr, false
	}
	return config.DefaultAddr, true
}

// listenTCP is net.Listen (a seam for tests).
var listenTCP = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }

// listen binds addr; with fallback (KIPPLE_ADDR unset) a
// taken default port moves to config.FallbackAddr with a warning.
func listen(addr string, fallback bool, logger *slog.Logger) (net.Listener, error) {
	ln, err := listenTCP(addr)
	if err != nil && fallback && isAddrInUse(err) {
		logger.Warn("port 1919 is in use; listening on 1138 instead (set KIPPLE_ADDR to choose the port)", "addr", config.FallbackAddr)
		return listenTCP(config.FallbackAddr)
	}
	return ln, err
}

// isAddrInUse reports EADDRINUSE (WSAEADDRINUSE, 10048, on Windows).
func isAddrInUse(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EADDRINUSE || errno == 10048
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

// warnTZOverride says so at every start when TZ is set and differs from a time
// zone chosen in Kipple: TZ now governs statistics and the nightly job too
// (before 0.5 it only set the log zone), so an old TZ line left in an .env
// would otherwise move them silently.
func warnTZOverride(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) {
	if cfg.TZ == "" {
		return
	}
	stored, ok, err := store.StoredZoneName(ctx, db.Reader())
	if err != nil || !ok || stored == cfg.TZ {
		return
	}
	logger.Warn("TZ overrides the time zone chosen in Kipple for statistics and the nightly job; unset TZ to use the chosen one",
		"TZ", cfg.TZ, "tz_setting", stored)
}
