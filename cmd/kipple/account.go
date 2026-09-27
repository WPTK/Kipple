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
	"github.com/WPTK/kipple/internal/store"
)

// ensureAccount creates the single account row on first start from
// KIPPLE_USERNAME / KIPPLE_PASSWORD (and KIPPLE_API_PASSWORD when set). An
// existing account is never modified, except that KIPPLE_API_PASSWORD is applied
// when the account has no API password yet (one that fails the length rules is
// then logged as an error and not applied: the server starts with the Reader
// API disabled). Invalid passwords for a new account refuse the start. Without credentials configured the
// account stays absent and the web login and Reader API stay disabled.
func ensureAccount(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) error {
	acc, exists, err := db.Account(ctx)
	if err != nil {
		return fmt.Errorf("read account: %w", err)
	}
	if exists {
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
		warnPasswordless(acc, cfg, logger)
		return nil
	}
	// A new account always gets a web password, Access or not: an empty
	// KIPPLE_PASSWORD is the example file's default, so it must never quietly
	// mean "no password". Removing it later is a deliberate step in Settings,
	// made through a verified Access sign-in (design §7.0).
	if cfg.Username == "" || cfg.Password == "" {
		logger.Warn("no account yet: set KIPPLE_USERNAME and KIPPLE_PASSWORD; the web login and the Reader API stay disabled")
		return nil
	}
	if err := checkEnvPassword("KIPPLE_PASSWORD", cfg.Password, auth.MinPasswordLen); err != nil {
		return err
	}
	if cfg.APIPassword != "" {
		if err := checkEnvPassword("KIPPLE_API_PASSWORD", cfg.APIPassword, auth.MinAPIPasswordLen); err != nil {
			return err
		}
	}
	pwHash, err := auth.HashPassword(cfg.Password)
	if err != nil {
		return err
	}
	var apiHash string
	if cfg.APIPassword != "" {
		if apiHash, err = auth.HashPassword(cfg.APIPassword); err != nil {
			return err
		}
	}
	secret, err := newAccountSecret()
	if err != nil {
		return err
	}
	created, err := db.CreateAccount(ctx, store.Account{
		Username: cfg.Username, PasswordHash: pwHash, APIPasswordHash: apiHash, Secret: secret,
	})
	if err != nil {
		return fmt.Errorf("create account (KIPPLE_USERNAME must be 1-64 characters of A-Z a-z 0-9 . _ -): %w", err)
	}
	if created {
		logger.Info("account created", "username", cfg.Username, "reader_api", apiHash != "")
		if apiHash == "" {
			logger.Info("Reader API is disabled until you run `kipple api-password`")
		}
	}
	return nil
}

// warnPasswordless logs an account without a web password that cannot sign in
// because Cloudflare Access validation is off (both variables unset): nothing
// else can stand in for the password, so web sign-in is impossible until one
// is set with `kipple password`.
func warnPasswordless(acc store.Account, cfg config.Config, logger *slog.Logger) {
	if acc.PasswordHash == "" && !cfg.AccessEnabled() {
		logger.Warn("the account has no web password and Cloudflare Access validation is off (KIPPLE_ACCESS_TEAM_DOMAIN and KIPPLE_ACCESS_AUD unset): web sign-in is impossible; set a password with `kipple password` or configure Access")
	}
}

// examplePassword is the placeholder an older .env.example shipped; it is
// refused so a copied example never becomes a real password.
const examplePassword = "change-me"

// checkEnvPassword applies the account endpoints' length rules (and refuses the
// example placeholder) to a password taken from the environment. It is checked
// only when the value is about to be used, so a stale variable left set after
// the account exists never stops a start or a recovery command.
func checkEnvPassword(name, pw string, min int) error {
	if pw == examplePassword {
		return fmt.Errorf("%s is the example value %q: choose a real password", name, examplePassword)
	}
	if n := len(pw); n < min || n > auth.MaxPasswordLen {
		return fmt.Errorf("%s must be %d to %d characters (it is %d)", name, min, auth.MaxPasswordLen, n)
	}
	return nil
}

// setAPIPassword generates a new Reader API password, stores its hash and
// returns the plain text (shown once). Every signed-in sync client is revoked.
func setAPIPassword(ctx context.Context, db *store.DB) (string, error) {
	if _, ok, err := db.Account(ctx); err != nil {
		return "", err
	} else if !ok {
		return "", errors.New("no account yet: start `kipple serve` once with KIPPLE_USERNAME and KIPPLE_PASSWORD set")
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
	fmt.Fprintln(os.Stderr, "Reader API password set (shown once above). Existing sync clients are signed out; enter it in Reeder and NetNewsWire.")
	return nil
}
