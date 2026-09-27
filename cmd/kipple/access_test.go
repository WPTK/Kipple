package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
)

func TestEnsureAccountNeverPasswordlessOnFirstStart(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{Username: "owner", AccessTeamDomain: "myteam.cloudflareaccess.com", AccessAUD: "aud"}

	// An empty KIPPLE_PASSWORD (the example file's default) creates nothing,
	// with or without Access: removing the password is a deliberate later step.
	for _, c := range []config.Config{{Username: "owner"}, cfg} {
		db := openDB(t)
		require.NoError(t, ensureAccount(ctx, db, c, quiet))
		_, ok, err := db.Account(ctx)
		require.NoError(t, err)
		require.False(t, ok)
	}

	// An account whose password was removed later (Settings, through Access).
	db := openDB(t)
	require.NoError(t, ensureAccount(ctx, db, config.Config{Username: "owner", Password: "web-pw"}, quiet))
	require.NoError(t, db.SetPasswordHash(ctx, "", ""))

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
