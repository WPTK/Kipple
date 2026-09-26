package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestEnsureAccountCreatesOnceAndNeverModifies(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)

	// No credentials configured: nothing is created.
	require.NoError(t, ensureAccount(ctx, db, config.Config{}, quiet))
	_, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.False(t, ok)

	cfg := config.Config{Username: "owner", Password: "web-pw"}
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	acc, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "owner", acc.Username)
	require.True(t, auth.CheckPassword("web-pw", acc.PasswordHash))
	require.Empty(t, acc.APIPasswordHash, "the Reader API stays disabled without an API password")
	require.Len(t, acc.Secret, 64)

	// A second start with different env values changes nothing.
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "other", Password: "x"}, quiet))
	again, _, _ := db.Account(ctx)
	require.Equal(t, acc, again)

	// KIPPLE_API_PASSWORD fills in a missing API password, once.
	require.NoError(t, ensureAccount(ctx, db, config.Config{APIPassword: "initial-api-passphrase"}, quiet))
	withAPI, _, _ := db.Account(ctx)
	require.True(t, auth.CheckPassword("initial-api-passphrase", withAPI.APIPasswordHash))
	require.NoError(t, ensureAccount(ctx, db, config.Config{APIPassword: "a-different-api-passphrase"}, quiet))
	still, _, _ := db.Account(ctx)
	require.Equal(t, withAPI.APIPasswordHash, still.APIPasswordHash)
}

func TestEnsureAccountRejectsBadUsername(t *testing.T) {
	db := openDB(t)
	err := ensureAccount(context.Background(), db, config.Config{Username: "owner smith", Password: "web-pw"}, quiet)
	require.Error(t, err)
}

func TestSetAPIPassword(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	_, err := setAPIPassword(ctx, db)
	require.Error(t, err, "needs an account first")

	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "web-pw", APIPassword: "old-api-passphrase"}, quiet))
	before, _, _ := db.Account(ctx)
	pw, err := setAPIPassword(ctx, db)
	require.NoError(t, err)
	require.Len(t, pw, 24)
	after, _, _ := db.Account(ctx)
	require.NotEqual(t, before.APIPasswordHash, after.APIPasswordHash)
	require.True(t, auth.CheckPassword(pw, after.APIPasswordHash))
	require.False(t, auth.CheckPassword("old-api-passphrase", after.APIPasswordHash))
	require.Equal(t, before.PasswordHash, after.PasswordHash, "the web password is untouched")
	require.Equal(t, before.Secret, after.Secret)
}

func TestEnsureAccountValidatesEnvPasswords(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"example placeholder", config.Config{Username: "owner", Password: "change-me"}, "KIPPLE_PASSWORD is the example value"},
		{"web too short", config.Config{Username: "owner", Password: "four"}, "KIPPLE_PASSWORD must be 5 to 256"},
		{"web too long", config.Config{Username: "owner", Password: strings.Repeat("x", 257)}, "KIPPLE_PASSWORD must be 5 to 256"},
		{"api too short", config.Config{Username: "owner", Password: "web-pw", APIPassword: "fifteen-chars-x"}, "KIPPLE_API_PASSWORD must be 16 to 256"},
		{"api placeholder", config.Config{Username: "owner", Password: "web-pw", APIPassword: "change-me"}, "KIPPLE_API_PASSWORD is the example value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			err := ensureAccount(ctx, db, tc.cfg, quiet)
			require.ErrorContains(t, err, tc.want)
			_, ok, _ := db.Account(ctx)
			require.False(t, ok, "nothing is created")
		})
	}

	// An existing account without an API password (a restored older backup) and a
	// leftover short KIPPLE_API_PASSWORD: the start goes on with the API disabled,
	// and says so loudly, instead of refusing (which took the web UI down too).
	db := openDB(t)
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "web-pw"}, quiet))
	var buf bytes.Buffer
	loud := slog.New(slog.NewTextHandler(&buf, nil))
	require.NoError(t, ensureAccount(ctx, db, config.Config{APIPassword: "short"}, loud))
	require.Contains(t, buf.String(), "level=ERROR")
	require.Contains(t, buf.String(), "KIPPLE_API_PASSWORD must be 16")
	require.Contains(t, buf.String(), "Reader API stays disabled")
	acc, _, _ := db.Account(ctx)
	require.Empty(t, acc.APIPasswordHash, "not applied")
	// ...but a stale KIPPLE_PASSWORD, which is never read again, does not stop a start.
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "change-me"}, quiet))
}
