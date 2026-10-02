package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/store"
)

func openDir(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	return db
}

// Mode derivation: no account is setup mode, an account is normal mode; a stale
// token file of an older Kipple is removed in both; env credentials create the
// row first, a lone KIPPLE_USERNAME does not.
func TestStartSetupModeFollowsTheAccountRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stale := filepath.Join(dir, "setup-token")
	require.NoError(t, os.WriteFile(stale, []byte("ABCD-EFGH-JKMN-PQRS-TVWX-YZ01\n"), 0o600))
	db := openDir(t, dir)
	defer db.Close()
	cfg := config.Config{DataDir: dir, Username: "owner"} // the old example file's default, without a password
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	started := 0
	m, err := startSetupMode(ctx, db, cfg, quiet, func() { started++ })
	require.NoError(t, err)
	require.True(t, m.Pending(), "KIPPLE_USERNAME alone is setup mode")
	require.NoFileExists(t, stale, "a token file left by an older Kipple is removed")
	require.Zero(t, started, "nothing starts before the account exists")
	m.Finish()
	m.Finish()
	require.Equal(t, 1, started, "the background work starts once, when the account appears")

	// A restart with env credentials: the row is created and setup mode is over.
	cfg.Password = "web-pw-123"
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	m, err = startSetupMode(ctx, db, cfg, quiet, func() { t.Fatal("not pending: nothing to wait for") })
	require.NoError(t, err)
	require.Nil(t, m)
	require.False(t, m.Pending())
	pending, err := db.SetupPending(ctx)
	require.NoError(t, err)
	require.False(t, pending, "an env account never sees onboarding")
}

// Restoring a backup that holds the account leaves setup mode on the next
// start; restoring into setup mode's empty database keeps it.
func TestRestoreLeavesSetupMode(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	db := openDir(t, dir)
	m, err := startSetupMode(ctx, db, config.Config{DataDir: dir}, quiet, nil)
	require.NoError(t, err)
	require.True(t, m.Pending())
	require.NoError(t, db.Close())

	zipPath := export(t, newData(t, 3))
	_, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)

	db = openDir(t, dir)
	defer db.Close()
	m, err = startSetupMode(ctx, db, config.Config{DataDir: dir}, quiet, nil)
	require.NoError(t, err)
	require.Nil(t, m, "the restored account ends setup mode")
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

// `kipple setup-token` is a stub until 1.0: it says no code is needed and exits 0.
func TestRunSetupTokenIsAStub(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, runSetupToken(&out))
	require.Contains(t, out.String(), "no setup code")
	require.Contains(t, out.String(), "create your account")
}
