package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	key      string // the wizard page's owner key, sent with every request
}

// req is setupHarness.req from the wizard's page: it sends the page's owner
// key with every request. A mod runs last, so stranger or anotherBrowser
// replaces it.
func (h *restoreHarness) req(method, path, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.setupHarness.req(method, path, body, append([]func(*http.Request){hdr(restoreKeyHeader, h.key)}, mod...)...)
}

// testKey is an owner key as a page makes one: 32 random bytes, unpadded
// base64url.
func testKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

// anotherBrowser is a page with its own owner key.
func anotherBrowser(key string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(restoreKeyHeader, key) }
}

// stranger is a request with no owner key at all.
func stranger(r *http.Request) { r.Header.Del(restoreKeyHeader) }

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
	return &restoreHarness{setupHarness: h, dir: dir, restarts: restarts, key: testKey(t)}
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
	t.Parallel()
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
	require.Equal(t, "restored", st["summary"].(map[string]any)["username"], "the waiting page still knows who signs in")
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
	t.Parallel()
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
	require.Equal(t, http.StatusNoContent, h.req("DELETE", "/api/setup/restore", "").Code)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	h := newRestoreHarness(t)
	rec := h.req("POST", "/api/setup/restore/upload", string(backupZip(t, "h", store.AuthStandard)))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Equal(t, http.StatusCreated, h.createAccount(map[string]any{"username": "reader", "password": setupPass}).Code)
	h.Restore().Close() // waits for the cancelled check, which held the slot until it stopped
	require.Equal(t, backup.RestoreNone, h.Restore().State())
	require.NoFileExists(t, filepath.Join(h.dir, backup.StagedFile))
	require.NoFileExists(t, filepath.Join(h.dir, backup.UploadFile))
}

// A checked backup belongs to the page that uploaded it. Anyone else who can
// reach setup sees no restore (GET /api/instance and GET /api/setup/restore),
// cannot confirm it with a password of their own, cannot take its feed list
// and cannot cancel it; the uploader still can.
func TestRestoreBelongsToTheUploadingBrowser(t *testing.T) {
	t.Parallel()
	h := newRestoreHarness(t)
	require.EqualValues(t, http.StatusOK, h.upload(backupZip(t, "h", store.AuthStandard))["status"])

	elsewhere := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "restore_elsewhere", decode(t, rec)["error"])
	}
	for _, mod := range []func(*http.Request){stranger, anotherBrowser("guess"), anotherBrowser(testKey(t))} {
		require.Equal(t, "none", decode(t, h.req("GET", "/api/instance", "", mod))["restore"])
		require.Equal(t, map[string]any{"state": "none", "summary": nil, "error": nil, "estimate_seconds": float64(0)}, h.status(mod))
		elsewhere(h.req("POST", "/api/setup/restore/confirm", `{"new_password":"`+setupPass+`"}`, mod))
		elsewhere(h.req("GET", "/api/setup/restore/feeds", "", mod))
		elsewhere(h.req("DELETE", "/api/setup/restore", "", mod))
	}
	elsewhere(h.req("POST", "/api/setup/restore/upload", string(backupZip(t, "h", store.AuthStandard)), anotherBrowser(testKey(t))))
	require.Zero(t, h.restarts.Load())
	require.NoFileExists(t, filepath.Join(h.dir, backup.MarkerFile))

	require.Equal(t, "ready", h.restoreState(), "the uploader still sees it")
	rec := h.req("POST", "/api/setup/restore/confirm", `{}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Equal(t, "confirmed", decode(t, h.req("GET", "/api/instance", "", stranger))["restore"], "everyone waits for the restart")
	require.Nil(t, h.status(stranger)["summary"], "but only the uploader sees whose account it is")
}

// A cookie grants nothing. Someone who can set cookies for the owner's
// browser (a sibling subdomain can) uploads a backup of their own, which is
// ready under their key K, and plants K in the owner's browser under any name.
// The owner's page, which sends its own key in the header, sees no restore
// and cannot confirm or take the attacker's upload; only the header counts.
func TestACookieGrantsNothing(t *testing.T) {
	t.Parallel()
	h := newRestoreHarness(t)
	k := testKey(t)
	rec := h.req("POST", "/api/setup/restore/upload", string(backupZip(t, "h", store.AuthStandard)), anotherBrowser(k))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return h.Restore().State() == backup.RestoreReady }, 30*time.Second, 5*time.Millisecond)

	planted := func(r *http.Request) {
		for _, name := range []string{"kipple_restore", "__Host-kipple_restore", "X-Kipple-Restore-Key"} {
			r.AddCookie(&http.Cookie{Name: name, Value: k})
		}
	}
	for _, mod := range [][]func(*http.Request){{planted}, {stranger, planted}} {
		require.Equal(t, "none", decode(t, h.req("GET", "/api/instance", "", mod...))["restore"])
		require.Equal(t, "none", h.status(mod...)["state"])
		rec = h.req("POST", "/api/setup/restore/confirm", `{"new_password":"`+setupPass+`"}`, mod...)
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Equal(t, "restore_elsewhere", decode(t, rec)["error"])
		require.Equal(t, http.StatusConflict, h.req("GET", "/api/setup/restore/feeds", "", mod...).Code)
	}
	require.Zero(t, h.restarts.Load())
	require.Equal(t, "ready", h.status(anotherBrowser(k))["state"], "it is still the attacker's, under the header")
}

// The upload refuses, before it reads the file, a request without an owner key
// of the right form, and closes the connection after the answer. The key must
// decode strictly: of the four spellings of one key that differ only in their
// last character's unused bits, only one is accepted.
func TestUploadNeedsAKey(t *testing.T) {
	t.Parallel()
	h := newRestoreHarness(t)
	b := string(backupZip(t, "h", store.AuthStandard))
	k := testKey(t)
	last := strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", k[42])
	alias := k[:42] + string("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"[last|1])
	require.NotEqual(t, k, alias)
	for _, mod := range []func(*http.Request){stranger, anotherBrowser("short"), anotherBrowser(k + "A"), anotherBrowser(alias)} {
		rec := h.req("POST", "/api/setup/restore/upload", b, mod)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		require.Equal(t, "restore_key_required", decode(t, rec)["error"])
		require.Equal(t, "close", rec.Header().Get("Connection"), "refused before the body: the connection closes")
	}
	require.Equal(t, "none", h.restoreState())
}

// rawUpload starts an upload with key on its own connection and sends the
// first sent bytes of b: the body does not finish until the caller sends the
// rest.
func rawUpload(t *testing.T, srv *httptest.Server, key string, b []byte, sent int) net.Conn {
	t.Helper()
	addr := srv.Listener.Addr().String()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "POST /api/setup/restore/upload HTTP/1.1\r\nHost: %s\r\nSec-Fetch-Site: same-origin\r\nX-Kipple-Client: web\r\n"+
		"%s: %s\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n",
		addr, restoreKeyHeader, key, len(b))
	require.NoError(t, err)
	_, err = conn.Write(b[:sent])
	require.NoError(t, err)
	return conn
}

// call sends a request to srv from the page that holds key ("": none).
func call(t *testing.T, srv *httptest.Server, key, method, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Kipple-Client", "web")
	if key != "" {
		req.Header.Set(restoreKeyHeader, key)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// readAnswer reads the upload's answer from conn.
func readAnswer(t *testing.T, conn net.Conn) (int, map[string]any) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err, "the uploader gets an answer")
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// While its body arrives an upload is already its browser's: another tab of
// that browser sees it and can cancel it (the uploader's own "Cancel upload",
// however late its abort reaches the server), while another browser sees no
// restore and its cancel is refused. The last byte is held back, so the upload
// cannot finish first whatever the timing.
func TestTheUploaderCancelsAnUploadStillArriving(t *testing.T) {
	t.Parallel()
	h := newRestoreHarness(t)
	srv := httptest.NewServer(h.root)
	defer srv.Close()
	b := backupZip(t, "h", store.AuthStandard)
	key := testKey(t)
	conn := rawUpload(t, srv, key, b, len(b)-1)

	require.Eventually(t, func() bool {
		_, st := call(t, srv, key, "GET", "/api/setup/restore")
		return st["state"] == "uploading"
	}, 5*time.Second, 5*time.Millisecond)
	_, inst := call(t, srv, key, "GET", "/api/instance")
	require.Equal(t, "uploading", inst["restore"])
	_, st := call(t, srv, "", "GET", "/api/setup/restore")
	require.Equal(t, "none", st["state"], "another browser sees nothing")
	code, out := call(t, srv, testKey(t), "DELETE", "/api/setup/restore")
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "restore_elsewhere", out["error"])

	code, _ = call(t, srv, key, "DELETE", "/api/setup/restore")
	require.Equal(t, http.StatusNoContent, code)
	code, out = readAnswer(t, conn)
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "restore_cancelled", out["error"])
	_, st = call(t, srv, key, "GET", "/api/setup/restore")
	require.Equal(t, "none", st["state"])
}

// A client that stalls mid-upload does not hold the slot past an account
// claim: the claim unblocks the waiting read at once (the handler's stop hook
// sets the connection's read deadline), and the uploader gets an answer.
// Not parallel: it measures wall time.
func TestAClaimUnblocksAStalledUpload(t *testing.T) {
	h := newRestoreHarness(t)
	srv := httptest.NewServer(h.root)
	defer srv.Close()
	conn := rawUpload(t, srv, testKey(t), backupZip(t, "h", store.AuthStandard), 100) // then nothing more
	require.Eventually(t, func() bool { return h.Restore().State() == backup.RestoreUploading }, 5*time.Second, 5*time.Millisecond)

	began := time.Now()
	require.Equal(t, http.StatusCreated, h.createAccount(map[string]any{"username": "reader", "password": setupPass}).Code)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
		require.Equal(t, http.StatusConflict, resp.StatusCode)
		_ = resp.Body.Close()
	}
	require.Less(t, time.Since(began), 5*time.Second, "not the 2 minute read timeout")
	require.Eventually(t, func() bool { return h.Restore().State() == backup.RestoreNone }, 5*time.Second, 5*time.Millisecond)
}

// An upload cannot hold the slot by sending slowly: it is stopped when it
// sends nothing for restoreReadIdle, or once its average rate falls below
// restoreMinRate after restoreRateGrace. The owner's page, which may only see
// a cut connection, reads the reason from GET /api/setup/restore (failed,
// upload_too_slow), and a new upload replaces it.
func TestASlowUploadIsStopped(t *testing.T) {
	tune := func(t *testing.T, idle time.Duration, rate int, grace time.Duration) {
		oldIdle, oldRate, oldGrace := restoreReadIdle, restoreMinRate, restoreRateGrace
		restoreReadIdle, restoreMinRate, restoreRateGrace = idle, rate, grace
		t.Cleanup(func() { restoreReadIdle, restoreMinRate, restoreRateGrace = oldIdle, oldRate, oldGrace })
	}
	// A body that looks like a zip and is longer than the 64 KB the server reads
	// first. Each case sends past that head, so the stop always lands in the
	// body: where it lands decides whether the server sees the reader's reason
	// or a cancelled request (the connection's read deadline cancels it).
	b := append([]byte("PK\x03\x04"), make([]byte, 200<<10)...)
	const sent = 64<<10 + 1000
	check := func(t *testing.T, trickle bool) {
		h := newRestoreHarness(t)
		srv := httptest.NewServer(h.root)
		defer srv.Close()
		key := testKey(t)
		conn := rawUpload(t, srv, key, b, sent)
		if trickle { // a byte at a time, never stalling for the idle limit
			go func() {
				for i := sent; i < len(b)-1; i++ {
					if _, err := conn.Write(b[i : i+1]); err != nil {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}()
		}
		code, out := readAnswer(t, conn)
		require.Equal(t, http.StatusRequestTimeout, code, out)
		require.Equal(t, "upload_too_slow", out["error"])
		require.Contains(t, out["message"], "too slow")
		_, st := call(t, srv, key, "GET", "/api/setup/restore")
		require.Equal(t, "failed", st["state"], "the owner can read why")
		require.Equal(t, "upload_too_slow", st["error"].(map[string]any)["code"])
		_, st = call(t, srv, "", "GET", "/api/setup/restore")
		require.Equal(t, "none", st["state"])
		require.Equal(t, http.StatusNoContent, func() int { c, _ := call(t, srv, key, "DELETE", "/api/setup/restore"); return c }())
		require.Equal(t, backup.RestoreNone, h.Restore().State())
	}
	t.Run("nothing sent for the idle limit", func(t *testing.T) {
		tune(t, 300*time.Millisecond, 0, time.Hour)
		check(t, false)
	})
	t.Run("average rate too low", func(t *testing.T) {
		tune(t, time.Hour, 1<<20, 200*time.Millisecond)
		check(t, true)
	})
	t.Run("past the whole upload's deadline", func(t *testing.T) {
		tune(t, time.Hour, 0, time.Hour)
		oldFloor, oldRate := restoreMaxFloor, restoreMaxRate
		restoreMaxFloor, restoreMaxRate = 400*time.Millisecond, 1<<40
		t.Cleanup(func() { restoreMaxFloor, restoreMaxRate = oldFloor, oldRate })
		check(t, true) // sending all along, but not done in time
	})
}

// One upload holds the slot at most max(2 h, its size at 128 KB/s), and never
// more than 4 h, whatever size it declares.
func TestRestoreDeadline(t *testing.T) {
	t.Parallel()
	require.Equal(t, 2*time.Hour, restoreDeadline(100<<20))
	require.Equal(t, 8192*time.Second, restoreDeadline(1<<30))
	require.Equal(t, 4*time.Hour, restoreDeadline(4<<30))
}

// A full disk while an upload is kept answers no_space, like the free-space check.
func TestRestoreErrorInfoFullDisk(t *testing.T) {
	t.Parallel()
	status, code, msg := restoreErrorInfo(fmt.Errorf("wrapped: %w", backup.ErrDiskFull))
	require.Equal(t, http.StatusInsufficientStorage, status)
	require.Equal(t, "no_space", code)
	require.Contains(t, msg, "disk is full")
}
