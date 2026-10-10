package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/httpx"
)

func (h *harness) backupSetup(t *testing.T) *http.Cookie {
	t.Helper()
	f := h.addFeed("A", h.addFolder("F"))
	h.addItem(f, seedItem{Title: "one"})
	return h.login()
}

func fresh(t *testing.T, h *harness) map[string]any {
	t.Helper()
	c := h.backupSetup(t)
	code, body, _ := h.api(c, "POST", "/api/backup", "")
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
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/backup", "").Code)
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
	code, body, rec := h.api(c, "POST", "/api/backup", "")
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "busy", body["error"])
	require.Equal(t, "10", rec.Header().Get("Retry-After"))
	require.EqualValues(t, 10, body["retry_after"])
	require.NotEmpty(t, body["message"])
	release()

	space = 1 << 20
	code, body, _ = h.api(c, "POST", "/api/backup", "")
	require.Equal(t, http.StatusInsufficientStorage, code)
	require.Equal(t, "no_space", body["error"])
	require.Contains(t, body["message"], "not enough free disk space")

	space = 1 << 40
	code, _, _ = h.api(c, "POST", "/api/backup", "")
	require.Equal(t, http.StatusOK, code)
}

func TestBackupResponseIsJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.backupSetup(t)
	rec := h.do("POST", "/api/backup", "", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	require.Contains(t, m, "token")
}

// A backup download is a plain read: the browser may send no fetch metadata at all, or
// cross-site, and either way the answer is the zip, which another origin cannot read
// (same-origin CORP, nosniff).
func TestBackupDownloadHasNoOriginRule(t *testing.T) {
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
			code, b, _ := h.api(c, "POST", "/api/backup", "")
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
