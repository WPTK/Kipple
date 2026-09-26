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
// when the account has no API password yet. Without credentials configured the
// account stays absent and the web login and Reader API stay disabled.
func ensureAccount(ctx context.Context, db *store.DB, cfg config.Config, logger *slog.Logger) error {
	acc, exists, err := db.Account(ctx)
	if err != nil {
		return fmt.Errorf("read account: %w", err)
	}
	if exists {
		if acc.APIPasswordHash == "" && cfg.APIPassword != "" {
			hash, err := auth.HashPassword(cfg.APIPassword)
			if err != nil {
				return err
			}
			if err := db.SetAPIPasswordHash(ctx, hash); err != nil {
				return err
			}
			logger.Info("Reader API password set from KIPPLE_API_PASSWORD")
		}
		return nil
	}
	if cfg.Username == "" || cfg.Password == "" {
		logger.Warn("no account yet: set KIPPLE_USERNAME and KIPPLE_PASSWORD; the web login and the Reader API stay disabled")
		return nil
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
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
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
