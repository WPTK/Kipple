package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
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
	require.NoError(t, ensureAccount(ctx, db, config.Config{APIPassword: "api-pw"}, quiet))
	withAPI, _, _ := db.Account(ctx)
	require.True(t, auth.CheckPassword("api-pw", withAPI.APIPasswordHash))
	require.NoError(t, ensureAccount(ctx, db, config.Config{APIPassword: "different"}, quiet))
	still, _, _ := db.Account(ctx)
	require.Equal(t, withAPI.APIPasswordHash, still.APIPasswordHash)
}

func TestEnsureAccountRejectsBadUsername(t *testing.T) {
	db := openDB(t)
	err := ensureAccount(context.Background(), db, config.Config{Username: "owner smith", Password: "x"}, quiet)
	require.Error(t, err)
}

func TestSetAPIPassword(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	_, err := setAPIPassword(ctx, db)
	require.Error(t, err, "needs an account first")

	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "web-pw", APIPassword: "old"}, quiet))
	before, _, _ := db.Account(ctx)
	pw, err := setAPIPassword(ctx, db)
	require.NoError(t, err)
	require.Len(t, pw, 24)
	after, _, _ := db.Account(ctx)
	require.NotEqual(t, before.APIPasswordHash, after.APIPasswordHash)
	require.True(t, auth.CheckPassword(pw, after.APIPasswordHash))
	require.False(t, auth.CheckPassword("old", after.APIPasswordHash))
	require.Equal(t, before.PasswordHash, after.PasswordHash, "the web password is untouched")
	require.Equal(t, before.Secret, after.Secret)
}
