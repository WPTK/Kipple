package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/access"
)

const (
	accTeam = "myteam.cloudflareaccess.com"
	accAUD  = "kipple-app-aud"
)

var (
	accKeyOnce sync.Once
	accKey     *rsa.PrivateKey
	accOther   *rsa.PrivateKey
)

func accessKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	accKeyOnce.Do(func() {
		var err error
		if accKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if accOther, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return accKey, accOther
}

// withAccess turns on Access validation against a fake key set that holds the
// test key under kid "k1".
func withAccess(t *testing.T) func(*Options) {
	return func(o *Options) {
		k, _ := accessKeys(t)
		b64 := base64.RawURLEncoding.EncodeToString
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "kid": "k1",
				"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes()),
			}}})
		}))
		t.Cleanup(srv.Close)
		v, err := access.New(accTeam, accAUD, access.Options{CertsURL: srv.URL, Client: srv.Client(), Now: o.Now})
		require.NoError(t, err)
		require.NoError(t, v.Prefetch(context.Background())) // as serve does at startup
		o.Access = v
	}
}

// jwt signs an Access token issued now by the harness clock; edit adjusts the
// claims.
func (h *harness) jwt(key *rsa.PrivateKey, edit func(map[string]any)) string {
	h.t.Helper()
	now := h.clk.Now()
	claims := map[string]any{
		"iss": "https://" + accTeam, "aud": []string{accAUD}, "email": "owner@example.com", "sub": "u1",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	if edit != nil {
		edit(claims)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
	cb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(h.t, err)
	return in + "." + b64(sig)
}

func withJWT(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(access.Header, tok) }
}

func (h *harness) dropPassword() {
	h.t.Helper()
	require.NoError(h.t, h.db.SetPasswordHash(context.Background(), "", ""))
}

func (h *harness) me(mod ...func(*http.Request)) map[string]any {
	h.t.Helper()
	rec := h.do("GET", "/api/auth/me", "", mod...)
	require.Equal(h.t, http.StatusOK, rec.Code, rec.Body.String())
	var m map[string]any
	require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &m))
	return m
}

func passwordlessLogin(user string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": ""})
	return string(b)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestAccessOffMeAndPasswordRequired(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	k, _ := accessKeys(t)
	m := h.me(withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, false, m["access_enabled"])
	require.Nil(t, m["access_email"], "without Access configured no token is ever read")
	require.Equal(t, true, m["password_set"])
	require.Equal(t, testUser, m["username"])

	// Removing the password is refused while Access is not configured.
	rec := h.do("POST", "/api/account/password", `{"current":"`+testPass+`","remove":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "access_not_configured")

	// Even an account that somehow has no password cannot sign in without
	// Access, with or without a (well-signed) token.
	h.dropPassword()
	for _, mod := range [][]func(*http.Request){nil, {withJWT(h.jwt(k, nil))}} {
		rec = h.do("POST", "/api/auth/login", passwordlessLogin(testUser), mod...)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		rec = h.do("POST", "/api/auth/login", loginBody(testPass), mod...)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	}
}

func TestAccessMeReportsVerifiedEmail(t *testing.T) {
	h := newHarness(t, withAccess(t))
	c := h.login()
	k, other := accessKeys(t)
	m := h.me(withCookie(c))
	require.Equal(t, true, m["access_enabled"])
	require.Nil(t, m["access_email"])

	m = h.me(withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, "owner@example.com", m["access_email"])

	for name, tok := range map[string]string{
		"forged":    h.jwt(other, nil),
		"wrong aud": h.jwt(k, func(c map[string]any) { c["aud"] = "another-app" }),
		"wrong iss": h.jwt(k, func(c map[string]any) { c["iss"] = "https://evil.cloudflareaccess.com" }),
		"expired":   h.jwt(k, func(c map[string]any) { c["exp"] = h.clk.Now().Add(-time.Hour).Unix() }),
		"garbage":   "not-a-jwt",
	} {
		m = h.me(withCookie(c), withJWT(tok))
		require.Nil(t, m["access_email"], name)
	}
	// A token alone is never a session: /me still needs the cookie.
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/auth/me", "", withJWT(h.jwt(k, nil))).Code)

	// The bootstrap user object carries the same fields.
	rec := h.do("GET", "/api/bootstrap", "", withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusOK, rec.Code)
	var boot struct {
		User map[string]any `json:"user"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &boot))
	require.Equal(t, "owner@example.com", boot.User["access_email"])
	require.Equal(t, true, boot.User["password_set"])
}

func TestAccessTokenNeverReplacesASetPassword(t *testing.T) {
	h := newHarness(t, withAccess(t))
	k, _ := accessKeys(t)
	tok := h.jwt(k, nil)
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(tok)).Code)
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong"), withJWT(tok)).Code)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), withJWT(tok)).Code)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code, "the password works without Access too")
}

func TestPasswordlessLoginNeedsAVerifiedToken(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, other := accessKeys(t)

	refused := map[string][]func(*http.Request){
		"no token":      nil,
		"forged":        {withJWT(h.jwt(other, nil))},
		"wrong aud":     {withJWT(h.jwt(k, func(c map[string]any) { c["aud"] = []string{"other"} }))},
		"wrong iss":     {withJWT(h.jwt(k, func(c map[string]any) { c["iss"] = "https://" + accTeam + "/" }))},
		"expired":       {withJWT(h.jwt(k, func(c map[string]any) { c["exp"] = h.clk.Now().Add(-2 * time.Minute).Unix() }))},
		"not yet":       {withJWT(h.jwt(k, func(c map[string]any) { c["nbf"] = h.clk.Now().Add(10 * time.Minute).Unix() }))},
		"service token": {withJWT(h.jwt(k, func(c map[string]any) { delete(c, "email"); c["common_name"] = "svc" }))},
	}
	for name, mod := range refused {
		rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), mod...)
		require.Equal(t, http.StatusUnauthorized, rec.Code, name)
		require.Empty(t, rec.Result().Cookies(), name)
	}
	// A valid token with the wrong user name is refused too.
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", passwordlessLogin("someone"), withJWT(h.jwt(k, nil))).Code)

	// A valid token signs in; a typed password is ignored (there is none).
	rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := sessionCookie(t, rec)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody("typed anyway"), withJWT(h.jwt(k, nil))).Code)

	m := h.me(withCookie(c))
	require.Equal(t, false, m["password_set"])
}

func TestPasswordlessFailuresCountTowardLockout(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, other := accessKeys(t)
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(other, nil))).Code)
	}
	require.Equal(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil))).Code)
}

func TestRemoveAndRestorePassword(t *testing.T) {
	h := newHarness(t, withAccess(t))
	k, other := accessKeys(t)
	c := h.login()
	elsewhere := h.login()
	remove := `{"current":"` + testPass + `","remove":true}`

	// Without a verified token (the LAN, a forged token) removal is refused.
	for _, mod := range [][]func(*http.Request){nil, {withJWT(h.jwt(other, nil))}} {
		rec := h.do("POST", "/api/account/password", remove, append([]func(*http.Request){withCookie(c)}, mod...)...)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "access_required")
	}
	// The current password is still needed.
	rec := h.do("POST", "/api/account/password", `{"current":"wrong","remove":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "bad_password")
	// "new" and "remove" together are refused.
	rec = h.do("POST", "/api/account/password", `{"current":"`+testPass+`","new":"abcdef","remove":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = h.do("POST", "/api/account/password", remove, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	acct, _, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.Empty(t, acct.PasswordHash)
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/auth/me", "", withCookie(elsewhere)).Code, "other sessions are signed out")
	require.Equal(t, http.StatusOK, h.do("GET", "/api/auth/me", "", withCookie(c)).Code, "the caller's session stays")

	// The old password no longer signs in; only a token does.
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)

	// Setting a password again: the token stands in for the missing current one,
	// and nothing else does.
	set := `{"current":"","new":"a new pass"}`
	rec = h.do("POST", "/api/account/password", set, withCookie(c))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "access_required")
	rec = h.do("POST", "/api/account/password", set, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	acct, _, err = h.db.Account(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, acct.PasswordHash)
}

func TestPasswordlessAPIPasswordNeedsToken(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, _ := accessKeys(t)
	rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := sessionCookie(t, rec)
	rec = h.do("POST", "/api/account/api-password", `{"current":"","generate":true}`, withCookie(c))
	require.Equal(t, http.StatusForbidden, rec.Code)
	rec = h.do("POST", "/api/account/api-password", `{"current":"","generate":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "api_password")
}

func TestPasswordlessNothingPresentedIsNotCounted(t *testing.T) {
	h := newHarness(t, withAccess(t))
	k, _ := accessKeys(t)
	// An empty password on an account that has one (a submit before typing):
	// refused, never counted, so it cannot lock the owner out.
	for i := 0; i < 15; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", passwordlessLogin(testUser)).Code)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)

	// A passwordless account tried without any token (the LAN): not counted
	// either, while a valid token still gets in.
	h.dropPassword()
	for i := 0; i < 15; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", passwordlessLogin(testUser)).Code)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil))).Code)
}

func TestRemoveWhenAlreadyPasswordlessChangesNothing(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, _ := accessKeys(t)
	login := func() *http.Cookie {
		rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
		require.Equal(t, http.StatusNoContent, rec.Code)
		return sessionCookie(t, rec)
	}
	c, other := login(), login()
	rec := h.do("POST", "/api/account/password", `{"current":"","remove":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, http.StatusOK, h.do("GET", "/api/auth/me", "", withCookie(other)).Code, "no other session is signed out")
}

func TestPasswordlessWithAccessOffPointsToTheCLI(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	// The password was removed while Access was on; Access is now off.
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "", sessionID(c.Value)))
	k, _ := accessKeys(t)
	for _, path := range []string{"/api/account/password", "/api/account/api-password"} {
		body := `{"current":"","new":"a new pass"}`
		if path == "/api/account/api-password" {
			body = `{"current":"","generate":true}`
		}
		rec := h.do("POST", path, body, withCookie(c), withJWT(h.jwt(k, nil)))
		require.Equal(t, http.StatusForbidden, rec.Code, path)
		require.Contains(t, rec.Body.String(), "access_not_configured", path)
		require.Contains(t, rec.Body.String(), "kipple password", path)
	}
	// Not counted: the owner is never locked out by these.
	for i := 0; i < 12; i++ {
		require.Equal(t, http.StatusForbidden, h.do("POST", "/api/account/password", `{"current":"","new":"a new pass"}`, withCookie(c)).Code)
	}
}
