package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
)

// startServe runs runServe in the background on a free loopback port and
// returns its base URL, the stop signal and its result.
func startServe(t *testing.T) (base string, stop context.CancelFunc, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopSignals = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	lns := make(chan net.Listener, 1)
	listenTCP = func(string) (net.Listener, error) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			lns <- ln
		}
		return ln, err
	}
	ch := make(chan error, 1)
	go func() { ch <- runServe() }()
	select {
	case ln := <-lns:
		return "http://" + ln.Addr().String(), cancel, ch
	case err := <-ch:
		t.Fatalf("runServe returned before listening: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not listen")
	}
	return "", nil, nil
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("runServe did not stop")
	}
	return nil
}

// The whole web restore against the real server: upload and confirm in setup
// mode; the confirm stops Kipple cleanly (exit 0); the next start applies the
// restore before the database opens and comes up in normal mode, signed out.
func TestRunServeAppliesARestoreConfirmedInTheWizard(t *testing.T) {
	oldH, oldSig, oldListen, oldLog := newWebHandler, stopSignals, listenTCP, slog.Default()
	t.Cleanup(func() { newWebHandler, stopSignals, listenTCP = oldH, oldSig, oldListen; slog.SetDefault(oldLog) })
	newWebHandler = func(func() string) (http.Handler, error) { return http.NotFoundHandler(), nil }
	quiesceHTTP(t)
	serveEnv(t, "127.0.0.1:0")
	dataDir := os.Getenv("KIPPLE_DATA")
	zipped, err := os.ReadFile(export(t, newData(t, 4)))
	require.NoError(t, err)

	base, stop, done := startServe(t)
	defer stop()
	tr := &http.Transport{Proxy: nil}
	t.Cleanup(tr.CloseIdleConnections)
	cl := &http.Client{Transport: tr}
	// The page's owner key, sent with every call as the page does.
	key := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	call := func(method, path, ctype string, body []byte) (int, map[string]any) {
		req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		require.NoError(t, err)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-Kipple-Client", "web")
		req.Header.Set("X-Kipple-Restore-Key", key)
		resp, err := cl.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	code, inst := call("GET", "/api/instance", "", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, inst["setup"])
	require.Equal(t, "none", inst["restore"])

	code, up := call("POST", "/api/setup/restore/upload", "application/octet-stream", zipped)
	require.Equal(t, http.StatusAccepted, code, up)
	require.Equal(t, "checking", up["state"])
	var st map[string]any
	require.Eventually(t, func() bool {
		code, st = call("GET", "/api/setup/restore", "", nil)
		return code == http.StatusOK && st["state"] != "checking"
	}, 30*time.Second, 10*time.Millisecond)
	require.Equal(t, "ready", st["state"], st)
	sum := st["summary"].(map[string]any)
	require.Equal(t, "backup", sum["kind"])
	require.Equal(t, "owner", sum["username"])
	require.EqualValues(t, 4, sum["items"])
	_, inst = call("GET", "/api/instance", "", nil)
	require.Equal(t, "ready", inst["restore"])

	code, conf := call("POST", "/api/setup/restore/confirm", "application/json", []byte(`{}`))
	require.Equal(t, http.StatusAccepted, code, conf)
	require.NoError(t, waitDone(t, done), "a confirmed restore is a clean exit")
	require.FileExists(t, filepath.Join(dataDir, backup.MarkerFile))
	require.FileExists(t, filepath.Join(dataDir, backup.StagedFile))
	tr.CloseIdleConnections()

	// The restart policy starts Kipple again.
	base, stop, done = startServe(t)
	defer stop()
	code, inst = call("GET", "/api/instance", "", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, inst["setup"], "the restored account ends setup mode")
	require.Equal(t, "password", inst["auth"])
	code, _ = call("POST", "/api/setup/restore/upload", "application/octet-stream", zipped)
	require.Equal(t, http.StatusUnauthorized, code, "no restore route outside setup mode: an unknown /api/ path")
	stop()
	require.NoError(t, waitDone(t, done))
	tr.CloseIdleConnections()

	require.NoFileExists(t, filepath.Join(dataDir, backup.MarkerFile))
	require.NoFileExists(t, filepath.Join(dataDir, backup.StagedFile))
	require.Equal(t, 4, countItems(t, dataDir))
	require.Equal(t, 0, countSessions(t, dataDir), "restore signs every session out")
	dirs, err := filepath.Glob(filepath.Join(dataDir, "backup", "pre-restore-*"))
	require.NoError(t, err)
	require.Empty(t, dirs, "the empty database of setup mode holds nothing, so no safety copy is kept")
}

// `kipple restore` and the web restore share one swap, and the CLI, the newer
// decision, drops a web restore still waiting to be applied.
func TestCLIRestoreDropsAWaitingWebRestore(t *testing.T) {
	dir := newData(t, 2)
	zipPath := export(t, newData(t, 5))
	require.NoError(t, os.WriteFile(filepath.Join(dir, backup.StagedFile), []byte("staged"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, backup.MarkerFile), []byte("{}"), 0o600))
	out, err := doRestore(dir, zipPath, true)
	require.NoError(t, err)
	require.Contains(t, out, "waiting to be applied")
	require.NoFileExists(t, filepath.Join(dir, backup.MarkerFile))
	require.NoFileExists(t, filepath.Join(dir, backup.StagedFile))
	require.Equal(t, 5, countItems(t, dir))
	require.NotContains(t, strings.ToLower(out), "schema", "the restore summary names no schema numbers")
}
