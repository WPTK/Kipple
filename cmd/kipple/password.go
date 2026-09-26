package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/store"
)

// newAccountSecret is the 32 random bytes (hex) that key Reader tokens, the
// login memo and image signatures.
func newAccountSecret() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return hex.EncodeToString(secret), nil
}

// resetPassword sets the web password, signs out every web session and revokes
// every Reader API token (by rotating the account secret; the Reader API
// password itself is kept). A running server notices within a few seconds.
func resetPassword(ctx context.Context, db *store.DB, pw string) error {
	if n := len(pw); n < auth.MinPasswordLen || n > auth.MaxPasswordLen {
		return fmt.Errorf("the password must be %d to %d characters", auth.MinPasswordLen, auth.MaxPasswordLen)
	}
	if _, ok, err := db.Account(ctx); err != nil {
		return err
	} else if !ok {
		return errors.New("no account yet: start `kipple serve` once with KIPPLE_USERNAME and KIPPLE_PASSWORD set")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	secret, err := newAccountSecret()
	if err != nil {
		return err
	}
	return db.ResetPassword(ctx, hash, secret)
}

// readPasswordStdin reads one line (the trailing newline is not part of the
// password; nothing else is trimmed).
func readPasswordStdin(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", errors.New("--stdin: no password on standard input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptPassword asks twice on the terminal without echo.
var promptPassword = func() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("standard input is not a terminal: use `kipple password --stdin` (with Docker: `docker exec -i kipple /kipple password --stdin`) or `docker exec -it`")
	}
	fmt.Fprint(os.Stderr, "New web password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat it: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("the two entries differ; nothing changed")
	}
	return string(a), nil
}

// runPassword implements `kipple password [--stdin]`: the recovery path for a
// forgotten web password. Like api-password it is safe while serve runs.
func runPassword(args []string) error {
	fromStdin := false
	for _, a := range args {
		if a != "--stdin" {
			return errors.New("usage: kipple password [--stdin]")
		}
		fromStdin = true
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var pw string
	if fromStdin {
		pw, err = readPasswordStdin(os.Stdin)
	} else {
		pw, err = promptPassword()
	}
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
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
	if err := resetPassword(ctx, db, pw); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Web password changed. Every web session was signed out and every Reader API token revoked:")
	fmt.Fprintln(os.Stderr, "sign in again in the browser, and re-enter the Reader API password in Reeder and NetNewsWire.")
	fmt.Fprintln(os.Stderr, "If KIPPLE_PASSWORD is still set in the environment, remove it: it is only read when the account is first created.")
	return nil
}
