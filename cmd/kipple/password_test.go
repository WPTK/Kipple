package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/store"
)

func openLive(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog, NoMigrate: true, NoCheckpoint: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestResetPasswordRevokesSessionsAndReaderTokens(t *testing.T) {
	dir := newData(t, 1)
	db := openLive(t, dir)
	ctx := context.Background()
	require.NoError(t, db.SetAPIPasswordHash(ctx, "api-hash"))
	before, _, err := db.Account(ctx)
	require.NoError(t, err)

	require.NoError(t, resetPassword(ctx, db, "a brand new pass"))

	after, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, auth.CheckPassword("a brand new pass", after.PasswordHash), "the new password verifies")
	require.NotEqual(t, before.PasswordHash, after.PasswordHash)
	require.NotEqual(t, before.Secret, after.Secret, "the secret rotates, so every Reader token is revoked")
	require.Len(t, after.Secret, 64)
	require.Equal(t, "api-hash", after.APIPasswordHash, "the Reader API password itself is kept")
	require.Equal(t, before.Username, after.Username)

	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM sessions").Scan(&n))
	require.Zero(t, n, "every web session is signed out")
}

func TestResetPasswordLengthLimits(t *testing.T) {
	dir := newData(t, 1)
	db := openLive(t, dir)
	ctx := context.Background()
	before, _, _ := db.Account(ctx)
	for _, pw := range []string{"", "1234", strings.Repeat("x", 257)} {
		require.ErrorContains(t, resetPassword(ctx, db, pw), "5 to 256", "%d chars", len(pw))
	}
	after, _, _ := db.Account(ctx)
	require.Equal(t, before, after, "a refused password changes nothing")
	require.NoError(t, resetPassword(ctx, db, "12345"))
	require.NoError(t, resetPassword(ctx, db, strings.Repeat("x", 256)))
}

func TestResetPasswordNeedsAnAccount(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.ErrorContains(t, resetPassword(context.Background(), db, "long enough"), "no account yet")
}

func TestReadPasswordStdin(t *testing.T) {
	for in, want := range map[string]string{"hunter22\n": "hunter22", "hunter22\r\n": "hunter22", "no newline": "no newline", " keeps spaces \n": " keeps spaces "} {
		got, err := readPasswordStdin(strings.NewReader(in))
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	_, err := readPasswordStdin(strings.NewReader(""))
	require.Error(t, err)
}

func TestRunPasswordWhileAnotherConnectionIsOpen(t *testing.T) {
	dir := newData(t, 1)
	t.Setenv("KIPPLE_DATA", dir)
	server := openLive(t, dir) // an open connection, as serve would have

	old := promptPassword
	t.Cleanup(func() { promptPassword = old })
	promptPassword = func() (string, error) { return "prompted password", nil }
	require.NoError(t, runPassword(nil))

	acc, _, err := server.Account(context.Background())
	require.NoError(t, err)
	require.True(t, auth.CheckPassword("prompted password", acc.PasswordHash))

	require.ErrorContains(t, runPassword([]string{"--bogus"}), "usage")
}
