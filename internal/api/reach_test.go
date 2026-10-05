package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/store"
)

// withSeed seeds the reachability settings as the environment variables would
// at start, and opens the server's Live on them.
func withSeed(t *testing.T, seed reach.Seed) func(*Options) {
	return func(o *Options) {
		_, err := reach.SeedSettings(context.Background(), o.DB, seed)
		require.NoError(t, err)
		l, err := reach.Open(context.Background(), o.DB, reach.Options{NoPrefetch: true})
		require.NoError(t, err)
		o.Reach = l
	}
}

// withAccessOptions turns the Access setting on (accTeam, accAUD) and opens the
// server's Live with verifiers built on opt.
func withAccessOptions(t *testing.T, opt access.Options) func(*Options) {
	return func(o *Options) {
		require.NoError(t, o.DB.SetSettings(context.Background(), map[string]any{
			store.SettingCloudflareAccess: map[string]any{"team_domain": accTeam, "aud": accAUD}}))
		l, err := reach.Open(context.Background(), o.DB, reach.Options{Access: opt, NoPrefetch: true})
		require.NoError(t, err)
		o.Reach = l
	}
}

// patchIssue sends one PATCH /api/settings and returns the status and, for a
// 400, the message of the issue about key.
func patchIssue(t *testing.T, h *harness, c *http.Cookie, body string, key string) (int, string) {
	t.Helper()
	rec := h.do("PATCH", "/api/settings", body, withCookie(c))
	if rec.Code != http.StatusBadRequest {
		return rec.Code, ""
	}
	var out struct {
		Error  string         `json:"error"`
		Issues []settingIssue `json:"issues"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "invalid_settings", out.Error)
	for _, is := range out.Issues {
		if is.Key == key {
			return rec.Code, is.Message
		}
	}
	t.Fatalf("no issue about %s in %s", key, rec.Body.String())
	return 0, ""
}

func TestConnectionSettingsValidateAndNormalize(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	values := func() map[string]any {
		_, out, _ := h.api(c, "GET", "/api/settings", "")
		return out["values"].(map[string]any)
	}
	// Defaults: nothing set.
	v := values()
	require.Equal(t, "", v[store.SettingPublicURL])
	require.Equal(t, []any{}, v[store.SettingTrustedProxies])
	require.Equal(t, map[string]any{}, v[store.SettingCloudflareAccess])

	code, _ := patchIssue(t, h, c, `{"server.public_url":" https://rss.example.com/kipple ",
		"security.trusted_proxies":["192.0.2.10"," 198.51.100.7/24","::ffff:192.0.2.10","2001:db8::/32"],
		"security.cloudflare_access":{"team_domain":"https://MyTeam.cloudflareaccess.com/","aud":" abc123 "}}`, "")
	require.Equal(t, http.StatusOK, code)
	v = values()
	require.Equal(t, "https://rss.example.com/kipple", v[store.SettingPublicURL])
	require.Equal(t, []any{"192.0.2.10", "198.51.100.0/24", "2001:db8::/32"}, v[store.SettingTrustedProxies], "normalized, repeats dropped")
	require.Equal(t, map[string]any{"team_domain": "myteam.cloudflareaccess.com", "aud": "abc123"}, v[store.SettingCloudflareAccess])

	// Empty values turn each one off.
	code, _ = patchIssue(t, h, c, `{"server.public_url":"","security.trusted_proxies":[],"security.cloudflare_access":{"team_domain":" ","aud":""}}`, "")
	require.Equal(t, http.StatusOK, code)
	v = values()
	require.Equal(t, "", v[store.SettingPublicURL])
	require.Equal(t, map[string]any{}, v[store.SettingCloudflareAccess])

	for _, bad := range []struct{ key, value, says string }{
		{store.SettingPublicURL, `"rss.example.com"`, "http://"},
		{store.SettingPublicURL, `"ftp://rss.example.com"`, "http://"},
		{store.SettingPublicURL, `"https://user@rss.example.com"`, "user info"},
		{store.SettingPublicURL, `"https://rss.example.com/?a=1"`, "query"},
		{store.SettingPublicURL, `"https://"`, "no host"},
		{store.SettingPublicURL, `42`, "https://rss.example.com"},
		{store.SettingTrustedProxies, `"192.0.2.10"`, "list"},
		{store.SettingTrustedProxies, `["192.0.2.10,192.0.2.11"]`, "list"},
		{store.SettingTrustedProxies, `["proxy.example.com"]`, "invalid IP"},
		{store.SettingTrustedProxies, `["0.0.0.0/0"]`, "every address"},
		{store.SettingTrustedProxies, `["::/0"]`, "every address"},
		{store.SettingTrustedProxies, `[1]`, "list"},
		{store.SettingCloudflareAccess, `{"team_domain":"myteam.cloudflareaccess.com"}`, "audience (AUD) tag is missing"},
		{store.SettingCloudflareAccess, `{"aud":"abc"}`, "team domain is missing"},
		{store.SettingCloudflareAccess, `{"team_domain":"localhost","aud":"abc"}`, "team domain"},
		{store.SettingCloudflareAccess, `{"team_domain":"myteam.cloudflareaccess.com","aud":"a b"}`, "AUD"},
		{store.SettingCloudflareAccess, `{"team_domain":"myteam.cloudflareaccess.com","aud":"abc","x":"y"}`, "off"},
		{store.SettingCloudflareAccess, `"myteam.cloudflareaccess.com"`, "off"},
	} {
		code, msg := patchIssue(t, h, c, `{"`+bad.key+`":`+bad.value+`}`, bad.key)
		require.Equal(t, http.StatusBadRequest, code, bad.value)
		require.Contains(t, msg, bad.says, bad.value)
	}
	many := make([]string, maxTrustedProxies+1)
	for i := range many {
		many[i] = `"192.0.2.1"`
	}
	code, _ = patchIssue(t, h, c, `{"security.trusted_proxies":[`+strings.Join(many, ",")+`]}`, store.SettingTrustedProxies)
	require.Equal(t, http.StatusBadRequest, code)

	// Nothing invalid was stored.
	v = values()
	require.Equal(t, "", v[store.SettingPublicURL])
	require.Equal(t, []any{}, v[store.SettingTrustedProxies])
}

// A settings write puts the new values in force at once: no restart.
func TestConnectionSettingsApplyAtOnce(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	viaProxy := func(r *http.Request) {
		r.RemoteAddr = "192.0.2.20:4000"
		r.Header.Set("X-Forwarded-Proto", "https")
	}
	login := func() bool {
		rec := h.do("POST", "/api/auth/login", loginBody(testPass), viaProxy)
		require.Equal(t, http.StatusNoContent, rec.Code)
		return rec.Result().Cookies()[0].Secure
	}
	require.False(t, login(), "an unlisted proxy cannot claim https")
	require.NotContains(t, h.srv.outgoingUA(), "+https://")
	_, about, _ := h.api(c, "GET", "/api/about", "")
	require.Equal(t, false, about["public_url_set"])

	code, _ := patchIssue(t, h, c, `{"security.trusted_proxies":["192.0.2.20"],"server.public_url":"https://rss.example.com"}`, "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, login(), "the proxy is trusted at once")
	require.Contains(t, h.srv.outgoingUA(), "; +https://rss.example.com)")
	_, about, _ = h.api(c, "GET", "/api/about", "")
	require.Equal(t, true, about["public_url_set"])
	require.Contains(t, h.srv.reach.HostNames(), "rss.example.com", "the public URL's host is an allowed name")

	// A reset (null) is the default: off again.
	code, _ = patchIssue(t, h, c, `{"security.trusted_proxies":null,"server.public_url":null}`, "")
	require.Equal(t, http.StatusOK, code)
	require.False(t, login())
	require.NotContains(t, h.srv.outgoingUA(), "+https://")
	require.Empty(t, h.srv.reach.HostNames())

	// Access on and off.
	require.Equal(t, false, h.me(withCookie(c))["access_enabled"])
	code, _ = patchIssue(t, h, c, `{"security.cloudflare_access":{"team_domain":"`+accTeam+`","aud":"`+accAUD+`"}}`, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, h.me(withCookie(c))["access_enabled"])
	code, _ = patchIssue(t, h, c, `{"security.cloudflare_access":{}}`, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, h.me(withCookie(c))["access_enabled"])
}

// Access cannot be changed or turned off while the account signs in through it
// (no web password): that would lock the owner out or hand sign-in to another
// team. Other settings still save, and so does the same Access value.
func TestAccessChangeRefusedWhileItIsTheSignIn(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, _ := accessKeys(t)
	rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := sessionCookie(t, rec)
	for _, body := range []string{
		`{"security.cloudflare_access":{}}`,
		`{"security.cloudflare_access":null}`,
		`{"security.cloudflare_access":{"team_domain":"other.cloudflareaccess.com","aud":"` + accAUD + `"}}`,
		`{"security.cloudflare_access":{"team_domain":"` + accTeam + `","aud":"other"},"server.public_url":"https://rss.example.com"}`,
	} {
		rec := h.do("PATCH", "/api/settings", body, withCookie(c))
		require.Equal(t, http.StatusConflict, rec.Code, body+": "+rec.Body.String())
		require.Equal(t, "access_in_use", decode(t, rec)["error"])
	}
	require.NotNil(t, h.srv.reach.Access(), "still on")
	require.Equal(t, "", h.srv.reach.PublicURL(), "a refused write writes nothing")
	same := `{"security.cloudflare_access":{"team_domain":"` + accTeam + `","aud":"` + accAUD + `"},"server.public_url":"https://rss.example.com"}`
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", same, withCookie(c)).Code)
	require.Equal(t, "https://rss.example.com", h.srv.reach.PublicURL())

	// With a web password again, Access can go.
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "web-hash", store.AuthStandard, ""))
	c = h.login()
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", `{"security.cloudflare_access":{}}`, withCookie(c)).Code)
	require.Nil(t, h.srv.reach.Access())
}

// The public URL's host opens in open mode once an authenticated write sets it,
// like a listed name.
func TestOpenModeAnswersThePublicURLHost(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	const name = "reader.example.net"
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host(name)).Code)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"server.public_url":"https://`+name+`"}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(name)).Code)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"server.public_url":""}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host(name)).Code)
}
