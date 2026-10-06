package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// noAccountYet is what the account commands say before the account exists.
const noAccountYet = "no account yet: create it in the browser (open Kipple), or start `kipple serve` once with KIPPLE_USERNAME and KIPPLE_PASSWORD set"

// ensureAccount creates the single account row on first start from
// KIPPLE_USERNAME / KIPPLE_PASSWORD (and KIPPLE_API_PASSWORD when set). An
// existing account is never modified, except that KIPPLE_API_PASSWORD is applied
// when the account has no API password yet (one that fails the length rules is
// then logged as an error and not applied: the server starts with the Reader
// API disabled). Invalid passwords for a new account refuse the start. Without
// credentials configured the account stays absent and the server starts in
// setup mode (the browser wizard creates it).
func ensureAccount(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) error {
	acc, exists, err := db.Account(ctx)
	if err != nil {
		return fmt.Errorf("read account: %w", err)
	}
	if exists {
		// A reset that kept the variables asked to ignore them until an account
		// exists; one does, so the request is spent.
		if err := setup.SetIgnoreEnvAccount(cfg.DataDir, false); err != nil {
			logger.Warn("cannot remove "+setup.NoEnvAccountFile, "err", err)
		}
		if acc.APIPasswordHash == "" && cfg.APIPassword != "" {
			if err := checkEnvPassword("KIPPLE_API_PASSWORD", cfg.APIPassword, auth.MinAPIPasswordLen); err != nil {
				// Usually a leftover variable meeting an account without an API
				// password (a restored older backup, say): refusing to start would
				// take the web UI down too. Say so loudly and keep the API disabled.
				logger.Error("KIPPLE_API_PASSWORD not applied, the Reader API stays disabled: "+err.Error()+
					"; fix or unset it, or run `kipple api-password`", "reader_api", false)
				return nil
			}
			hash, err := auth.HashPassword(cfg.APIPassword)
			if err != nil {
				return err
			}
			if err := db.SetAPIPasswordHash(ctx, hash); err != nil {
				return err
			}
			logger.Info("Reader API password set from KIPPLE_API_PASSWORD")
		}
		warnPasswordless(ctx, db, acc, logger)
		return nil
	}
	if setup.IgnoreEnvAccount(cfg.DataDir) {
		logger.Info("not creating the account from KIPPLE_USERNAME and KIPPLE_PASSWORD: Kipple was reset and told to ignore them until an account exists")
		return nil
	}
	// A new account always gets a web password, Access or not: an empty
	// KIPPLE_PASSWORD is the example file's default, so it must never quietly
	// mean "no password". Removing it later is a deliberate step in Settings,
	// made through a verified Access sign-in (design §7.0). Without both
	// variables the wizard asks (a lone KIPPLE_USERNAME is ignored).
	if cfg.Username == "" || cfg.Password == "" {
		return nil // setup mode: startSetupMode logs the one "no account yet" line
	}
	if err := checkEnvPassword("KIPPLE_PASSWORD", cfg.Password, auth.MinPasswordLen); err != nil {
		return err
	}
	if cfg.APIPassword != "" {
		if err := checkEnvPassword("KIPPLE_API_PASSWORD", cfg.APIPassword, auth.MinAPIPasswordLen); err != nil {
			return err
		}
	}
	created, _, err := setup.CreateAccount(ctx, db, setup.NewAccount{
		Username: cfg.Username, Password: cfg.Password, APIPassword: cfg.APIPassword,
		AuthMode: store.AuthStandard, CreatedVia: store.CreatedViaEnv,
	})
	if err != nil {
		return fmt.Errorf("create account (KIPPLE_USERNAME must be 1-64 characters of A-Z a-z 0-9 . _ -): %w", err)
	}
	if created {
		logger.Info("account created", "username", cfg.Username, "reader_api", cfg.APIPassword != "",
			"created_via", store.CreatedViaEnv, "auth_mode", "password")
		if cfg.APIPassword == "" {
			logger.Info("Reader API is disabled until you run `kipple api-password`")
		}
	}
	return nil
}

// warnPasswordless logs an account without a web password that cannot sign in
// because Cloudflare Access validation is off (the setting is empty): nothing
// else can stand in for the password, so web sign-in is impossible until one
// is set with `kipple password`. Open mode has no password by design. Settings
// refuses to turn Access off under such an account, so this takes a restored
// backup or a hand-edited database.
func warnPasswordless(ctx context.Context, db *store.DB, acc store.Account, logger *slog.Logger) {
	if acc.PasswordHash != "" || acc.AuthMode == store.AuthOpen {
		return
	}
	sec, err := db.SecuritySettings(ctx)
	if err == nil && sec.Access.TeamDomain != "" {
		return
	}
	logger.Warn("the account has no web password and Cloudflare Access validation is off: web sign-in is impossible; set a password with `kipple password`")
}

// checkEnvPassword applies the account endpoints' length rules (and refuses the
// example placeholder) to a password taken from the environment. It is checked
// only when the value is about to be used, so a stale variable left set after
// the account exists never stops a start or a recovery command.
func checkEnvPassword(name, pw string, min int) error { return setup.CheckPassword(name, pw, min) }

// setAPIPassword generates a new Reader API password, stores its hash and
// returns the plain text (shown once). Every signed-in sync client is revoked.
func setAPIPassword(ctx context.Context, db *store.DB) (string, error) {
	if _, ok, err := db.Account(ctx); err != nil {
		return "", err
	} else if !ok {
		return "", errors.New(noAccountYet)
	}
	pw, err := auth.GeneratePassword(24)
	if err != nil {
		return "", err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return "", err
	}
	if err := db.SetAPIPasswordHash(ctx, hash); err != nil {
		return "", err
	}
	return pw, nil
}

// runAPIPassword implements `kipple api-password`: it opens the store like
// import does (safe while serve runs; SQLite WAL serializes the write), sets a
// fresh random API password and prints it once. A running server notices the
// change within a few seconds and rejects the old token.
func runAPIPassword(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: kipple api-password")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := ensureDataDir(cfg.DataDir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(cfg.DataDir, "kipple.db"), Logger: logger, NoMigrate: true, NoCheckpoint: true})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Warn("closing store", "err", err)
		}
	}()
	pw, err := setAPIPassword(ctx, db)
	if err != nil {
		return err
	}
	fmt.Println(pw)
	fmt.Fprintln(os.Stderr, "Reader API password set (shown once above). Existing sync clients are signed out; enter it in your sync apps.")
	return nil
}
