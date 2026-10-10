package greader

// Conformance: sign-in, tokens, authentication failures, methods and content
// types. See conformance_test.go for the reference tags.

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
)

func TestConformanceClientLogin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := newConf(t, h)
	tok := c.token

	t.Run("form POST answers SID, LSID and Auth lines", func(t *testing.T) {
		// [GR] ClientLogin: text/plain key=value lines; Auth is the token. [RS] LSID=null, same keys.
		r := c.call(http.MethodPost, "/accounts/ClientLogin", loginBody(), map[string]string{"Authorization": ""})
		require.Equal(t, 200, r.code)
		require.Contains(t, r.header.Get("Content-Type"), "text/plain")
		kv := parseLoginLines(t, r.body)
		require.Equal(t, tok, kv["Auth"])
		require.Equal(t, tok, kv["SID"], "[RS] SID carries the same token")
		require.Contains(t, kv, "LSID")
		require.NotContains(t, tok, "=", "the token never holds '=', so a line split on every '=' still has two parts")
	})

	t.Run("GET with query parameters", func(t *testing.T) {
		// [RS] Email/Passwd are read from POST or GET.
		r := c.call(http.MethodGet, "/accounts/ClientLogin?"+loginBody(), "", map[string]string{"Authorization": ""})
		require.Equal(t, 200, r.code)
		require.Equal(t, tok, parseLoginLines(t, r.body)["Auth"])
	})

	t.Run("POST with parameters in the query string", func(t *testing.T) {
		// [RS] POST or GET, merged form values.
		r := c.call(http.MethodPost, "/accounts/ClientLogin?"+loginBody(), "", map[string]string{"Authorization": ""})
		require.Equal(t, 200, r.code)
		require.Equal(t, tok, parseLoginLines(t, r.body)["Auth"])
	})

	t.Run("multipart form", func(t *testing.T) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		require.NoError(t, mw.WriteField("Email", testUser))
		require.NoError(t, mw.WriteField("Passwd", testPass))
		require.NoError(t, mw.Close())
		r := c.call(http.MethodPost, "/accounts/ClientLogin", buf.String(), map[string]string{"Authorization": "", "Content-Type": mw.FormDataContentType()})
		require.Equal(t, 200, r.code, r.body)
		require.Equal(t, tok, parseLoginLines(t, r.body)["Auth"])
	})

	t.Run("form with no or another content type", func(t *testing.T) {
		// [K §6.2] every non-multipart POST body is parsed as urlencoded.
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded; charset=UTF-8", "application/octet-stream"} {
			r := c.rawPost("/accounts/ClientLogin", loginBody(), ct)
			require.Equal(t, 200, r.code, "Content-Type %q", ct)
		}
	})

	t.Run("root path without the mount prefix", func(t *testing.T) {
		// [RS] ClientLogin lives at BASE_URL/accounts/ClientLogin; [K §6.1] the root forms answer too.
		root := &confClient{client: newClient(t, h, "", confUA), h: h}
		r := root.call(http.MethodPost, "/accounts/ClientLogin", loginBody(), nil)
		require.Equal(t, 200, r.code)
		require.Equal(t, tok, parseLoginLines(t, r.body)["Auth"])
	})

	t.Run("output=json", func(t *testing.T) {
		// [RS] JSON with SID, LSID, Auth when output=json.
		r := c.call(http.MethodPost, "/accounts/ClientLogin", loginBody()+"&output=json", map[string]string{"Authorization": ""})
		require.Equal(t, 200, r.code)
		require.Contains(t, r.header.Get("Content-Type"), "application/json")
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(r.body), &m))
		require.Equal(t, tok, m["Auth"])
		require.Equal(t, tok, m["SID"])
		require.Contains(t, m, "LSID")
	})

	t.Run("email is case-insensitive", func(t *testing.T) {
		r := c.call(http.MethodPost, "/accounts/ClientLogin", "Email="+strings.ToUpper(testUser)+"&Passwd="+url.QueryEscape(testPass), map[string]string{"Authorization": ""})
		require.Equal(t, 200, r.code)
	})

	t.Run("failures are 401 Error=BadAuthentication", func(t *testing.T) {
		// [GR] failure body Error=BadAuthentication; [RS] status 401 (Google answered 403; see
		// docs/compatibility.md). Never a redirect, never 429.
		for _, body := range []string{
			"Email=" + testUser + "&Passwd=wrong",
			"Email=someone-else&Passwd=" + url.QueryEscape(testPass),
			"Email=" + testUser,
			"Passwd=" + url.QueryEscape(testPass),
			"",
		} {
			r := c.call(http.MethodPost, "/accounts/ClientLogin", body, map[string]string{"Authorization": ""})
			require.Equal(t, 401, r.code, body)
			require.True(t, strings.HasPrefix(r.body, "Error=BadAuthentication"), body)
			require.Equal(t, "true", r.header.Get("Google-Bad-Token"))
		}
	})
}

// rawPost posts body with exactly the given Content-Type ("" sends none) and no Authorization.
func (c *confClient) rawPost(path, body, ctype string) resp {
	c.t.Helper()
	req, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(body))
	require.NoError(c.t, err)
	req.Header.Set("User-Agent", confUA)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(res.Body)
	return resp{res.StatusCode, res.Header, b.String()}
}

func TestConformanceTokenAndUserInfo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := newConf(t, h)

	// [GR] GET /reader/api/0/token: the edit token as plain text with a trailing newline.
	// [RS] the edit token is the auth token.
	r := c.call(http.MethodGet, rd+"token", "", nil)
	require.Equal(t, 200, r.code)
	require.Contains(t, r.header.Get("Content-Type"), "text/plain")
	require.Equal(t, c.token, strings.TrimSpace(r.body))

	// [RS] user-info: userId, userName, userProfileId, userEmail as strings.
	m := c.getJSON(rd + "user-info")
	for _, k := range []string{"userId", "userName", "userProfileId", "userEmail"} {
		require.IsType(t, "", m[k], k)
	}
	require.Equal(t, testUser, m["userName"])
}

func TestConformanceAuthFailures(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)
	tok := c.token
	edit := "i=" + l.dec("tech-unread") + "&a=" + url.QueryEscape(stateStarred)

	unauthorized := requireUnauthorized

	t.Run("GET without, or with a wrong, Authorization header", func(t *testing.T) {
		for name, hdr := range map[string]string{
			"absent":       "",
			"wrong token":  "GoogleLogin auth=" + testUser + "/" + strings.Repeat("0", 64),
			"no auth= key": "GoogleLogin " + tok,
			"bearer":       "Bearer " + tok,
		} {
			h := map[string]string{"Authorization": hdr}
			if hdr == "" {
				h = nil
			}
			cc := *c.client
			cc.auth = hdr
			r := cc.doAny(http.MethodGet, rd+"subscription/list?output=json", "", h)
			unauthorized(t, r, name)
		}
	})

	t.Run("GET never authenticates by a T in the query", func(t *testing.T) {
		// [RS] GET requests do not accept the token from the query string; [K §6.3].
		cc := *c.client
		cc.auth = ""
		unauthorized(t, cc.doAny(http.MethodGet, rd+"subscription/list?T="+url.QueryEscape(tok), "", nil), "T on GET")
	})

	t.Run("POST authenticates by T alone, in the body or the query", func(t *testing.T) {
		// [RS] POST requests are authenticated with T from the merged form values.
		cc := *c.client
		cc.auth = ""
		r := cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T="+url.QueryEscape(tok), nil)
		require.Equal(t, 200, r.code, r.body)
		require.Equal(t, "OK", r.body)
		r = cc.doAny(http.MethodPost, rd+"edit-tag?T="+url.QueryEscape(tok), edit, nil)
		require.Equal(t, 200, r.code, r.body)
		unauthorized(t, cc.doAny(http.MethodPost, rd+"edit-tag", edit, nil), "POST with neither header nor T")
		unauthorized(t, cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T=wrong", nil), "POST with a wrong T")
		// Trimming never turns a blank T into a credential [K §6.3].
		unauthorized(t, cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T=%20%0A", nil), "POST with a whitespace-only T")
		unauthorized(t, cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T=", nil), "POST with an empty T")
		unauthorized(t, cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T=x", nil), "POST with T=x and no header")
	})

	t.Run("T exactly as GET token returned it", func(t *testing.T) {
		// [GR] the token endpoint ends its body with a newline; [RS] trims T before comparing, so a
		// client that sends the body verbatim is accepted.
		body := c.call(http.MethodGet, rd+"token", "", nil).body
		cc := *c.client
		cc.auth = ""
		r := cc.doAny(http.MethodPost, rd+"edit-tag", edit+"&T="+url.QueryEscape(body), nil)
		require.Equal(t, 200, r.code, r.body)
	})

	t.Run("header plus T: T must be the token, empty or x", func(t *testing.T) {
		// [RS] checkToken accepts "" and "x" for an authenticated user.
		for _, T := range []string{tok, "", "x"} {
			r := c.call(http.MethodPost, rd+"edit-tag", edit+"&T="+url.QueryEscape(T), nil)
			require.Equal(t, 200, r.code, "T=%q", T)
		}
		r := c.call(http.MethodPost, rd+"edit-tag", edit, nil)
		require.Equal(t, 200, r.code, "no T at all with a valid header")
		unauthorized(t, c.call(http.MethodPost, rd+"edit-tag", edit+"&T=stale", nil), "header with a wrong T")
	})

}

// requireUnauthorized checks the answer to a missing, wrong or revoked credential.
func requireUnauthorized(t *testing.T, r resp, what string) {
	t.Helper()
	// [GR] 401 with X-Reader-Google-Bad-Token so the client signs in again; [RS] Google-Bad-Token and
	// "Unauthorized!"; [RS] X-Reader-Google-Bad-Token. Never 403.
	require.Equal(t, 401, r.code, what)
	require.Equal(t, "true", r.header.Get("X-Reader-Google-Bad-Token"), what)
	require.Equal(t, "true", r.header.Get("Google-Bad-Token"), what)
	require.Contains(t, r.header.Get("Content-Type"), "text/plain", what)
	require.Equal(t, "Unauthorized!", r.body, what)
}

func TestConformanceRevokedTokenUntilSignInAgain(t *testing.T) {
	t.Parallel()
	// Tokens do not expire on a timer; changing the API password revokes every token at once, and
	// the client gets a working token by signing in with the new password [K §6.3].
	const newPass, newHash = "a-new-api-password", "another-hash"
	h := newHarness(t)
	l := seedConf(h)
	h.api.ver = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Check: func(pw, phc string) bool {
		return (pw == testPass && phc == testHash) || (pw == newPass && phc == newHash)
	}})
	c := newConf(t, h)
	old := c.token
	edit := "i=" + l.dec("tech-unread") + "&a=" + url.QueryEscape(stateStarred)

	require.NoError(t, h.db.SetAPIPasswordHash(context.Background(), newHash))
	h.api.InvalidateAccount()
	requireUnauthorized(t, c.call(http.MethodGet, rd+"subscription/list", "", nil), "old token on GET")
	requireUnauthorized(t, c.call(http.MethodPost, rd+"edit-tag", edit+"&T="+url.QueryEscape(old), nil), "old token on POST")
	r := c.call(http.MethodPost, "/accounts/ClientLogin", loginBody(), map[string]string{"Authorization": ""})
	require.Equal(t, 401, r.code, "the old password no longer signs in")

	r = c.call(http.MethodPost, "/accounts/ClientLogin", "Email="+testUser+"&Passwd="+url.QueryEscape(newPass), map[string]string{"Authorization": ""})
	require.Equal(t, 200, r.code, r.body)
	tok := parseLoginLines(t, r.body)["Auth"]
	require.NotEmpty(t, tok)
	require.NotEqual(t, old, tok)
	c.auth = "GoogleLogin auth=" + tok
	require.Equal(t, 200, c.call(http.MethodGet, rd+"subscription/list", "", nil).code)
	require.Equal(t, 200, c.call(http.MethodPost, rd+"edit-tag", edit+"&T="+url.QueryEscape(tok), nil).code)
}

func TestConformanceAPIDisabled(t *testing.T) {
	t.Parallel()
	// With no API password set, sign-in and every endpoint answer 401 (never 403 or 404).
	h := newHarness(t, harnessOpts{noAPIPassword: true})
	c := &confClient{client: newClient(t, h, base, confUA), h: h}
	r := c.call(http.MethodPost, "/accounts/ClientLogin", loginBody(), nil)
	require.Equal(t, 401, r.code)
	c.auth = "GoogleLogin auth=" + makeToken(testUser, testSecret, testHash)
	require.Equal(t, 401, c.call(http.MethodGet, rd+"token", "", nil).code)
}

func TestConformanceMethodsAndContentTypes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	t.Run("probes", func(t *testing.T) {
		// [RS] an empty path answers OK; /check/compatibility answers PASS.
		cc := *c.client
		cc.auth = ""
		r := cc.doAny(http.MethodGet, "", "", nil)
		require.Equal(t, 200, r.code)
		require.Equal(t, "OK", r.body)
		r = cc.doAny(http.MethodGet, "/check/compatibility", "", nil)
		require.Equal(t, "PASS", r.body)
	})

	t.Run("write endpoints answer 405 to GET", func(t *testing.T) {
		for _, name := range []string{"edit-tag", "mark-all-as-read", "subscription/quickadd", "subscription/edit", "rename-tag", "disable-tag", "subscription/import"} {
			r := c.call(http.MethodGet, rd+name, "", nil)
			require.Equal(t, 405, r.code, name)
			require.Equal(t, "POST", r.header.Get("Allow"), name)
		}
	})

	t.Run("read endpoints also answer POST with parameters in the body", func(t *testing.T) {
		// Some clients POST every call; parameters may then come in the body or the query [RS].
		r := c.call(http.MethodPost, rd+"stream/items/ids", "s="+url.QueryEscape(feedID(l.tech))+"&n=50", nil)
		ids, _ := itemRefs(t, r)
		require.Equal(t, l.decs("tech-new", "tech-unread", "tech-old-read"), ids)
		r = c.call(http.MethodPost, rd+"subscription/list?output=json", "", nil)
		require.Equal(t, 200, r.code)
		r = c.call(http.MethodPost, rd+"stream/contents/"+feedID(l.loose), "n=5", nil)
		require.Len(t, decodeStream(t, r).Items, 1)
	})

	t.Run("form bodies whatever the content type", func(t *testing.T) {
		id := l.dec("loose-old")
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded; charset=UTF-8", "application/octet-stream"} {
			require.NoError(t, execSQL(h, "UPDATE items SET starred = 0 WHERE id = ?", l.ids["loose-old"]))
			body := "i=" + id + "&a=" + url.QueryEscape(stateStarred) + "&T=" + url.QueryEscape(c.token)
			r := c.rawPost(rd+"edit-tag", body, ct)
			require.Equal(t, 200, r.code, "Content-Type %q: %s", ct, r.body)
			require.True(t, isStarred(h, l.ids["loose-old"]), "Content-Type %q", ct)
		}
	})

	t.Run("multipart write", func(t *testing.T) {
		require.NoError(t, execSQL(h, "UPDATE items SET starred = 0 WHERE id = ?", l.ids["loose-old"]))
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		require.NoError(t, mw.WriteField("i", l.dec("loose-old")))
		require.NoError(t, mw.WriteField("a", stateStarred))
		require.NoError(t, mw.WriteField("T", c.token))
		require.NoError(t, mw.Close())
		r := c.call(http.MethodPost, rd+"edit-tag", buf.String(), map[string]string{"Content-Type": mw.FormDataContentType()})
		require.Equal(t, 200, r.code, r.body)
		require.True(t, isStarred(h, l.ids["loose-old"]))
	})

	t.Run("unknown endpoints are an empty JSON array after auth", func(t *testing.T) {
		// [RS] catch-all: 200 []. A client probing an optional endpoint never sees an error.
		r := c.call(http.MethodGet, rd+"preference/stream/list?output=json", "", nil)
		require.Equal(t, 200, r.code)
		require.Equal(t, "[]", r.body)
	})

	t.Run("ignored parameters", func(t *testing.T) {
		// [GR] ck (cache buster) and client are informational; output on JSON endpoints is JSON either way.
		for _, extra := range []string{"ck=1790251200", "client=anything", "output=json", "output=xml"} {
			m := c.getJSON(rd + "tag/list?" + extra)
			require.NotEmpty(t, m["tags"], extra)
		}
	})
}
