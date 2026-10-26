package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/httpx"
)

// backupConfirm is the body of an export: the current web password.
var backupConfirm = jsonStr(map[string]string{"current": testPass})

func (h *harness) backupSetup(t *testing.T) *http.Cookie {
	t.Helper()
	f := h.addFeed("A", h.addFolder("F"))
	h.addItem(f, seedItem{Title: "one"})
	return h.login()
}

func fresh(t *testing.T, h *harness) map[string]any {
	t.Helper()
	c := h.backupSetup(t)
	code, body, _ := h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusOK, code, "%v", body)
	body["cookie"] = c
	return body
}

func TestBackupCreateAndDownload(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, Version: "t", FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
	})
	body := fresh(t, h)
	c := body["cookie"].(*http.Cookie)
	tok := body["token"].(string)
	require.Len(t, tok, 32)
	require.Equal(t, "/api/backup/"+tok, body["url"])
	require.Regexp(t, `^kipple-backup-\d{8}-\d{6}\.zip$`, body["filename"])
	require.Contains(t, body["warning"], "password hashes")
	require.Contains(t, body["warning"], "HTTP Basic")
	require.Contains(t, body["warning"], "sign in from a sync app as you")
	require.EqualValues(t, 300, body["expires_in"])
	require.InDelta(t, time.Now().Add(5*time.Minute).Unix(), body["expires_at"], 10)
	require.EqualValues(t, 1, body["contents"].(map[string]any)["items"])

	// A plain link click: same-origin GET, no X-Kipple-Client, session cookie.
	get := func(mod ...func(*http.Request)) (int, []byte, http.Header) {
		rec := h.do("GET", "/api/backup/"+tok, "", append([]func(*http.Request){func(r *http.Request) { r.Header.Del("X-Kipple-Client") }}, mod...)...)
		return rec.Code, rec.Body.Bytes(), rec.Header()
	}
	code, _, _ := get() // no session
	require.Equal(t, http.StatusUnauthorized, code)

	code, zipBytes, hdr := get(withCookie(c))
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "application/zip", hdr.Get("Content-Type"))
	require.Equal(t, `attachment; filename="`+body["filename"].(string)+`"`, hdr.Get("Content-Disposition"))
	require.Equal(t, "private, no-store", hdr.Get("Cache-Control"))
	require.EqualValues(t, len(zipBytes), int(body["bytes"].(float64)))
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	require.Contains(t, names, "kipple.db")
	require.Contains(t, names, "manifest.json")

	code, b, _ := get(withCookie(c))
	require.Equal(t, http.StatusNotFound, code, "single use")
	require.Contains(t, string(b), "gone")
}

func TestBackupRules(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
	})
	// unauthenticated
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/backup", backupConfirm).Code)
	c := h.login()
	// POST needs X-Kipple-Client
	rec := h.do("POST", "/api/backup", "", withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Equal(t, http.StatusForbidden, rec.Code)
	// unknown token
	rec = h.do("GET", "/api/backup/"+strings.Repeat("a", 32), "", withCookie(c))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBackupBusyAndNoSpace(t *testing.T) {
	t.Parallel()
	space := uint64(1 << 40)
	h := newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, FreeBytes: func(string) (uint64, error) { return space, nil }})
	})
	c := h.backupSetup(t)

	release, err := h.db.TrySnapshot()
	require.NoError(t, err)
	code, body, rec := h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "busy", body["error"])
	require.Equal(t, "10", rec.Header().Get("Retry-After"))
	require.EqualValues(t, 10, body["retry_after"])
	require.NotEmpty(t, body["message"])
	release()

	space = 1 << 20
	code, body, _ = h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusInsufficientStorage, code)
	require.Equal(t, "no_space", body["error"])
	require.Contains(t, body["message"], "not enough free disk space")

	space = 1 << 40
	code, _, _ = h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusOK, code)
}

func TestBackupResponseIsJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.backupSetup(t)
	rec := h.do("POST", "/api/backup", backupConfirm, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	require.Contains(t, m, "token")
}

// A backup download is a plain read: the browser may send no fetch metadata at all, or
// cross-site, and either way the answer is the zip, which another origin cannot read
// (same-origin CORP, nosniff).
func TestBackupDownloadHasNoOriginRule(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, Version: "t", FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
	})
	secure := httpx.Secure(h.mux, httpx.Options{})
	body := fresh(t, h)
	c := body["cookie"].(*http.Cookie)
	download := func(tok string, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/backup/"+tok, nil)
		r.AddCookie(c)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		secure.ServeHTTP(rec, r)
		return rec
	}
	for _, site := range []string{"", "cross-site"} {
		tok := body["token"].(string)
		if site != "" {
			code, b, _ := h.api(c, "POST", "/api/backup", backupConfirm)
			require.Equal(t, http.StatusOK, code)
			tok = b["token"].(string)
		}
		rec := download(tok, site)
		require.Equal(t, http.StatusOK, rec.Code, "site=%q", site)
		require.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
		require.Equal(t, "same-origin", rec.Header().Get("Cross-Origin-Resource-Policy"))
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		require.Equal(t, "PK", rec.Body.String()[:2])
	}
}

// An export holds the account's credentials, so it proves the current web
// password exactly as the API password change does: a wrong or missing one is
// 403 bad_password, a body that is not JSON 400, and nothing is built.
func TestBackupNeedsCurrentPassword(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	h := newHarness(t, func(o *Options) {
		o.Backups = backup.New(backup.Options{DB: o.DB, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	c := h.backupSetup(t)
	for _, tc := range []struct {
		name, body string
		want       int
		kind       string
	}{
		{"wrong password", `{"current":"nope-nope-nope"}`, http.StatusForbidden, "bad_password"},
		{"no password", `{}`, http.StatusForbidden, "bad_password"},
		{"empty password", `{"current":""}`, http.StatusForbidden, "bad_password"},
		{"no body", ``, http.StatusForbidden, "bad_password"},
		{"not JSON", `current=correct-horse`, http.StatusBadRequest, "bad_request"},
	} {
		code, out, _ := h.api(c, "POST", "/api/backup", tc.body)
		require.Equal(t, tc.want, code, tc.name)
		require.Equal(t, tc.kind, out["error"], tc.name)
		require.NotContains(t, out, "token", tc.name)
	}
	code, out, _ := h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Len(t, out["token"], 32)
	require.NotContains(t, logs.String(), testPass, "the password never reaches the log")
	require.NotContains(t, logs.String(), "nope-nope-nope", "nor does a wrong one")
}

// Wrong passwords on the export are paced like sign-in and every other
// password check, and never lock the owner out: the right one still works.
func TestBackupPasswordFailuresArePacedNeverLocked(t *testing.T) {
	t.Parallel()
	h := newHarness(t, realVerifier)
	c := h.backupSetup(t)
	for i := range 10 {
		code, _, _ := h.api(c, "POST", "/api/backup", `{"current":"wrong-wrong-wrong"}`)
		require.Equal(t, http.StatusForbidden, code, "attempt %d", i)
	}
	require.Positive(t, h.paced.Load(), "the failures were counted")
	// Counted with the other password checks: sign-in and the API password change still accept the right one.
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	code, _, _ := h.api(c, "POST", "/api/backup", backupConfirm)
	require.Equal(t, http.StatusOK, code)
}

// An account without a web password proves itself with a verified Cloudflare
// Access token, as for the API password; nothing else stands in.
func TestPasswordlessBackupNeedsToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, other := accessKeys(t)
	rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := sessionCookie(t, rec)
	for _, body := range []string{``, `{"current":""}`, backupConfirm} {
		rec = h.do("POST", "/api/backup", body, withCookie(c))
		require.Equal(t, http.StatusForbidden, rec.Code, "body %q: %s", body, rec.Body.String())
		require.NotContains(t, rec.Body.String(), "token\":")
	}
	rec = h.do("POST", "/api/backup", ``, withCookie(c), withJWT(h.jwt(other, nil)))
	require.Equal(t, http.StatusForbidden, rec.Code, "a token from another key is refused")
	rec = h.do("POST", "/api/backup", ``, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"token"`)
}

// Open mode has no password to prove (as for the API password): the export
// needs no body, but only from where open mode itself is allowed.
func TestOpenModeBackupNeedsNoPassword(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	rec := h.req("POST", "/api/backup", "", withCookies(sess))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"token"`)

	rec = h.req("POST", "/api/backup", "", withCookies(sess), host("anything.ts.net"), hdr("Origin", "http://anything.ts.net"),
		hdr("X-Forwarded-For", "203.0.113.9"), hdr("X-Forwarded-Proto", "http"))
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.Equal(t, "open_refused", decode(t, rec)["error"])
}

// On an account without a web password, a token signed with the right key but
// for another application or issuer is a failed proof and is counted; a
// request with no token at all presents nothing and is not.
func TestPasswordlessBackupTokenCounting(t *testing.T) {
	t.Parallel()
	for name, edit := range map[string]func(map[string]any){
		"wrong aud": func(c map[string]any) { c["aud"] = []string{"other"} },
		"wrong iss": func(c map[string]any) { c["iss"] = "https://evil.cloudflareaccess.com" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, withAccess(t))
			h.dropPassword()
			k, _ := accessKeys(t)
			rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
			require.Equal(t, http.StatusNoContent, rec.Code)
			c := sessionCookie(t, rec)
			for i := range 10 {
				rec = h.do("POST", "/api/backup", ``, withCookie(c), withJWT(h.jwt(k, edit)))
				require.Equal(t, http.StatusForbidden, rec.Code, "attempt %d: %s", i, rec.Body.String())
			}
			require.Positive(t, h.paced.Load(), "the refused tokens were counted")
		})
	}
	t.Run("no token", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, withAccess(t))
		h.dropPassword()
		k, _ := accessKeys(t)
		rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
		require.Equal(t, http.StatusNoContent, rec.Code)
		c := sessionCookie(t, rec)
		for i := range 15 {
			rec = h.do("POST", "/api/backup", ``, withCookie(c))
			require.Equal(t, http.StatusForbidden, rec.Code, "attempt %d", i)
		}
		require.Zero(t, h.paced.Load(), "nothing presented, nothing counted")
		rec = h.do("POST", "/api/backup", ``, withCookie(c), withJWT(h.jwt(k, nil)))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestBackupOversizedBodyAndReaderAuth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.backupSetup(t)
	big := `{"current":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	code, out, _ := h.api(c, "POST", "/api/backup", big)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "bad_request", out["error"])
	require.Zero(t, h.paced.Load(), "refused before the password check")

	// A Reader API token is not a web session.
	rec := h.do("POST", "/api/backup", backupConfirm, func(r *http.Request) { r.Header.Set("Authorization", "GoogleLogin auth="+testUser+"/abcdef") })
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotContains(t, rec.Body.String(), `"token"`)
}
