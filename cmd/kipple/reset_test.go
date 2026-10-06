package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/setup"
)

// resetClient is a signed-in browser stand-in against base.
type resetClient struct {
	t    *testing.T
	base string
	cl   *http.Client
}

func newResetClient(t *testing.T) *resetClient {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	return &resetClient{t: t, cl: &http.Client{Transport: tr, Jar: jar}}
}

func (c *resetClient) call(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader([]byte(body)))
	require.NoError(c.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Kipple-Client", "web")
	resp, err := c.cl.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

const resetTestPassword = "a-long-enough-password"

// callHost is call with another Host header.
func (c *resetClient) callHost(host, method, path string) (int, map[string]any) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.base+path, nil)
	require.NoError(c.t, err)
	req.Host = host
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Kipple-Client", "web")
	resp, err := c.cl.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(c.t, err)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// The whole reset against the real server: sign in, reset, Kipple stops cleanly
// (exit 0), the next start moves the library into backup/pre-restore-* and comes
// up empty, in setup mode, answering at the same address (the server settings
// survive). With KIPPLE_USERNAME and KIPPLE_PASSWORD set it is the same: they are
// ignored until a new account exists, then the request is spent.
func TestRunServeAppliesAReset(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  bool
	}{
		{"no environment account", false},
		{"environment account is ignored until a new account exists", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldH, oldSig, oldListen, oldLog := newWebHandler, stopSignals, listenTCP, slog.Default()
			t.Cleanup(func() { newWebHandler, stopSignals, listenTCP = oldH, oldSig, oldListen; slog.SetDefault(oldLog) })
			newWebHandler = func(func() string) (http.Handler, error) { return http.NotFoundHandler(), nil }
			quiesceHTTP(t)
			serveEnv(t, "127.0.0.1:0")
			dataDir := os.Getenv("KIPPLE_DATA")
			if tc.env {
				t.Setenv("KIPPLE_USERNAME", "envowner")
				t.Setenv("KIPPLE_PASSWORD", resetTestPassword)
			}

			base, stop, done := startServe(t)
			defer stop()
			c := newResetClient(t)
			c.base = base
			if tc.env {
				code, out := c.call("POST", "/api/auth/login", `{"username":"envowner","password":"`+resetTestPassword+`"}`)
				require.Equal(t, http.StatusNoContent, code, out)
			} else {
				code, out := c.call("POST", "/api/setup/account", `{"username":"owner","password":"`+resetTestPassword+`"}`)
				require.Less(t, code, 300, out)
			}
			code, out := c.call("PATCH", "/api/settings", `{"server.public_url":"https://rss.example.test","security.allowed_hosts":["rss.example.test"]}`)
			require.Equal(t, http.StatusOK, code, out)
			code, info := c.call("GET", "/api/reset", "")
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, tc.env, info["env_account"])
			require.Equal(t, true, info["public_address_set"])

			code, out = c.call("POST", "/api/reset", `{"password":"`+resetTestPassword+`","phrase":"reset kipple"}`)
			require.Equal(t, http.StatusAccepted, code, out)
			require.NoError(t, waitDone(t, done), "a confirmed reset is a clean exit")
			require.FileExists(t, filepath.Join(dataDir, backup.MarkerFile))
			require.FileExists(t, filepath.Join(dataDir, backup.StagedFile))
			require.Equal(t, tc.env, setup.IgnoreEnvAccount(dataDir))

			// The restart policy starts Kipple again.
			base, stop, done = startServe(t)
			defer stop()
			c = newResetClient(t)
			c.base = base
			code, inst := c.callHost("rss.example.test", "GET", "/api/instance")
			require.Equal(t, http.StatusOK, code, "the public address still answers in setup mode")
			require.Equal(t, true, inst["setup"])
			code, _ = c.callHost("other.example.test", "GET", "/api/instance")
			require.Equal(t, http.StatusMisdirectedRequest, code, "and the allowed host names are kept, not widened")
			code, _ = c.call("POST", "/api/setup/account", `{"username":"second","password":"`+resetTestPassword+`"}`)
			require.Less(t, code, 300)
			stop()
			require.NoError(t, waitDone(t, done))

			require.NoFileExists(t, filepath.Join(dataDir, backup.MarkerFile))
			require.False(t, setup.IgnoreEnvAccount(dataDir), "an account exists: the request to ignore the variables is spent")
			dirs, err := filepath.Glob(filepath.Join(dataDir, "backup", "pre-restore-*"))
			require.NoError(t, err)
			require.Len(t, dirs, 1, "the old library is kept as a restore keeps it")
			kept, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dirs[0], "kipple.db"))+"?mode=ro")
			require.NoError(t, err)
			defer kept.Close()
			var name string
			require.NoError(t, kept.QueryRow("SELECT username FROM account").Scan(&name))
			want := "owner"
			if tc.env {
				want = "envowner"
			}
			require.Equal(t, want, name)
		})
	}
}

// KIPPLE_USERNAME and KIPPLE_PASSWORD seed the account on first start, unless a
// reset asked to ignore them: then no account is made while the file exists and
// no account does, and the file goes once one exists.
func TestEnsureAccountHonoursTheIgnoreFile(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	dir := t.TempDir()
	cfg := config.Config{Username: "owner", Password: "web-pw", DataDir: dir}

	require.NoError(t, setup.SetIgnoreEnvAccount(dir, true))
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	_, ok, err := db.Account(ctx)
	require.NoError(t, err)
	require.False(t, ok, "ignored while the file exists")
	require.True(t, setup.IgnoreEnvAccount(dir), "still waiting for an account")

	require.NoError(t, setup.SetIgnoreEnvAccount(dir, false))
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	_, ok, _ = db.Account(ctx)
	require.True(t, ok, "without the file the variables seed the account")

	require.NoError(t, setup.SetIgnoreEnvAccount(dir, true))
	require.NoError(t, ensureAccount(ctx, db, cfg, quiet))
	require.False(t, setup.IgnoreEnvAccount(dir), "an account exists: the file is removed")
}

// While the variables are being ignored the setup-mode line does not tell the
// reader to set them.
func TestSetupModeLogIsHonestAboutIgnoredVariables(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		dir := t.TempDir()
		require.NoError(t, setup.SetIgnoreEnvAccount(dir, ignored))
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		_, err := startSetupMode(context.Background(), openDB(t), config.Config{DataDir: dir}, logger, func() {})
		require.NoError(t, err)
		if ignored {
			require.Contains(t, buf.String(), "being ignored")
			require.NotContains(t, buf.String(), "or set KIPPLE_USERNAME")
		} else {
			require.Contains(t, buf.String(), "or set KIPPLE_USERNAME")
		}
	}
}
