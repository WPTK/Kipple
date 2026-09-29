package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
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

func TestServeAddr(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&logs, nil))
	db := openDir(t, t.TempDir())
	defer db.Close()

	addr, fallback, err := serveAddr(ctx, db, config.Config{Addr: config.DefaultAddr}, lg)
	require.NoError(t, err)
	require.Equal(t, ":1919", addr)
	require.True(t, fallback)

	// KIPPLE_ADDR always wins, and never falls back.
	addr, fallback, err = serveAddr(ctx, db, config.Config{Addr: ":7080", AddrSet: true}, lg)
	require.NoError(t, err)
	require.Equal(t, ":7080", addr)
	require.False(t, fallback)

	// A database that had an account before 0.5 keeps 7080 with a warning.
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('sys.legacy_port', 'true')`)
		return err
	}))
	addr, fallback, err = serveAddr(ctx, db, config.Config{Addr: config.DefaultAddr}, lg)
	require.NoError(t, err)
	require.Equal(t, ":7080", addr)
	require.False(t, fallback)
	require.Contains(t, logs.String(), "port 7080 is the pre-0.5 default")
	addr, _, err = serveAddr(ctx, db, config.Config{Addr: "127.0.0.1:9000", AddrSet: true}, lg)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9000", addr, "an explicit address beats the shim")
}

func TestListenFallsBackOnlyWhenTheDefaultIsTaken(t *testing.T) {
	old := listenTCP
	t.Cleanup(func() { listenTCP = old })
	var tried []string
	inUse := &net.OpError{Op: "listen", Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
	listenTCP = func(addr string) (net.Listener, error) {
		tried = append(tried, addr)
		if addr == config.DefaultAddr {
			return nil, inUse
		}
		return net.Listen("tcp", "127.0.0.1:0")
	}
	var logs bytes.Buffer
	ln, err := listen(config.DefaultAddr, true, slog.New(slog.NewTextHandler(&logs, nil)))
	require.NoError(t, err)
	_ = ln.Close()
	require.Equal(t, []string{":1919", ":1138"}, tried)
	require.Contains(t, logs.String(), "listening on 1138 instead")

	tried = nil
	_, err = listen(config.DefaultAddr, false, quiet)
	require.Error(t, err, "no fallback for an explicit or legacy address")
	require.Equal(t, []string{":1919"}, tried)

	tried = nil
	listenTCP = func(addr string) (net.Listener, error) {
		tried = append(tried, addr)
		return nil, errors.New("permission denied")
	}
	_, err = listen(config.DefaultAddr, true, quiet)
	require.Error(t, err)
	require.Equal(t, []string{":1919"}, tried, "only EADDRINUSE falls back")

	require.True(t, isAddrInUse(inUse))
	require.True(t, isAddrInUse(syscall.Errno(10048)), "WSAEADDRINUSE")
	require.False(t, isAddrInUse(errors.New("x")))
}

// A real taken port is recognized on this OS.
func TestIsAddrInUseForReal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	_, err = net.Listen("tcp", ln.Addr().String())
	require.Error(t, err)
	require.True(t, isAddrInUse(err), err.Error())
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

// A set TZ that differs from the zone chosen in Kipple is warned about at start.
func TestWarnTZOverride(t *testing.T) {
	ctx := context.Background()
	db := openDir(t, t.TempDir())
	defer db.Close()
	var logs bytes.Buffer
	lg := slog.New(slog.NewTextHandler(&logs, nil))
	warnTZOverride(ctx, db, config.Config{TZ: "America/Chicago"}, lg)
	require.Empty(t, logs.String(), "nothing chosen in Kipple")
	require.NoError(t, db.SetSettings(ctx, map[string]any{"tz": "America/Chicago"}))
	warnTZOverride(ctx, db, config.Config{TZ: "America/Chicago"}, lg)
	require.Empty(t, logs.String(), "the same zone")
	warnTZOverride(ctx, db, config.Config{}, lg)
	require.Empty(t, logs.String(), "TZ unset")
	require.NoError(t, db.SetSettings(ctx, map[string]any{"tz": "Europe/Paris"}))
	warnTZOverride(ctx, db, config.Config{TZ: "America/New_York"}, lg)
	require.Contains(t, logs.String(), "TZ overrides the time zone chosen in Kipple")
	require.Contains(t, logs.String(), "Europe/Paris")
}

// The listen port belongs to the installation, not to the backup: restoring
// keeps what the live database had (or a fresh directory's 1919).
func TestRestoreKeepsTheInstallationsPort(t *testing.T) {
	ctx := context.Background()
	setLegacy := func(dir string, on bool) {
		db := openDir(t, dir)
		defer db.Close()
		v := "false"
		if on {
			v = "true"
		}
		require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('sys.legacy_port', ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v)
			return err
		}))
	}
	addrOf := func(dir string) string {
		db := openDir(t, dir)
		defer db.Close()
		addr, _, err := serveAddr(ctx, db, config.Config{Addr: config.DefaultAddr}, quiet)
		require.NoError(t, err)
		return addr
	}

	// A legacy 7080 installation restores a backup made by a new one: still 7080.
	live := newData(t, 1)
	setLegacy(live, true)
	_, err := doRestore(live, export(t, newData(t, 2)), true)
	require.NoError(t, err)
	require.Equal(t, ":7080", addrOf(live))

	// A new 1919 installation restores a legacy backup: still 1919.
	src := newData(t, 2)
	setLegacy(src, true)
	legacyZip := export(t, src)
	current := newData(t, 1)
	out, err := doRestore(current, legacyZip, true)
	require.NoError(t, err)
	require.Equal(t, ":1919", addrOf(current))
	require.NotContains(t, out, "old default port")

	// No live database (a rebuilt host, a new volume): the backup keeps its own
	// port, and restore says so.
	fresh := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(fresh, 0o700))
	out, err = doRestore(fresh, legacyZip, true)
	require.NoError(t, err)
	require.Equal(t, ":7080", addrOf(fresh))
	require.Contains(t, out, "old default port")

	// A live database that was never set up (the container started once on a
	// rebuilt host) is no installation to follow: the backup keeps its own.
	unset := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.MkdirAll(unset, 0o700))
	openDir(t, unset).Close()
	out, err = doRestore(unset, legacyZip, true)
	require.NoError(t, err)
	require.Equal(t, ":7080", addrOf(unset))
	require.Contains(t, out, "old default port")

	// A live database too broken to read does not stop the restore.
	broken := newData(t, 1)
	require.NoError(t, os.WriteFile(filepath.Join(broken, "kipple.db"), []byte("not a database at all, just junk bytes"), 0o600))
	out, err = doRestore(broken, export(t, newData(t, 2)), true)
	require.NoError(t, err, out)
	require.Contains(t, out, "could not be read for its port setting")
	require.Equal(t, ":1919", addrOf(broken))
}
