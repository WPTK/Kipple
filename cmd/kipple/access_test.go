package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
)

func TestEnsureAccountPasswordlessOnlyWithAccess(t *testing.T) {
	ctx := context.Background()

	// Access off: an empty KIPPLE_PASSWORD creates nothing.
	db := openDB(t)
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner"}, quiet))
	_, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.False(t, ok, "no passwordless account without Access validation")

	// Access on: the account is created without a web password.
	db = openDB(t)
	cfg := config.Config{Username: "owner", AccessTeamDomain: "myteam.cloudflareaccess.com", AccessAUD: "aud"}
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	acc, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, acc.PasswordHash)

	// A later start with Access switched off warns that web sign-in is impossible.
	var buf bytes.Buffer
	loud := slog.New(slog.NewTextHandler(&buf, nil))
	require.NoError(t, ensureAccount(ctx, db, config.Config{}, loud))
	require.Contains(t, buf.String(), "no web password")
	buf.Reset()
	require.NoError(t, ensureAccount(ctx, db, cfg, loud))
	require.NotContains(t, buf.String(), "no web password")
}

func TestAccessVerifierOffWhenUnset(t *testing.T) {
	v, err := accessVerifier(config.Config{}, quiet)
	require.NoError(t, err)
	require.Nil(t, v)
}
