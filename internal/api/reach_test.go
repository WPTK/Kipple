package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/access"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/setup"
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

// withCurrent adds the current web password a guarded write carries.
func withCurrent(body string) string {
	return strings.Replace(body, "{", `{"current":"`+testPass+`",`, 1)
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

	code, _ := patchIssue(t, h, c, withCurrent(`{"server.public_url":" https://rss.example.com/kipple ",
		"security.trusted_proxies":["192.0.2.10"," 198.51.100.7/24","::ffff:192.0.2.10","2001:db8::/32","10.0.0.0/8","fc00::/7"],
		"security.cloudflare_access":{"team_domain":"https://MyTeam.cloudflareaccess.com/","aud":" abc123 "}}`), "")
	require.Equal(t, http.StatusOK, code)
	v = values()
	require.Equal(t, "https://rss.example.com/kipple", v[store.SettingPublicURL])
	require.Equal(t, []any{"192.0.2.10", "198.51.100.0/24", "2001:db8::/32", "10.0.0.0/8", "fc00::/7"}, v[store.SettingTrustedProxies], "normalized, repeats dropped")
	require.Equal(t, map[string]any{"team_domain": "myteam.cloudflareaccess.com", "aud": "abc123"}, v[store.SettingCloudflareAccess])

	// An internationalized host is stored in its xn-- form, so the Host gate and the User-Agent agree.
	code, _ = patchIssue(t, h, c, `{"server.public_url":"https://bücher.example:8443/r"}`, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "https://xn--bcher-kva.example:8443/r", values()[store.SettingPublicURL])
	require.Equal(t, "xn--bcher-kva.example", h.srv.reach.Get().PublicHost)
	require.Contains(t, h.srv.outgoingUA(), "+https://xn--bcher-kva.example:8443/r)")

	// Empty values turn each one off.
	code, _ = patchIssue(t, h, c, withCurrent(`{"server.public_url":"","security.trusted_proxies":[],"security.cloudflare_access":{"team_domain":" ","aud":""}}`), "")
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
		{store.SettingPublicURL, `"http://*.example.com"`, "one name or IP address, such as rss.example.com (no wildcards)"},
		{store.SettingPublicURL, `"http://*.home:1919"`, "no wildcards"},
		{store.SettingTrustedProxies, `"192.0.2.10"`, "list"},
		{store.SettingTrustedProxies, `["192.0.2.10,192.0.2.11"]`, "list"},
		{store.SettingTrustedProxies, `["proxy.example.com"]`, `"proxy.example.com" is not an IP address or a range`},
		{store.SettingTrustedProxies, `["10.0.0.0/33"]`, `"10.0.0.0/33" is not an IP address or a range`},
		{store.SettingTrustedProxies, `["0.0.0.0/0"]`, "too wide to trust"},
		{store.SettingTrustedProxies, `["10.0.0.0/7"]`, "too wide to trust"},
		{store.SettingTrustedProxies, `["::/0"]`, "too wide to trust"},
		{store.SettingTrustedProxies, `["2000::/3"]`, "too wide to trust"},
		{store.SettingTrustedProxies, `["2001::/16"]`, "too wide to trust"},
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
		require.NotContains(t, msg, "ParsePrefix", "no parser wording reaches the person")
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

// The trusted proxies and Cloudflare Access decide who may name the client's
// address and who may sign in, so a write proves the account like an account
// change: a session alone is refused, a wrong password is refused and counted.
// The public URL and the allowed names need only the session.
func TestGuardedConnectionSettingsNeedTheCurrentPassword(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, body := range []string{
		`{"security.trusted_proxies":["192.0.2.10"]}`,
		`{"security.cloudflare_access":{"team_domain":"` + accTeam + `","aud":"` + accAUD + `"}}`,
		`{"security.trusted_proxies":null}`,
		`{"current":"wrong-password","security.trusted_proxies":["192.0.2.10"]}`,
	} {
		rec := h.do("PATCH", "/api/settings", body, withCookie(c))
		require.Equal(t, http.StatusForbidden, rec.Code, body)
		require.Equal(t, "bad_password", decode(t, rec)["error"], body)
	}
	require.Empty(t, h.srv.reach.Trusted())
	require.Nil(t, h.srv.reach.Access())
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", `{"server.public_url":"https://rss.example.com","security.allowed_hosts":["rss.example.org"]}`, withCookie(c)).Code)
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", withCurrent(`{"security.trusted_proxies":["192.0.2.10"]}`), withCookie(c)).Code)
	require.Len(t, h.srv.reach.Trusted(), 1)
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

	code, _ := patchIssue(t, h, c, withCurrent(`{"security.trusted_proxies":["192.0.2.20"],"server.public_url":"https://rss.example.com"}`), "")
	require.Equal(t, http.StatusOK, code)
	require.True(t, login(), "the proxy is trusted at once")
	require.Contains(t, h.srv.outgoingUA(), "; +https://rss.example.com)")
	_, about, _ = h.api(c, "GET", "/api/about", "")
	require.Equal(t, true, about["public_url_set"])
	require.Equal(t, "rss.example.com", h.srv.reach.Get().PublicHost, "the public URL's host is a Host gate name")

	// A reset (null) is the default, off again, and stored as such: the row stays,
	// so a seed variable can never fill it again.
	code, _ = patchIssue(t, h, c, withCurrent(`{"security.trusted_proxies":null,"server.public_url":null}`), "")
	require.Equal(t, http.StatusOK, code)
	require.False(t, login())
	require.NotContains(t, h.srv.outgoingUA(), "+https://")
	require.Empty(t, h.srv.reach.Get().PublicHost)
	stored, err := h.db.StoredSettings(context.Background(), store.ReachKeys)
	require.NoError(t, err)
	require.True(t, stored[store.SettingTrustedProxies])
	require.True(t, stored[store.SettingPublicURL])
	ignored, err := reach.SeedSettings(context.Background(), h.db, reach.Seed{PublicURL: "https://seed.example.com"})
	require.NoError(t, err)
	require.Equal(t, []string{store.SettingPublicURL}, ignored, "the reset value wins over the seed")

	// Access on and off.
	require.Equal(t, false, h.me(withCookie(c))["access_enabled"])
	code, _ = patchIssue(t, h, c, withCurrent(`{"security.cloudflare_access":{"team_domain":"`+accTeam+`","aud":"`+accAUD+`"}}`), "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, h.me(withCookie(c))["access_enabled"])
	code, _ = patchIssue(t, h, c, withCurrent(`{"security.cloudflare_access":{}}`), "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, h.me(withCookie(c))["access_enabled"])
}

// Access cannot be changed or turned off while the account signs in through it
// (no web password): that would lock the owner out or hand sign-in to another
// team. Other settings still save, and so does the same Access value. Without a
// password the proof for the write is a verified Access token.
func TestAccessChangeRefusedWhileItIsTheSignIn(t *testing.T) {
	h := newHarness(t, withAccess(t))
	h.dropPassword()
	k, _ := accessKeys(t)
	rec := h.do("POST", "/api/auth/login", passwordlessLogin(testUser), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := sessionCookie(t, rec)
	// The session alone is not proof.
	rec = h.do("PATCH", "/api/settings", `{"security.trusted_proxies":["192.0.2.10"]}`, withCookie(c))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "access_required", decode(t, rec)["error"])
	for _, body := range []string{
		`{"security.cloudflare_access":{}}`,
		`{"security.cloudflare_access":null}`,
		`{"security.cloudflare_access":{"team_domain":"other.cloudflareaccess.com","aud":"` + accAUD + `"}}`,
		`{"security.cloudflare_access":{"team_domain":"` + accTeam + `","aud":"other"},"server.public_url":"https://rss.example.com"}`,
	} {
		rec := h.do("PATCH", "/api/settings", body, withCookie(c), withJWT(h.jwt(k, nil)))
		require.Equal(t, http.StatusConflict, rec.Code, body+": "+rec.Body.String())
		require.Equal(t, "access_in_use", decode(t, rec)["error"])
	}
	require.NotNil(t, h.srv.reach.Access(), "still on")
	require.Equal(t, "", h.srv.reach.PublicURL(), "a refused write writes nothing")
	same := `{"security.cloudflare_access":{"team_domain":"` + accTeam + `","aud":"` + accAUD + `"},"server.public_url":"https://rss.example.com"}`
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", same, withCookie(c), withJWT(h.jwt(k, nil))).Code)
	require.Equal(t, "https://rss.example.com", h.srv.reach.PublicURL())

	// With a web password again, Access can go.
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "web-hash", store.AuthStandard, ""))
	c = h.login()
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", withCurrent(`{"security.cloudflare_access":{}}`), withCookie(c)).Code)
	require.Nil(t, h.srv.reach.Access())
}

// Removing the web password and turning Access off at the same moment can never
// leave an account with neither. The settings write lands in the worst place:
// after the removal was proven (with Access on) and before its write. The
// removal re-checks Access under the lock the settings write holds, so it is
// refused. Without that re-check this test fails.
func TestPasswordRemovalAndAccessOffNeverBothWin(t *testing.T) {
	k, _ := accessKeys(t)
	h := newHarness(t, withAccess(t))
	ca := h.login()
	cb := h.login()
	h.srv.removeProved = func() {
		rec := h.do("PATCH", "/api/settings", withCurrent(`{"security.cloudflare_access":{}}`), withCookie(cb), peer("10.20.30.12:5555"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	rec := h.do("POST", "/api/account/password", `{"current":"`+testPass+`","remove":true}`, withCookie(ca), withJWT(h.jwt(k, nil)), peer("10.20.30.11:5555"))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Equal(t, "access_not_configured", decode(t, rec)["error"])
	acct, _, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, acct.PasswordHash, "the password stays")
	require.Nil(t, h.srv.reach.Access())

	// And concurrently, many times (meaningful under -race in CI): never both.
	for i := 0; i < 10; i++ {
		h := newHarness(t, withAccess(t))
		ca, cb := h.login(), h.login()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.do("POST", "/api/account/password", `{"current":"`+testPass+`","remove":true}`, withCookie(ca), withJWT(h.jwt(k, nil)), peer("10.20.30.11:5555"))
		}()
		go func() {
			defer wg.Done()
			h.do("PATCH", "/api/settings", withCurrent(`{"security.cloudflare_access":{}}`), withCookie(cb), peer("10.20.30.12:5555"))
		}()
		wg.Wait()
		acct, _, err := h.db.Account(context.Background())
		require.NoError(t, err)
		require.False(t, acct.PasswordHash == "" && h.srv.reach.Access() == nil, "run %d: no password and Access off", i)
	}
}

// The removal is bound to the Access setting it was proven under: a write that
// points Access at another team between the proof and the write makes the
// removal fail (409 access_changed), and the password stays.
func TestPasswordRemovalRefusedWhenAccessChangesUnderIt(t *testing.T) {
	k, _ := accessKeys(t)
	h := newHarness(t, withAccess(t))
	ca, cb := h.login(), h.login()
	h.srv.removeProved = func() {
		rec := h.do("PATCH", "/api/settings", withCurrent(`{"security.cloudflare_access":{"team_domain":"other.cloudflareaccess.com","aud":"`+accAUD+`"}}`), withCookie(cb), peer("10.20.30.12:5555"))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	rec := h.do("POST", "/api/account/password", `{"current":"`+testPass+`","remove":true}`, withCookie(ca), withJWT(h.jwt(k, nil)), peer("10.20.30.11:5555"))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Equal(t, "access_changed", decode(t, rec)["error"])
	acct, _, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, acct.PasswordHash)
}

// Once Access is off, removing the password is refused, even with a token that
// was verified before (the check is made again, under the lock, at the write).
func TestPasswordRemovalRefusedOnceAccessIsOff(t *testing.T) {
	h := newHarness(t, withAccess(t))
	c := h.login()
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", withCurrent(`{"security.cloudflare_access":{}}`), withCookie(c)).Code)
	k, _ := accessKeys(t)
	rec := h.do("POST", "/api/account/password", `{"current":"`+testPass+`","remove":true}`, withCookie(c), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "access_not_configured", decode(t, rec)["error"])
	acct, _, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, acct.PasswordHash)
}

// A LAN name is a fine public URL (sync apps on the network get icons from it),
// but open mode does not answer it unless it is listed by name (#254), as the
// refusal says.
func TestLANPublicURLDoesNotWidenOpenMode(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	for _, u := range []string{"http://nas.local:1919", "http://unraid:1919", "https://rss.home.arpa"} {
		rec := h.req("PATCH", "/api/settings", `{"server.public_url":"`+u+`"}`, withCookies(sess))
		require.Equal(t, http.StatusOK, rec.Code, u+": "+rec.Body.String())
		require.Equal(t, u, h.srv.reach.PublicURL())
		require.Empty(t, h.srv.reach.HostNames(), u)
	}
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("rss.home.arpa")).Code)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.allowed_hosts":["rss.home.arpa"]}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("rss.home.arpa")).Code, "listed by name, it is answered")
}

// Setup mode answers the public URL's host whatever its zone (the operator
// typed it, for example in KIPPLE_PUBLIC_URL), including router and LAN zones
// setup mode does not answer by shape; open mode does not (#254).
func TestSetupModeAnswersThePublicURLHost(t *testing.T) {
	for _, name := range []string{"kipple.fritz.box", "rss.home", "rss.corp", "nas.localdomain", "nas.local", "reader.example.net"} {
		h := newSetupHarness(t, func(o *Options) {
			o.Reach = reach.Fixed(reach.State{PublicURL: "http://" + name + ":1919", PublicHost: name})
		})
		require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(name+":1919")).Code, name)
		require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("other.fritz.box")).Code, name)
		h.openAccount(nil)
		want := http.StatusOK
		if setup.LANClaimable(name) {
			want = http.StatusMisdirectedRequest
		}
		require.Equal(t, want, h.req("GET", "/api/instance", "", host(name+":1919")).Code, "open mode: %s", name)
	}
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

// In open mode the open gate refuses a trusted peer as forwarded, so a list
// that names the caller's own address would lock the caller out: refused, and
// nothing is written. A list without it saves, from where open mode works.
func TestOpenModeTrustedProxiesCannotLockTheCallerOut(t *testing.T) {
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	for _, list := range []string{`["127.0.0.1"]`, `["192.0.2.10","127.0.0.0/8"]`, `["::ffff:127.0.0.1"]`} {
		rec := h.req("PATCH", "/api/settings", `{"security.trusted_proxies":`+list+`}`, withCookies(sess))
		require.Equal(t, http.StatusConflict, rec.Code, list)
		require.Equal(t, "proxy_is_you", decode(t, rec)["error"])
	}
	require.Empty(t, h.srv.reach.Trusted())
	require.Equal(t, http.StatusOK, h.req("GET", "/api/bootstrap", "", withCookies(sess)).Code, "still signed in")
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.trusted_proxies":["192.0.2.10"]}`, withCookies(sess)).Code)
	require.Len(t, h.srv.reach.Trusted(), 1)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.trusted_proxies":null}`, withCookies(sess)).Code, "a reset can never name the caller")
	// From a proxied request, open mode refuses the write at the gate.
	rec := h.req("PATCH", "/api/settings", `{"security.trusted_proxies":["192.0.2.10"]}`, withCookies(sess), hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
