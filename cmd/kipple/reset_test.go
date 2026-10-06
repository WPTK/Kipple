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

// The whole reset against the real server: sign in, reset, Kipple stops cleanly
// (exit 0), the next start moves the database into backup/pre-restore-* and comes
// up empty, in setup mode (or, when KIPPLE_USERNAME and KIPPLE_PASSWORD are set
// and not ignored, with the account they describe).
func TestRunServeAppliesAReset(t *testing.T) {
	for _, tc := range []struct {
		name      string
		env       bool
		ignore    bool
		wantSetup bool
	}{
		{"no environment account", false, false, true},
		{"environment account ignored until a new account exists", true, true, true},
		{"environment account kept", true, false, false},
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
			code, info := c.call("GET", "/api/reset", "")
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, tc.env, info["env_account"])

			ignore := "false"
			if tc.ignore {
				ignore = "true"
			}
			code, out := c.call("POST", "/api/reset", `{"password":"`+resetTestPassword+`","phrase":"reset kipple","ignore_env_account":`+ignore+`}`)
			require.Equal(t, http.StatusAccepted, code, out)
			require.NoError(t, waitDone(t, done), "a confirmed reset is a clean exit")
			require.FileExists(t, filepath.Join(dataDir, backup.MarkerFile))
			require.FileExists(t, filepath.Join(dataDir, "kipple.db"), "the library stays until the next start moves it")
			require.Equal(t, tc.ignore, setup.IgnoreEnvAccount(dataDir))

			// The restart policy starts Kipple again.
			base, stop, done = startServe(t)
			defer stop()
			c = newResetClient(t)
			c.base = base
			code, inst := c.call("GET", "/api/instance", "")
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, tc.wantSetup, inst["setup"])
			if tc.wantSetup {
				code, _ = c.call("POST", "/api/setup/account", `{"username":"second","password":"`+resetTestPassword+`"}`)
				require.Less(t, code, 300)
			}
			stop()
			require.NoError(t, waitDone(t, done))

			require.NoFileExists(t, filepath.Join(dataDir, backup.MarkerFile))
			require.False(t, setup.IgnoreEnvAccount(dataDir), "an account exists: the request to ignore the variables is spent")
			dirs, err := filepath.Glob(filepath.Join(dataDir, "backup", "pre-restore-*"))
			require.NoError(t, err)
			require.Len(t, dirs, 1, "the old database is kept as a restore keeps it")
			require.FileExists(t, filepath.Join(dirs[0], "kipple.db"))
			// The kept database is the old library, with the first account.
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
