package api

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A session id in the database is the hash of the cookie value, so a copy of the
// database (a backup, a leaked file) holds nothing a browser can present, and the
// cookie value is unguessable and never reused: every sign-in mints a new one,
// whatever cookie the browser already carried.
func TestSessionCookieValueIsNeverStoredOrReused(t *testing.T) {
	h := newHarness(t)
	a, b := h.login(), h.login()
	require.NotEqual(t, a.Value, b.Value)
	require.GreaterOrEqual(t, len(a.Value), 43, "256 bits, base64url")

	sum := sha256.Sum256([]byte(a.Value))
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions WHERE id = ?", hex.EncodeToString(sum[:])))
	require.Zero(t, h.count("SELECT count(*) FROM sessions WHERE id = ? OR id = ?", a.Value, b.Value), "the cookie value itself is not a row id")

	// Session fixation: a cookie value an attacker planted is not a session, and signing in
	// while carrying it issues a different value; the planted one stays worthless.
	planted := &http.Cookie{Name: cookieName, Value: "attacker-chosen-value"}
	code, _, _ := h.api(planted, "GET", "/api/status", "")
	require.Equal(t, http.StatusUnauthorized, code)
	rec := h.do("POST", "/api/auth/login", loginBody(testPass), withCookie(planted))
	require.Equal(t, http.StatusNoContent, rec.Code)
	for _, c := range rec.Result().Cookies() {
		require.NotEqual(t, planted.Value, c.Value)
	}
	code, _, _ = h.api(planted, "GET", "/api/status", "")
	require.Equal(t, http.StatusUnauthorized, code, "the planted value never becomes valid")
}

// Logout deletes the server-side row, not just the browser's cookie: replaying the
// old cookie fails, and signing out one browser leaves the others signed in.
func TestLogoutDeletesTheServerSideSession(t *testing.T) {
	h := newHarness(t)
	a, b := h.login(), h.login()
	require.Equal(t, 2, h.count("SELECT count(*) FROM sessions"))

	rec := h.do("POST", "/api/auth/logout", "", withCookie(a))
	require.Equal(t, http.StatusNoContent, rec.Code)
	sum := sha256.Sum256([]byte(a.Value))
	require.Zero(t, h.count("SELECT count(*) FROM sessions WHERE id = ?", hex.EncodeToString(sum[:])), "the row is gone")
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions"), "the other browser's session is untouched")

	code, _, _ := h.api(a, "GET", "/api/status", "") // a captured copy of the cookie
	require.Equal(t, http.StatusUnauthorized, code)
	code, _, _ = h.api(b, "GET", "/api/status", "")
	require.Equal(t, http.StatusOK, code)

	// Replaying the logout with the dead cookie needs a session like any other call and changes nothing.
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/logout", "", withCookie(a)).Code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions"))
}

// A session that is not used for the whole 90 days expires, and one that is used
// slides forward from the use.
func TestSessionExpiresAfterNinetyIdleDaysAndSlidesWithUse(t *testing.T) {
	h := newHarness(t)
	idle, busy := h.login(), h.login()

	h.clk.Advance(60 * 24 * time.Hour)
	code, _, rec := h.api(busy, "GET", "/api/status", "")
	require.Equal(t, http.StatusOK, code)
	var renewed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			renewed = c
		}
	}
	require.NotNil(t, renewed, "a use after an hour re-issues the cookie with a fresh Max-Age")
	require.Equal(t, int(sessionTTL/time.Second), renewed.MaxAge)

	h.clk.Advance(31 * 24 * time.Hour) // day 91 since login
	code, _, _ = h.api(idle, "GET", "/api/status", "")
	require.Equal(t, http.StatusUnauthorized, code, "unused for 91 days")
	code, _, _ = h.api(busy, "GET", "/api/status", "")
	require.Equal(t, http.StatusOK, code, "used at day 60, so valid until day 150")
}
