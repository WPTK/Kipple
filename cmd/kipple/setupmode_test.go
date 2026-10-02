package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

func openDir(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	return db
}

// Mode derivation: no account is setup mode (a token file), an account is
// normal mode (a stale token file removed); env credentials create the row
// first, a lone KIPPLE_USERNAME does not.
func TestStartSetupModeFollowsTheAccountRow(t *testing.T) {
	ctx := context.Background()
	var banner bytes.Buffer
	old := setupOut
	t.Cleanup(func() { setupOut = old })
	setupOut = &banner

	dir := t.TempDir()
	db := openDir(t, dir)
	defer db.Close()
	cfg := config.Config{DataDir: dir, Username: "owner"} // the old example file's default, without a password
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	m, err := startSetupMode(ctx, db, cfg, quiet)
	require.NoError(t, err)
	require.True(t, m.Pending(), "KIPPLE_USERNAME alone is setup mode")
	tok, ok, err := setup.ReadToken(dir)
	require.NoError(t, err)
	require.True(t, ok)
	m.Announce("1919")
	require.Contains(t, banner.String(), tok)

	// A restart with env credentials: the row is created and setup mode is over.
	cfg.Password = "web-pw-123"
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	m, err = startSetupMode(ctx, db, cfg, quiet)
	require.NoError(t, err)
	require.Nil(t, m)
	require.False(t, m.Pending())
	_, ok, _ = setup.ReadToken(dir)
	require.False(t, ok, "the stale token file is removed on a normal start")
	pending, err := db.SetupPending(ctx)
	require.NoError(t, err)
	require.False(t, pending, "an env account never sees onboarding")
}

// Restoring a backup that holds the account leaves setup mode on the next
// start; restoring into setup mode's empty database keeps it.
func TestRestoreLeavesSetupMode(t *testing.T) {
	ctx := context.Background()
	old := setupOut
	t.Cleanup(func() { setupOut = old })
	setupOut = io.Discard

	dir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	db := openDir(t, dir)
	m, err := startSetupMode(ctx, db, config.Config{DataDir: dir}, quiet)
	require.NoError(t, err)
	require.True(t, m.Pending())
	require.NoError(t, db.Close())
	require.FileExists(t, setup.TokenPath(dir))

	zipPath := export(t, newData(t, 3))
	_, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)

	db = openDir(t, dir)
	defer db.Close()
	m, err = startSetupMode(ctx, db, config.Config{DataDir: dir}, quiet)
	require.NoError(t, err)
	require.Nil(t, m, "the restored account ends setup mode")
	require.NoFileExists(t, setup.TokenPath(dir))
}

// The setup token never travels in a backup, even when the file sits next to
// the database.
func TestBackupNeverCarriesTheSetupToken(t *testing.T) {
	dir := newData(t, 1)
	require.NoError(t, os.WriteFile(setup.TokenPath(dir), []byte("ABCD-EFGH-JKMN-PQRS-TVWX-YZ01\n"), 0o600))
	zr, err := zip.OpenReader(export(t, dir))
	require.NoError(t, err)
	defer zr.Close()
	require.NotEmpty(t, zr.File)
	for _, f := range zr.File {
		require.NotContains(t, f.Name, "setup-token")
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		require.NoError(t, err)
		require.NotContains(t, string(b), "ABCD-EFGH-JKMN-PQRS-TVWX-YZ01", f.Name)
	}
}

// A failed bind says which address and how to change it; there is no fallback
// port.
func TestListenFailureNamesTheAddressAndTheVariable(t *testing.T) {
	old := listenTCP
	t.Cleanup(func() { listenTCP = old })
	var tried []string
	inUse := &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	listenTCP = func(addr string) (net.Listener, error) {
		tried = append(tried, addr)
		return nil, inUse
	}
	_, err := listen(config.DefaultAddr)
	require.Error(t, err)
	require.Equal(t, []string{":1919"}, tried, "no second port is tried")
	require.Contains(t, err.Error(), ":1919")
	require.Contains(t, err.Error(), "KIPPLE_ADDR")
	require.ErrorIs(t, err, inUse)
}

func TestAllowedHostsIncludesThePublicURL(t *testing.T) {
	require.Equal(t, []string{"a.example.com", "rss.example.com"},
		allowedHosts(config.Config{AllowedHosts: []string{"a.example.com"}, PublicURL: "https://RSS.example.com/kipple"}))
	require.Nil(t, allowedHosts(config.Config{}))
}

func TestRunSetupToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIPPLE_DATA", dir)
	stdout := captureStdout(t, func() { require.NoError(t, runSetupToken(nil)) })
	require.Empty(t, stdout, "nothing pending")
	m := setup.New(setup.Options{DataDir: dir, Logger: quiet})
	require.NoError(t, m.Begin())
	tok, _, _ := setup.ReadToken(dir)
	stdout = captureStdout(t, func() { require.NoError(t, runSetupToken(nil)) })
	require.Equal(t, tok, strings.TrimSpace(stdout))
	require.Error(t, runSetupToken([]string{"x"}))
	m.Finish()
	stdout = captureStdout(t, func() { require.NoError(t, runSetupToken(nil)) })
	require.Empty(t, stdout)
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	require.NoError(t, w.Close())
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}
