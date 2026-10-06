package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

func newResetHarness(t *testing.T, env bool) (*harness, string, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	restarts := &atomic.Int32{}
	h := newHarness(t, realVerifier, func(o *Options) {
		o.DataDir = dir
		o.Version = "1.0.0"
		o.EnvAccount = env
		o.Restart = func() { restarts.Add(1) }
	})
	return h, dir, restarts
}

func resetBody(pw, phrase string, ignore bool) string {
	return jsonStr(map[string]any{"password": pw, "phrase": phrase, "ignore_env_account": ignore})
}

func TestResetInfoReportsTheEnvironmentAccount(t *testing.T) {
	for _, env := range []bool{false, true} {
		h, _, _ := newResetHarness(t, env)
		code, out, _ := h.api(h.login(), "GET", "/api/reset", "")
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, env, out["env_account"])
	}
	h, _, _ := newResetHarness(t, false)
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/reset", "").Code, "signed in only")
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/reset", resetBody(testPass, resetPhrase, false)).Code)
}

func TestResetConfirms(t *testing.T) {
	t.Run("writes the marker and restarts", func(t *testing.T) {
		h, dir, restarts := newResetHarness(t, false)
		code, out, _ := h.api(h.login(), "POST", "/api/reset", resetBody(testPass, "  Reset KIPPLE ", false))
		require.Equal(t, http.StatusAccepted, code, out)
		require.Equal(t, true, out["restarting"])
		require.EqualValues(t, 30, out["estimate_seconds"])
		require.True(t, backup.MarkerPending(dir))
		require.EqualValues(t, 1, restarts.Load())
		require.False(t, setup.IgnoreEnvAccount(dir))
		// The next start applies it.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kipple.db"), []byte("x"), 0o600))
		done, err := backup.ApplyStaged(dir, h.clk.Now(), nil)
		require.NoError(t, err)
		require.True(t, done.Reset)
		require.Equal(t, testUser, done.Username)
	})
	for _, tc := range []struct {
		name, body string
		want       int
		kind       string
	}{
		{"wrong phrase", resetBody(testPass, "reset", false), http.StatusBadRequest, "bad_phrase"},
		{"no phrase", resetBody(testPass, "", false), http.StatusBadRequest, "bad_phrase"},
		{"wrong password", resetBody("nope-nope-nope", resetPhrase, false), http.StatusForbidden, "bad_password"},
		{"no password", resetBody("", resetPhrase, false), http.StatusForbidden, "bad_password"},
		{"bad json", `{`, http.StatusBadRequest, "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dir, restarts := newResetHarness(t, true)
			code, out, _ := h.api(h.login(), "POST", "/api/reset", tc.body)
			require.Equal(t, tc.want, code)
			require.Equal(t, tc.kind, out["error"])
			require.False(t, backup.MarkerPending(dir))
			require.False(t, setup.IgnoreEnvAccount(dir))
			require.Zero(t, restarts.Load())
		})
	}
	t.Run("an account without a password needs the same proof as changing it", func(t *testing.T) {
		h, dir, restarts := newResetHarness(t, false)
		me := h.login()
		require.NoError(t, h.db.SetPasswordHash(t.Context(), "", store.AuthStandard, ""))
		code, _, _ := h.api(me, "POST", "/api/reset", resetBody("", resetPhrase, false))
		require.NotEqual(t, http.StatusAccepted, code, "no Access sign-in to stand in for the password")
		require.False(t, backup.MarkerPending(dir))
		require.Zero(t, restarts.Load())
	})
	t.Run("409 while a restore is waiting", func(t *testing.T) {
		h, dir, restarts := newResetHarness(t, false)
		require.NoError(t, os.WriteFile(filepath.Join(dir, backup.MarkerFile), []byte(`{"kind":"restore"}`), 0o600))
		code, out, _ := h.api(h.login(), "POST", "/api/reset", resetBody(testPass, resetPhrase, false))
		require.Equal(t, http.StatusConflict, code)
		require.Equal(t, "restore_pending", out["error"])
		require.Zero(t, restarts.Load())
	})
	t.Run("a second reset is refused", func(t *testing.T) {
		h, _, restarts := newResetHarness(t, false)
		me := h.login()
		code, _, _ := h.api(me, "POST", "/api/reset", resetBody(testPass, resetPhrase, false))
		require.Equal(t, http.StatusAccepted, code)
		code, out, _ := h.api(me, "POST", "/api/reset", resetBody(testPass, resetPhrase, false))
		require.Equal(t, http.StatusConflict, code)
		require.Equal(t, "restore_pending", out["error"])
		require.EqualValues(t, 1, restarts.Load())
	})
}

func TestResetEnvironmentAccountChoice(t *testing.T) {
	t.Run("ignore writes the file", func(t *testing.T) {
		h, dir, _ := newResetHarness(t, true)
		code, _, _ := h.api(h.login(), "POST", "/api/reset", resetBody(testPass, resetPhrase, true))
		require.Equal(t, http.StatusAccepted, code)
		require.True(t, setup.IgnoreEnvAccount(dir))
	})
	t.Run("not ignoring removes an earlier request", func(t *testing.T) {
		h, dir, _ := newResetHarness(t, true)
		require.NoError(t, setup.SetIgnoreEnvAccount(dir, true))
		code, _, _ := h.api(h.login(), "POST", "/api/reset", resetBody(testPass, resetPhrase, false))
		require.Equal(t, http.StatusAccepted, code)
		require.False(t, setup.IgnoreEnvAccount(dir))
	})
	t.Run("without the variables there is nothing to ignore", func(t *testing.T) {
		h, dir, _ := newResetHarness(t, false)
		code, _, _ := h.api(h.login(), "POST", "/api/reset", resetBody(testPass, resetPhrase, true))
		require.Equal(t, http.StatusAccepted, code)
		require.False(t, setup.IgnoreEnvAccount(dir))
	})
}

func TestResetIsNotOfferedInSetupMode(t *testing.T) {
	h := newSetupHarness(t)
	for _, m := range []string{"GET", "POST"} {
		code := h.req(m, "/api/reset", "{}").Code
		require.Contains(t, []int{http.StatusUnauthorized, http.StatusNotFound}, code, "setup mode has nothing to reset")
	}
}
