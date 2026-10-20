package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// holding makes a request carry only the cookies given, as a browser does: a Secure (__Host-) cookie is sent over
// https only, so a test over http never holds one.
func holding(cs ...*http.Cookie) func(*http.Request) {
	return func(r *http.Request) {
		for _, c := range cs {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
}

// Over https the session cookie is __Host- prefixed, with what the browser requires of one, and no bare cookie is set
// or sent to clear.
func TestSessionCookieOverHTTPSIsHostPrefixed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do("POST", "/api/auth/login", loginBody(testPass), overScheme("https"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	c := cookieNamed(rec, hostCookieName)
	require.NotNil(t, c)
	require.True(t, c.Secure)
	require.True(t, c.HttpOnly)
	require.Equal(t, "/", c.Path)
	require.Empty(t, c.Domain)
	require.Equal(t, int(sessionTTL/time.Second), c.MaxAge)
	require.Nil(t, cookieNamed(rec, cookieName), "no bare session cookie over https, and none sent to clear")

	// The browser sends back only the __Host- cookie.
	require.Equal(t, http.StatusOK, h.do("GET", "/api/status", "", overScheme("https"), holding(c)).Code)
}

// Over plain http the browser refuses the prefix, so the bare name is used, and a __Host- cookie is not read.
func TestSessionCookieOverHTTPIsBare(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	rec := h.do("POST", "/api/auth/login", loginBody(testPass), overScheme("http"))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	c := cookieNamed(rec, cookieName)
	require.NotNil(t, c)
	require.False(t, c.Secure)
	require.Nil(t, cookieNamed(rec, hostCookieName))
	require.Equal(t, http.StatusOK, h.do("GET", "/api/status", "", overScheme("http"), holding(c)).Code)

	// The same value under the __Host- name over http is not the session.
	host := &http.Cookie{Name: hostCookieName, Value: c.Value}
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/status", "", overScheme("http"), holding(host)).Code)
}

// A session cookie still held under the bare name over https is no longer valid: the next launch shows the sign-in
// form, and signing in again clears the leftover.
func TestBareSessionCookieOverHTTPSIsNoSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bare := h.login() // an earlier visit over plain http, or before the prefix
	require.Equal(t, cookieName, bare.Name)
	require.Equal(t, http.StatusOK, h.do("GET", "/api/status", "", overScheme("http"), holding(bare)).Code)

	for _, path := range []string{"/api/status", "/api/bootstrap", "/api/events"} {
		require.Equal(t, http.StatusUnauthorized, h.do("GET", path, "", overScheme("https"), holding(bare)).Code, path)
	}
	rec := h.do("GET", "/api/instance", "", overScheme("https"), holding(bare))
	require.Equal(t, false, decode(t, rec)["signed_in"])

	// Signing in clears the leftover under its name, and sets the __Host- cookie.
	rec = h.do("POST", "/api/auth/login", loginBody(testPass), overScheme("https"), holding(bare))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.NotNil(t, cookieNamed(rec, hostCookieName))
	left := cookieNamed(rec, cookieName)
	require.NotNil(t, left)
	require.Equal(t, -1, left.MaxAge)
	require.Equal(t, int(sessionTTL/time.Second), cookieNamed(rec, hostCookieName).MaxAge)
}

// A cookie of the bare name set for the whole parent domain by a sibling subdomain rides along with the real one. It
// is never the session: it neither stands in for a missing __Host- cookie, nor replaces the real one, nor stops a
// sign-in from working.
func TestParentDomainBareCookieCannotPoseAsTheSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	real := h.loginHTTPS()
	other := h.loginHTTPS() // another live session, whose value the attacker's cookie carries
	planted := &http.Cookie{Name: cookieName, Value: other.Value}
	forged := &http.Cookie{Name: cookieName, Value: "attacker-chosen-value"}

	for _, p := range []*http.Cookie{planted, forged} {
		require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/status", "", overScheme("https"), holding(p)).Code,
			"a bare cookie alone is no session over https")
		rec := h.do("GET", "/api/instance", "", overScheme("https"), holding(p))
		require.Equal(t, false, decode(t, rec)["signed_in"])
	}

	// With the real cookie too, the request is the real session's: the planted cookie is not read.
	require.Equal(t, http.StatusOK, h.do("GET", "/api/status", "", overScheme("https"), holding(real, forged)).Code)
	// A sign-out by the real session deletes that session only.
	rec := h.do("POST", "/api/auth/logout", "", overScheme("https"), holding(real, planted))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/status", "", overScheme("https"), holding(real)).Code)

	// The sign-in form works with the planted cookie in the jar, and the server picks the value, never the client.
	rec = h.do("POST", "/api/auth/login", loginBody(testPass), overScheme("https"), holding(forged))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	fresh := cookieNamed(rec, hostCookieName)
	require.NotNil(t, fresh)
	require.NotEqual(t, forged.Value, fresh.Value)
}

// Every clear removes both names, the __Host- one always with Secure, Path=/ and no Domain, whatever the scheme.
func TestSessionCookieClearsBothNames(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, scheme := range []string{"https", "http"} {
		rec := h.do("POST", "/api/auth/login", loginBody(testPass), overScheme(scheme))
		require.Equal(t, http.StatusNoContent, rec.Code)
		name := cookieName
		if scheme == "https" {
			name = hostCookieName
		}
		rec = h.do("POST", "/api/auth/logout", "", overScheme(scheme), holding(cookieNamed(rec, name)))
		require.Equal(t, http.StatusNoContent, rec.Code, scheme)
		requireSessionCleared(t, rec, scheme)
		requireBothMarkersCleared(t, rec, scheme)
	}
}

func requireSessionCleared(t *testing.T, rec *httptest.ResponseRecorder, msg string) {
	t.Helper()
	host := cookieNamed(rec, hostCookieName)
	require.NotNil(t, host, msg)
	require.Equal(t, -1, host.MaxAge, msg)
	require.True(t, host.Secure, "%s: the browser ignores a __Host- clear without Secure", msg)
	require.Equal(t, "/", host.Path, msg)
	require.Empty(t, host.Domain, msg)
	bare := cookieNamed(rec, cookieName)
	require.NotNil(t, bare, msg)
	require.Equal(t, -1, bare.MaxAge, msg)
}

// A sign-out over https also ends a session held under the bare name, so a copy left from plain http does not sign in
// again if the scheme the server sees changes back.
func TestLogoutEndsABareSessionToo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bare := h.login()
	real := h.loginHTTPS()
	rec := h.do("POST", "/api/auth/logout", "", overScheme("https"), holding(real, bare))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/status", "", overScheme("http"), holding(bare)).Code)
}

// The first-run claim over https signs the browser in under the __Host- name.
func TestSetupSessionOverHTTPSIsHostPrefixed(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	rec := h.createAccount(map[string]any{"username": "reader", "password": setupPass}, overScheme("https"))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	c := cookieNamed(rec, hostCookieName)
	require.NotNil(t, c)
	require.True(t, c.Secure)
	require.Nil(t, cookieNamed(rec, cookieName))
}
