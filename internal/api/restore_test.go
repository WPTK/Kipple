package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/store"
)

// restoreHarness is a setup-mode server with a restore on its own data
// directory; restarts counts the shutdowns a confirm asked for.
type restoreHarness struct {
	*setupHarness
	dir      string
	restarts *atomic.Int32
}

func newRestoreHarness(t *testing.T, tune ...func(*backup.RestorerOptions)) *restoreHarness {
	t.Helper()
	dir := t.TempDir()
	ro := backup.RestorerOptions{DataDir: dir, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }}
	for _, f := range tune {
		f(&ro)
	}
	restarts := &atomic.Int32{}
	h := newSetupHarness(t, func(o *Options) {
		o.DataDir = dir
		o.Restore = backup.NewRestorer(ro)
		o.Restart = func() { restarts.Add(1) }
	})
	return &restoreHarness{setupHarness: h, dir: dir, restarts: restarts}
}

// backupZip exports a real backup of a library whose account has the given
// password hash and sign-in mode.
func backupZip(t *testing.T, hash, mode string) []byte {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db")})
	require.NoError(t, err)
	defer db.Close()
	_, err = db.CreateAccount(context.Background(), store.Account{Username: "restored", PasswordHash: hash, AuthMode: mode,
		Secret: strings.Repeat("ab", 32)})
	require.NoError(t, err)
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host, title) VALUES ('http://a.test/rss', 'a.test/rss', 'a.test', 'A')`)
		return err
	}))
	m := backup.New(backup.Options{DB: db, Version: "0.8.0", FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
	defer m.Close()
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	defer d.Close()
	b, err := io.ReadAll(d.File)
	require.NoError(t, err)
	return b
}

// upload posts b. A zip is answered 202 {"state":"checking"} and checked in
// the background: upload then follows GET /api/setup/restore and returns the
// summary (with "status": 200) once ready, or the error object (with
// "status": "failed") if the check refused it. Anything else is the upload's
// own answer with its status.
func (h *restoreHarness) upload(b []byte, mod ...func(*http.Request)) map[string]any {
	h.t.Helper()
	rec := h.req("POST", "/api/setup/restore/upload", string(b), append([]func(*http.Request){hdr("Content-Type", "application/octet-stream")}, mod...)...)
	out := decode(h.t, rec)
	if rec.Code != http.StatusAccepted {
		out["status"] = float64(rec.Code)
		return out
	}
	require.Equal(h.t, map[string]any{"state": "checking"}, out)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		st := h.status(mod...)
		switch st["state"] {
		case "uploading", "checking":
			require.Nil(h.t, st["summary"])
			continue
		case "ready":
			require.Nil(h.t, st["error"])
			sum := st["summary"].(map[string]any)
			require.Equal(h.t, sum["estimate_seconds"], st["estimate_seconds"])
			sum["status"] = float64(http.StatusOK)
			return sum
		case "failed":
			require.Nil(h.t, st["summary"])
			e := st["error"].(map[string]any)
			return map[string]any{"status": "failed", "error": e["code"], "message": e["message"]}
		default:
			h.t.Fatalf("the check ended in %v", st)
		}
	}
	h.t.Fatal("the check did not finish")
	return nil
}

func (h *restoreHarness) status(mod ...func(*http.Request)) map[string]any {
	h.t.Helper()
	rec := h.req("GET", "/api/setup/restore", "", mod...)
	require.Equal(h.t, http.StatusOK, rec.Code, rec.Body.String())
	return decode(h.t, rec)
}

func (h *restoreHarness) restoreState() any {
	return decode(h.t, h.req("GET", "/api/instance", ""))["restore"]
}

func TestRestoreEverything(t *testing.T) {
	h := newRestoreHarness(t)
	require.Equal(t, "none", h.restoreState())
	require.Equal(t, map[string]any{"state": "none", "summary": nil, "error": nil, "estimate_seconds": float64(0)}, h.status())

	out := h.upload(backupZip(t, "h", store.AuthStandard))
	require.EqualValues(t, http.StatusOK, out["status"], out)
	delete(out, "created_at")
	require.Equal(t, map[string]any{"status": float64(200), "kind": "backup", "kipple_version": "0.8.0", "feeds": float64(1), "items": float64(0),
		"starred": float64(0), "username": "restored", "password_state": "password", "needs_new_password": false,
		"new_password_reason": "", "estimate_seconds": float64(30)}, out)
	require.Equal(t, "ready", h.restoreState())

	rec := h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"restarting":true,"estimate_seconds":30}`, rec.Body.String())
	require.EqualValues(t, 1, h.restarts.Load(), "the confirm shuts Kipple down")
	require.Equal(t, "confirmed", h.restoreState())
	st := h.status()
	require.Equal(t, "confirmed", st["state"])
	require.Nil(t, st["summary"])
	require.Equal(t, float64(30), st["estimate_seconds"], "the waiting page can still read the estimate")
	require.FileExists(t, filepath.Join(h.dir, backup.MarkerFile))

	// While it waits, nothing else may happen.
	rec = h.createAccount(map[string]any{"username": "reader", "password": setupPass})
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "restore_pending", decode(t, rec)["error"])
	require.EqualValues(t, http.StatusConflict, h.upload(backupZip(t, "h", store.AuthStandard))["status"])
	require.Equal(t, http.StatusConflict, h.req("DELETE", "/api/setup/restore", "").Code)
	require.Equal(t, http.StatusConflict, h.req("POST", "/api/setup/restore/confirm", `{}`).Code)
	require.EqualValues(t, 1, h.restarts.Load())
	_, ok, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.False(t, ok, "the running database is never touched")
}

func TestRestoreNeedsANewPassword(t *testing.T) {
	// No password, signed in through Cloudflare Access, which is not set up here.
	h := newRestoreHarness(t)
	out := h.upload(backupZip(t, "", store.AuthStandard))
	require.Equal(t, "none_access", out["password_state"])
	require.Equal(t, true, out["needs_new_password"])
	require.Equal(t, "access_unavailable", out["new_password_reason"])

	rec := h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "password_required", decode(t, rec)["error"])
	rec = h.req("POST", "/api/setup/restore/confirm", `{"new_password":"ab"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "bad_new_password", decode(t, rec)["error"])
	require.Zero(t, h.restarts.Load())

	rec = h.req("POST", "/api/setup/restore/confirm", `{"new_password":"`+setupPass+`"}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	staged, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(h.dir, backup.StagedFile)))
	require.NoError(t, err)
	defer staged.Close()
	var hash string
	require.NoError(t, staged.QueryRow("SELECT password_hash FROM account").Scan(&hash))
	require.True(t, strings.HasPrefix(hash, "$argon2id$"), hash)

	// Open mode: fine from this computer, not through a proxy.
	h = newRestoreHarness(t)
	out = h.upload(backupZip(t, "", store.AuthOpen))
	require.Equal(t, "open", out["password_state"])
	require.Equal(t, false, out["needs_new_password"])
	require.NoError(t, h.Restore().Cancel())
	out = h.upload(backupZip(t, "", store.AuthOpen), hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, true, out["needs_new_password"])
	require.Equal(t, "open_refused", out["new_password_reason"])
	rec = h.req("POST", "/api/setup/restore/confirm", `{}`, hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "open_refused", decode(t, rec)["reason"])
	// A password set on an open account switches it to standard sign-in.
	rec = h.req("POST", "/api/setup/restore/confirm", `{"new_password":"`+setupPass+`"}`, hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	staged2, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(h.dir, backup.StagedFile)))
	require.NoError(t, err)
	defer staged2.Close()
	var mode string
	require.NoError(t, staged2.QueryRow("SELECT auth_mode FROM account").Scan(&mode))
	require.Equal(t, store.AuthStandard, mode)
}

// Restore returns the harness's Restorer.
func (h *restoreHarness) Restore() *backup.Restorer { return h.srv.restore }

func TestRestoreUploadKindsAndErrors(t *testing.T) {
	h := newRestoreHarness(t)
	out := h.upload([]byte(`<opml version="2.0"><body><outline text="A" xmlUrl="http://a.test/rss"/></body></opml>`))
	require.Equal(t, map[string]any{"status": float64(200), "kind": "opml", "feeds": float64(1)}, out)
	require.Equal(t, "none", h.restoreState(), "an OPML file is not kept")

	out = h.upload([]byte("just some text"))
	require.EqualValues(t, http.StatusBadRequest, out["status"])
	require.Equal(t, "not_a_backup", out["error"])
	require.Equal(t, "This is not a Kipple backup zip or an OPML file.", out["message"])

	rec := h.req("POST", "/api/setup/restore/upload", "abc", func(r *http.Request) { r.ContentLength = -1 })
	require.Equal(t, http.StatusLengthRequired, rec.Code)

	// A damaged zip: accepted as a zip, refused by the check.
	b := backupZip(t, "h", store.AuthStandard)
	out = h.upload(b[:len(b)/2])
	require.Equal(t, "failed", out["status"])
	require.Equal(t, "bad_backup", out["error"])
	require.Contains(t, out["message"], "This backup cannot be restored")
	rec = h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "no_upload", decode(t, rec)["error"], "only a checked backup can be confirmed")
	require.Equal(t, http.StatusNoContent, h.req("DELETE", "/api/setup/restore", "").Code, "a failed check is cleared")
	require.Equal(t, "none", h.restoreState())

	// The guards of the setup routes.
	rec = h.req("POST", "/api/setup/restore/upload", string(b), hdr("X-Kipple-Client", ""))
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = h.req("POST", "/api/setup/restore/upload", string(b), hdr("Sec-Fetch-Site", "cross-site"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, http.StatusMisdirectedRequest, h.req("POST", "/api/setup/restore/upload", string(b), host("evil.example:1919")).Code)

	// Two uploads: the second is busy until the first is cancelled.
	require.EqualValues(t, http.StatusOK, h.upload(b)["status"])
	rec = h.req("POST", "/api/setup/restore/upload", string(b))
	require.Equal(t, http.StatusConflict, rec.Code)
	out = decode(t, rec)
	require.Equal(t, "restore_busy", out["error"])
	require.Equal(t, http.StatusNoContent, h.req("DELETE", "/api/setup/restore", "").Code)
	require.Equal(t, http.StatusNoContent, h.req("DELETE", "/api/setup/restore", "").Code, "idempotent")
	require.Equal(t, "none", h.restoreState())
	rec = h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "no_upload", decode(t, rec)["error"])

	// Too large, and no room.
	h = newRestoreHarness(t, func(o *backup.RestorerOptions) { o.MaxBytes = 100 })
	rec = h.req("POST", "/api/setup/restore/upload", string(b))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, "too_large", decode(t, rec)["error"])
	require.Equal(t, "close", rec.Header().Get("Connection"), "refused before the body was read: the connection closes after the answer")
	h = newRestoreHarness(t, func(o *backup.RestorerOptions) {
		o.FreeBytes = func(string) (uint64, error) { return 1 << 20, nil }
	})
	// A zip by its first bytes, larger than what was read to tell.
	big := "PK\x03\x04" + strings.Repeat("x", 200<<10)
	rec = h.req("POST", "/api/setup/restore/upload", big)
	require.Equal(t, http.StatusInsufficientStorage, rec.Code)
	out = decode(t, rec)
	require.Equal(t, "no_space", out["error"])
	require.Contains(t, out["message"], "it needs about")
	require.Equal(t, "close", rec.Header().Get("Connection"))
}

func TestRestoreFeedsOnly(t *testing.T) {
	h := newRestoreHarness(t)
	rec := h.req("GET", "/api/setup/restore/feeds", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "no_upload", decode(t, rec)["error"])

	require.EqualValues(t, http.StatusOK, h.upload(backupZip(t, "h", store.AuthStandard))["status"], "uploaded and checked")
	rec = h.req("GET", "/api/setup/restore/feeds", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/x-opml; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Body.String(), "http://a.test/rss")
	require.Equal(t, "none", h.restoreState(), "the upload is discarded")
	require.NoFileExists(t, filepath.Join(h.dir, backup.StagedFile))
	require.Equal(t, http.StatusNotFound, h.req("GET", "/api/setup/restore/feeds", "").Code)
}

// The account form and a restore exclude each other: a claim after an upload
// wins and drops the upload; afterwards every restore route is gone.
func TestRestoreLosesToAnAccountClaim(t *testing.T) {
	h := newRestoreHarness(t)
	require.EqualValues(t, http.StatusOK, h.upload(backupZip(t, "h", store.AuthStandard))["status"], "uploaded and checked")
	rec := h.createAccount(map[string]any{"username": "reader", "password": setupPass})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, backup.RestoreNone, h.Restore().State())
	_, err := os.Stat(filepath.Join(h.dir, backup.StagedFile))
	require.True(t, os.IsNotExist(err), "the staged copy is deleted")

	for _, r := range []struct{ method, path string }{
		{"POST", "/api/setup/restore/upload"}, {"POST", "/api/setup/restore/confirm"},
		{"GET", "/api/setup/restore/feeds"}, {"DELETE", "/api/setup/restore"},
	} {
		require.Equal(t, http.StatusNotFound, h.req(r.method, r.path, "").Code, r.path)
	}
	require.Zero(t, h.restarts.Load())
	require.NoFileExists(t, filepath.Join(h.dir, backup.MarkerFile))
}

// A confirm that finds the account already there (claimed between the upload
// and the confirm, through another process's database) refuses cleanly.
func TestRestoreConfirmRechecksTheAccountRow(t *testing.T) {
	h := newRestoreHarness(t)
	require.EqualValues(t, http.StatusOK, h.upload(backupZip(t, "h", store.AuthStandard))["status"], "uploaded and checked")
	_, err := h.db.CreateAccount(context.Background(), store.Account{Username: "other", PasswordHash: "x", Secret: strings.Repeat("cd", 32)})
	require.NoError(t, err)
	rec := h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "already_set_up", decode(t, rec)["error"])
	require.Zero(t, h.restarts.Load())
	require.NoFileExists(t, filepath.Join(h.dir, backup.MarkerFile))
}

// A confirm and an account claim sent at the same moment: exactly one wins,
// and the loser changes nothing.
func TestRestoreConfirmRacesAnAccountClaim(t *testing.T) {
	zipped := backupZip(t, "h", store.AuthStandard)
	for i := 0; i < 8; i++ {
		h := newRestoreHarness(t)
		require.EqualValues(t, http.StatusOK, h.upload(zipped)["status"])
		codes := make(chan [2]int, 2)
		go func() {
			codes <- [2]int{0, h.req("POST", "/api/setup/restore/confirm", `{}`).Code}
		}()
		go func() {
			codes <- [2]int{1, h.createAccount(map[string]any{"username": "reader", "password": setupPass}).Code}
		}()
		got := map[int]int{}
		for j := 0; j < 2; j++ {
			c := <-codes
			got[c[0]] = c[1]
		}
		_, account, err := h.db.Account(context.Background())
		require.NoError(t, err)
		_, statErr := os.Stat(filepath.Join(h.dir, backup.MarkerFile))
		marker := statErr == nil
		switch {
		case got[0] == http.StatusAccepted:
			require.Equal(t, http.StatusConflict, got[1], "the claim after a confirm: restore_pending")
			require.False(t, account)
			require.True(t, marker)
			require.EqualValues(t, 1, h.restarts.Load())
		case got[1] == http.StatusCreated:
			require.Contains(t, []int{http.StatusNotFound, http.StatusConflict}, got[0], "the confirm after a claim")
			require.True(t, account)
			require.False(t, marker)
			require.Zero(t, h.restarts.Load())
		default:
			t.Fatalf("nobody won: %v", got)
		}
	}
}

// An account claim while a backup is being checked cancels the check: the
// upload is gone and the restore routes with it.
func TestAccountClaimCancelsTheCheck(t *testing.T) {
	h := newRestoreHarness(t)
	rec := h.req("POST", "/api/setup/restore/upload", string(backupZip(t, "h", store.AuthStandard)))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, http.StatusCreated, h.createAccount(map[string]any{"username": "reader", "password": setupPass}).Code)
	require.Equal(t, backup.RestoreNone, h.Restore().State())
	h.Restore().Close() // waits for the cancelled check
	require.NoFileExists(t, filepath.Join(h.dir, backup.StagedFile))
	require.NoFileExists(t, filepath.Join(h.dir, backup.UploadFile))
}

// A second tab sees an upload while its body arrives, and a DELETE from there
// stops it; the first tab is told it was cancelled.
func TestSecondTabSeesAndCancelsAnUpload(t *testing.T) {
	h := newRestoreHarness(t)
	b := backupZip(t, "h", store.AuthStandard)
	pr, pw := io.Pipe()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/setup/restore/upload", pr)
		r.ContentLength = int64(len(b))
		r.Host, r.RemoteAddr = setupHost, setupPeer
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-Kipple-Client", "web")
		rec := httptest.NewRecorder()
		h.root.ServeHTTP(rec, r)
		done <- rec
	}()
	_, err := pw.Write(b[:100])
	require.NoError(t, err)
	require.Equal(t, "uploading", h.status()["state"])
	require.Equal(t, "uploading", h.restoreState())
	go func() { _, _ = pw.Write(b[100:]); _ = pw.Close() }()
	require.Equal(t, http.StatusNoContent, h.req("DELETE", "/api/setup/restore", "").Code)
	rec := <-done
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "restore_cancelled", decode(t, rec)["error"])
	require.Equal(t, "none", h.status()["state"])
}
