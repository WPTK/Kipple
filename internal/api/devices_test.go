package api

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func deviceCookieOf(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == deviceCookieName {
			return c
		}
	}
	return nil
}

// dev is a logged-in browser: a session cookie plus (once issued) a device cookie.
type dev struct {
	h    *harness
	sess *http.Cookie
	dc   *http.Cookie
}

func (h *harness) newDev() *dev { return &dev{h: h, sess: h.login()} }

func (d *dev) call(method, path, body string, mod ...func(*http.Request)) (int, map[string]any, *httptest.ResponseRecorder) {
	d.h.t.Helper()
	mods := []func(*http.Request){func(r *http.Request) {
		if d.dc != nil {
			r.AddCookie(&http.Cookie{Name: d.dc.Name, Value: d.dc.Value})
		}
	}}
	code, out, rec := d.h.api(d.sess, method, path, body, append(mods, mod...)...)
	if c := deviceCookieOf(rec); c != nil {
		d.dc = c
	}
	return code, out, rec
}

func TestDeviceCookieIssuedOnFirstBootstrap(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	require.Nil(t, deviceCookieOf(h.do("POST", "/api/auth/login", loginBody(testPass))), "login does not issue it")

	code, out, rec := h.api(sess, "GET", "/api/bootstrap", "")
	require.Equal(t, http.StatusOK, code)
	c := deviceCookieOf(rec)
	require.NotNil(t, c)
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.Equal(t, "/", c.Path)
	require.Equal(t, 400*24*3600, c.MaxAge)
	require.False(t, c.Secure)
	require.GreaterOrEqual(t, len(c.Value), 22)
	require.True(t, store.ValidDeviceID(c.Value))

	dv := out["device"].(map[string]any)
	require.Equal(t, c.Value, dv["id"])
	require.Equal(t, "", dv["name"])
	require.Equal(t, map[string]any{}, dv["profile"])
	merged := dv["merged"].(map[string]any)
	require.Equal(t, "system", merged["ui.theme"])
	require.Equal(t, "magazine", merged["client.layout"])

	// With the cookie sent back nothing new is issued and no device is added.
	code, out, rec = h.api(sess, "GET", "/api/bootstrap", "", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value}) })
	require.Equal(t, http.StatusOK, code)
	require.Nil(t, deviceCookieOf(rec))
	require.Equal(t, c.Value, out["device"].(map[string]any)["id"])
	list, err := h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestDeviceCookieSecureOverHTTPS(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	_, _, rec := h.api(sess, "GET", "/api/device", "", func(r *http.Request) { r.TLS = &tls.ConnectionState{} })
	c := deviceCookieOf(rec)
	require.NotNil(t, c)
	require.True(t, c.Secure)
}

func TestUnknownOrMalformedDeviceCookieGetsAFreshDevice(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	for _, v := range []string{"AAAAAAAAAAAAAAAAAAAAAA", "short", "not valid!!not valid!!"} {
		_, out, rec := h.api(sess, "GET", "/api/device", "", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: deviceCookieName, Value: v}) })
		c := deviceCookieOf(rec)
		require.NotNil(t, c, v)
		require.NotEqual(t, v, c.Value, "the client never picks its own id")
		require.Equal(t, c.Value, out["id"])
	}
	_, ok, _ := h.db.GetDevice(t.Context(), "AAAAAAAAAAAAAAAAAAAAAA")
	require.False(t, ok)
}

func TestDeviceLastSeenSlidesDaily(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	_, out, _ := d.call("GET", "/api/device", "")
	first := out["last_seen_at"].(float64)
	h.clk.Advance(2 * time.Hour)
	_, out, rec := d.call("GET", "/api/device", "")
	require.Nil(t, deviceCookieOf(rec))
	require.Equal(t, first, out["last_seen_at"])
	h.clk.Advance(23 * time.Hour)
	_, out, rec = d.call("GET", "/api/device", "")
	require.NotNil(t, deviceCookieOf(rec), "cookie expiry slides when last_seen is bumped")
	require.Greater(t, out["last_seen_at"].(float64), first)
}

func TestDeviceResolutionOrder(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	get := func() (defaults, profile, merged map[string]any) {
		_, out, _ := d.call("GET", "/api/device", "")
		return out["defaults"].(map[string]any), out["profile"].(map[string]any), out["merged"].(map[string]any)
	}
	// 1. built-in default
	def, prof, merged := get()
	require.Equal(t, "system", merged["ui.theme"])
	require.Equal(t, "standard", merged["ui.list_density"])
	require.EqualValues(t, 240, merged["client.sidebar_width"])
	require.NotContains(t, merged, "client.link_target", "no built-in default: the client decides")
	require.Empty(t, prof)
	require.Equal(t, def["ui.theme"], merged["ui.theme"])
	// 2. server default (the global settings row) beats the built-in
	code, _, _ := h.api(d.sess, "PATCH", "/api/settings", `{"ui.theme":"linen","ui.font_size":22}`)
	require.Equal(t, http.StatusOK, code)
	def, _, merged = get()
	require.Equal(t, "linen", def["ui.theme"])
	require.Equal(t, "linen", merged["ui.theme"])
	require.EqualValues(t, 22, merged["ui.font_size"])
	// 3. the device value beats the server default
	code, out, _ := d.call("PATCH", "/api/device", `{"ui.theme":"graphite","client.layout":"cards","client.text_size":1.25}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "graphite", out["merged"].(map[string]any)["ui.theme"])
	def, prof, merged = get()
	require.Equal(t, "linen", def["ui.theme"], "defaults unchanged")
	require.Equal(t, map[string]any{"ui.theme": "graphite", "client.layout": "cards", "client.text_size": 1.25}, prof)
	require.Equal(t, "cards", merged["client.layout"])
	// 4. null clears the override, back to the server default
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":null}`)
	require.Equal(t, "linen", out["merged"].(map[string]any)["ui.theme"])
	require.NotContains(t, out["profile"], "ui.theme")
	// An old theme id is stored as its alias.
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":"oled"}`)
	require.Equal(t, "midnight", out["profile"].(map[string]any)["ui.theme"])
	// bootstrap carries the merged profile
	_, out, _ = d.call("GET", "/api/bootstrap", "")
	bd := out["device"].(map[string]any)
	require.Equal(t, "midnight", bd["merged"].(map[string]any)["ui.theme"])
	require.Equal(t, "cards", bd["profile"].(map[string]any)["client.layout"])
	// GET /api/settings still reports the account default
	_, out, _ = d.call("GET", "/api/settings", "")
	require.Equal(t, "linen", vals(out)["ui.theme"])
}

func TestPatchDeviceValidation(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	for _, tc := range []struct{ name, body, bad string }{
		{"unknown key", `{"nope":1}`, "nope"},
		{"global setting", `{"retention.default":100}`, "retention.default"},
		{"global null", `{"tz":null}`, "tz"},
		{"sys key", `{"sys.id_high_water":1}`, "sys.id_high_water"},
		{"unknown client key", `{"client.nope":1}`, "client.nope"},
		{"bad theme", `{"ui.theme":"neon"}`, "ui.theme"},
		{"day theme system", `{"ui.theme_day":"system"}`, "ui.theme_day"},
		{"font", `{"ui.font_body":"Comic Sans"}`, "ui.font_body"},
		{"font size", `{"ui.font_size":99}`, "ui.font_size"},
		{"layout", `{"client.layout":"list"}`, "client.layout"},
		{"order type", `{"client.order":1}`, "client.order"},
		{"width low", `{"client.sidebar_width":100}`, "client.sidebar_width"},
		{"width fractional", `{"client.list_width":300.5}`, "client.list_width"},
		{"text size", `{"client.text_size":1.3}`, "client.text_size"},
		{"bool type", `{"client.large_targets":"yes"}`, "client.large_targets"},
		{"highlight type", `{"client.highlight_keywords":"no"}`, "client.highlight_keywords"},
		{"voice newline", `{"client.voice":"a\nb"}`, "client.voice"},
		{"voice long", `{"client.voice":"` + strings.Repeat("v", 201) + `"}`, "client.voice"},
		{"override bad layout", `{"client.layout_overrides":{"feed":{"1":"grid"},"folder":{}}}`, "client.layout_overrides"},
		{"override bad id", `{"client.layout_overrides":{"feed":{"abc":"cards"}}}`, "client.layout_overrides"},
		{"override extra key", `{"client.layout_overrides":{"tag":{"1":"cards"}}}`, "client.layout_overrides"},
		{"collapsed dup", `{"client.collapsed_folders":["1","1"]}`, "client.collapsed_folders"},
		{"collapsed type", `{"client.collapsed_folders":[1]}`, "client.collapsed_folders"},
	} {
		code, out, _ := d.call("PATCH", "/api/device", tc.body)
		require.Equal(t, http.StatusBadRequest, code, tc.name)
		require.Equal(t, "invalid_settings", out["error"], tc.name)
		require.Equal(t, []any{tc.bad}, out["keys"], tc.name)
	}
	// empty and malformed bodies
	code, _, _ := d.call("PATCH", "/api/device", `{}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _, _ = d.call("PATCH", "/api/device", `[1]`)
	require.Equal(t, http.StatusBadRequest, code)
	// all or nothing
	code, _, _ = d.call("PATCH", "/api/device", `{"ui.theme":"paper","client.layout":"grid"}`)
	require.Equal(t, http.StatusBadRequest, code)
	_, out, _ := d.call("GET", "/api/device", "")
	require.Empty(t, out["profile"])
}

func TestPatchDeviceAcceptsEveryClientKey(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	body := `{"ui.theme":"system","ui.theme_day":"linen","ui.theme_night":"carbon","ui.font_body":"Atkinson Hyperlegible Next",
	 "ui.font_ui":"Inter","ui.font_size":19,"ui.reading_density":"airy","ui.list_density":"dense","ui.mark_read_on_scroll":true,
	 "ui.layouts":{"all":"cards"},
	 "client.layout":"headlines","client.layout_overrides":{"feed":{"12":"inbox"},"folder":{"3":"compact"}},
	 "client.order":"oldest","client.search_order":"relevance","client.inbox_thumbs":"off","client.peek_seen":true,"client.article_width":"full",
	 "client.list_width":400,"client.sidebar_width":300,"client.link_target":"same","client.unread_badge":"dot",
	 "client.text_size":0.875,"client.adjust_separately":true,"client.shortcuts":false,"client.spacing":"roomy",
	 "client.motion":"on","client.large_targets":true,"client.highlight_keywords":false,"client.listen":true,"client.voice":"Samantha","client.rate":1.2,
	 "client.collapsed_folders":["4","9"]}`
	code, out, _ := d.call("PATCH", "/api/device", body)
	require.Equal(t, http.StatusOK, code, out)
	m := out["merged"].(map[string]any)
	require.Equal(t, "relevance", m["client.search_order"])
	require.Equal(t, "headlines", m["client.layout"])
	for _, bad := range []string{"\"rank\"", "1"} {
		c2, o2, _ := d.call("PATCH", "/api/device", `{"client.search_order":`+bad+`}`)
		require.NotEqual(t, http.StatusOK, c2, bad)
		_ = o2
	}
	require.Equal(t, map[string]any{"feed": map[string]any{"12": "inbox"}, "folder": map[string]any{"3": "compact"}}, m["client.layout_overrides"])
	require.Equal(t, false, m["client.shortcuts"])
	require.Equal(t, false, m["client.highlight_keywords"])
	_, fresh, _ := h.newDev().call("GET", "/api/device", "")
	require.Equal(t, true, fresh["merged"].(map[string]any)["client.highlight_keywords"], "on unless a device turns it off")
	require.EqualValues(t, 1.2, m["client.rate"])
	require.Equal(t, []any{"4", "9"}, m["client.collapsed_folders"])
	// partial update keeps the rest
	_, out, _ = d.call("PATCH", "/api/device", `{"client.layout":null,"client.shortcuts":null}`)
	prof := out["profile"].(map[string]any)
	require.NotContains(t, prof, "client.layout")
	require.NotContains(t, prof, "client.shortcuts")
	require.Equal(t, "airy", prof["ui.reading_density"])
	require.Equal(t, "magazine", out["merged"].(map[string]any)["client.layout"])
	require.NotContains(t, out["merged"], "client.shortcuts")
}

func TestDeviceProfileSizeLimit413(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	// Two full layout maps (200 entries each, each valid on its own) exceed 8 KB together.
	big := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`"%019d":"headlines"`, i+1)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	body := `{"client.layout_overrides":{"feed":` + big(200) + `,"folder":` + big(200) + `}}`
	code, _, _ := d.call("PATCH", "/api/device", body)
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	_, out, _ := d.call("GET", "/api/device", "")
	require.Empty(t, out["profile"], "nothing was stored")
	// The request body itself is bounded too.
	code, _, _ = d.call("PATCH", "/api/device", `{"client.voice":"`+strings.Repeat("x", 2<<20)+`"}`)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestDeviceName(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	code, out, _ := d.call("PUT", "/api/device/name", `{"name":"  Owners iPhone  "}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Owners iPhone", out["name"])
	code, out, _ = d.call("PUT", "/api/device/name", `{"name":""}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "", out["name"])
	for _, body := range []string{`{}`, `{"name":null}`, `{"name":1}`, `{"name":"` + strings.Repeat("n", 65) + `"}`, `{"name":"a\nb"}`, `nope`} {
		code, _, _ = d.call("PUT", "/api/device/name", body)
		require.Equal(t, http.StatusBadRequest, code, body)
	}
	code, _, _ = d.call("PUT", "/api/device/name", `{"name":"`+strings.Repeat("n", 64)+`"}`)
	require.Equal(t, http.StatusOK, code)
}

func TestListCopyDeleteDevices(t *testing.T) {
	h := newHarness(t)
	phone, laptop := h.newDev(), h.newDev()
	phone.call("PATCH", "/api/device", `{"ui.theme":"fountain","client.layout":"inbox"}`)
	phone.call("PUT", "/api/device/name", `{"name":"Phone"}`)
	h.clk.Advance(time.Hour)
	_, out, _ := laptop.call("GET", "/api/device", "")
	laptopID := out["id"].(string)
	phoneID := phone.dc.Value
	require.NotEqual(t, phoneID, laptopID)

	code, out, _ := laptop.call("GET", "/api/devices", "")
	require.Equal(t, http.StatusOK, code)
	list := out["devices"].([]any)
	require.Len(t, list, 2)
	first := list[0].(map[string]any)
	require.Equal(t, laptopID, first["id"], "most recently seen first")
	require.Equal(t, true, first["current"])
	second := list[1].(map[string]any)
	require.Equal(t, "Phone", second["name"])
	require.Equal(t, false, second["current"])
	require.EqualValues(t, 2, second["overrides"])
	for _, k := range []string{"user_agent", "client", "created_at", "last_seen_at"} {
		require.Contains(t, second, k)
	}

	// copy-from replaces this device's overrides
	laptop.call("PATCH", "/api/device", `{"ui.font_size":24,"client.order":"oldest"}`)
	code, out, _ = laptop.call("POST", "/api/device/copy-from/"+phoneID, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]any{"ui.theme": "fountain", "client.layout": "inbox"}, out["profile"])
	require.Equal(t, "fountain", out["merged"].(map[string]any)["ui.theme"])
	require.EqualValues(t, 18, out["merged"].(map[string]any)["ui.font_size"])
	// the source is untouched
	_, out, _ = phone.call("GET", "/api/device", "")
	require.Equal(t, "Phone", out["name"])
	// "defaults" clears, self and unknown are refused
	code, out, _ = laptop.call("POST", "/api/device/copy-from/defaults", "")
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out["profile"])
	code, _, _ = laptop.call("POST", "/api/device/copy-from/"+laptopID, "")
	require.Equal(t, http.StatusBadRequest, code)
	code, _, _ = laptop.call("POST", "/api/device/copy-from/zzzzzzzzzzzzzzzzzzzzzz", "")
	require.Equal(t, http.StatusNotFound, code)

	// delete: not the current one
	code, out, _ = laptop.call("DELETE", "/api/devices/"+laptopID, "")
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "current_device", out["error"])
	code, _, _ = laptop.call("DELETE", "/api/devices/zzzzzzzzzzzzzzzzzzzzzz", "")
	require.Equal(t, http.StatusNotFound, code)
	code, _, _ = laptop.call("DELETE", "/api/devices/"+phoneID, "")
	require.Equal(t, http.StatusNoContent, code)
	_, out, _ = laptop.call("GET", "/api/devices", "")
	require.Len(t, out["devices"], 1)
	// the deleted device's cookie now reads as unknown: a fresh profile and a new cookie
	_, out, rec := phone.call("GET", "/api/device", "")
	require.NotNil(t, deviceCookieOf(rec))
	require.NotEqual(t, phoneID, out["id"])
	require.Empty(t, out["profile"])
}

func TestMakeDeviceDefault(t *testing.T) {
	h := newHarness(t)
	phone, tablet := h.newDev(), h.newDev()
	phone.call("PATCH", "/api/device", `{"ui.theme":"fountain","ui.font_size":21,"client.layout":"cards","client.sidebar_width":300}`)
	code, out, _ := phone.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "fountain", out["defaults"].(map[string]any)["ui.theme"])
	require.Equal(t, "cards", out["defaults"].(map[string]any)["client.layout"])

	// a device with no overrides starts from those defaults
	_, out, _ = tablet.call("GET", "/api/device", "")
	require.Empty(t, out["profile"])
	m := out["merged"].(map[string]any)
	require.Equal(t, "fountain", m["ui.theme"])
	require.EqualValues(t, 21, m["ui.font_size"])
	require.Equal(t, "cards", m["client.layout"])
	require.EqualValues(t, 300, m["client.sidebar_width"])
	// the server settings API sees the account defaults for the device-scoped ui.* rows
	_, out, _ = tablet.call("GET", "/api/settings", "")
	require.Equal(t, "fountain", vals(out)["ui.theme"])
	require.Equal(t, map[string]any{"client.layout": "cards", "client.sidebar_width": float64(300)}, vals(out)["ui.device_defaults"])
	// a second make-default merges into the stored client defaults
	tablet.call("PATCH", "/api/device", `{"client.order":"oldest"}`)
	tablet.call("POST", "/api/device/make-default", "")
	_, out, _ = phone.call("GET", "/api/settings", "")
	dd := vals(out)["ui.device_defaults"].(map[string]any)
	require.Len(t, dd, 3)
	require.Equal(t, "oldest", dd["client.order"])
	// the making device keeps its own overrides
	_, out, _ = phone.call("GET", "/api/device", "")
	require.Len(t, out["profile"], 4)
}

func TestSettingsMetadataScope(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, out, _ := h.api(c, "GET", "/api/settings", "")
	by := map[string]string{}
	for _, x := range out["settings"].([]any) {
		m := x.(map[string]any)
		by[m["key"].(string)] = m["scope"].(string)
	}
	for _, k := range []string{"ui.theme", "ui.theme_day", "ui.theme_night", "ui.font_body", "ui.font_ui", "ui.font_size",
		"ui.reading_density", "ui.list_density", "ui.mark_read_on_scroll", "ui.layouts"} {
		require.Equal(t, "device", by[k], k)
	}
	for _, k := range []string{"tz", "retention.default", "refresh.interval_minutes", "library.favorites", "ui.device_defaults", "links.strip_tracking"} {
		require.Equal(t, "global", by[k], k)
	}
	for k, sc := range by {
		require.Contains(t, []string{"global", "device", "both"}, sc, k)
	}
	// every device-scoped key is a valid profile key and no global one is
	for _, d := range settingDefs {
		if d.Scope != scopeGlobal {
			require.Empty(t, profileKeyProblem(d.Key), d.Key)
		} else {
			require.NotEmpty(t, profileKeyProblem(d.Key), d.Key)
		}
	}
	// the defaults of every client key validate under their own rule
	for k, cd := range clientDefs {
		if cd.def != nil {
			_, msg := cd.check(jsonRoundTrip(t, cd.def))
			require.Empty(t, msg, k)
		}
	}
	// the account-defaults key rejects unknown and bad entries
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"ui.device_defaults":{"client.layout":"grid"}}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _, _ = h.api(c, "PATCH", "/api/settings", `{"ui.device_defaults":{"nope":1}}`)
	require.Equal(t, http.StatusBadRequest, code)
}

func TestDeviceRoutesAuthAndOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	routes := [][2]string{
		{"GET", "/api/device"}, {"PATCH", "/api/device"}, {"PUT", "/api/device/name"}, {"POST", "/api/device/make-default"},
		{"POST", "/api/device/copy-from/defaults"}, {"GET", "/api/devices"}, {"DELETE", "/api/devices/zzzzzzzzzzzzzzzzzzzzzz"},
	}
	for _, ep := range routes {
		body := `{"name":"x","ui.theme":"paper"}`
		rec := h.do(ep[0], ep[1], body)
		require.Equal(t, http.StatusUnauthorized, rec.Code, ep[0]+" "+ep[1]+" anonymous")
		require.Nil(t, deviceCookieOf(rec), "no device cookie without a session")
		if ep[0] == "GET" {
			continue
		}
		code, _, rec := h.api(c, ep[0], ep[1], body, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		require.Equal(t, http.StatusForbidden, code, ep[0]+" "+ep[1]+" cross-site")
		require.Nil(t, deviceCookieOf(rec))
		code, _, _ = h.api(c, ep[0], ep[1], body, func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
		require.Equal(t, http.StatusForbidden, code, ep[0]+" "+ep[1]+" no client header")
	}
	n, err := h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Empty(t, n, "rejected requests register nothing")
}

// A device cookie is not a credential: without a session the routes stay closed.
func TestDeviceCookieAloneGrantsNothing(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	d.call("GET", "/api/device", "")
	require.NotNil(t, d.dc)
	rec := h.do("GET", "/api/device", "", func(r *http.Request) { r.AddCookie(d.dc) })
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = h.do("GET", "/api/bootstrap", "", func(r *http.Request) { r.AddCookie(d.dc) })
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A session that never sends the device cookie registers at most maxNewDevicesPerSessionDay
// devices a day, then keeps getting its last one: a buggy or cookieless client cannot fill the
// table or evict real profiles.
func TestCookielessSessionRegistrationIsCapped(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	var ids []string
	for i := 0; i < 20; i++ {
		code, out, _ := h.api(sess, "GET", "/api/device", "")
		require.Equal(t, 200, code)
		ids = append(ids, out["id"].(string))
		h.clk.Advance(time.Minute)
	}
	distinct := map[string]bool{}
	for _, id := range ids {
		distinct[id] = true
	}
	require.Len(t, distinct, maxNewDevicesPerSessionDay)
	require.Equal(t, ids[maxNewDevicesPerSessionDay-1], ids[19], "beyond the cap the session keeps its last device")
	list, err := h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, maxNewDevicesPerSessionDay)
	// A new day starts a new allowance.
	h.clk.Advance(25 * time.Hour)
	_, out, _ := h.api(sess, "GET", "/api/device", "")
	require.False(t, distinct[out["id"].(string)])
}

// Concurrent first loads (two tabs, bootstrap plus device) share one device.
func TestConcurrentFirstLoadsRegisterOneDevice(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out, _ := h.api(sess, "GET", "/api/device", "")
			ids[i], _ = out["id"].(string)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		require.Equal(t, ids[0], id)
		require.NotEmpty(t, id)
	}
	list, err := h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1)
}

// At the cap with every device recently seen, a new client is served defaults, nothing is
// evicted, and writes to the unsaved device are refused.
func TestDeviceCapNeverEvictsRecentDevices(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	now := h.clk.Now().Unix()
	for i := 0; i < store.MaxDevices; i++ {
		_, err := h.db.RegisterDevice(t.Context(), fmt.Sprintf("seed-device-%016d", i), "ua", "web", now-int64(i)-60)
		require.NoError(t, err)
	}
	code, out, _ := h.api(sess, "GET", "/api/device", "")
	require.Equal(t, 200, code)
	require.Equal(t, "", out["id"], "an unsaved default device")
	code, _, _ = h.api(sess, "PATCH", "/api/device", `{"ui.theme":"dark"}`)
	require.Equal(t, 404, code)
	list, err := h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, store.MaxDevices)
	// Once devices age past 30 days the cap makes room again.
	h.clk.Advance(31 * 24 * time.Hour)
	_, out, _ = h.api(sess, "GET", "/api/device", "")
	require.NotEmpty(t, out["id"])
	list, err = h.db.ListDevices(t.Context())
	require.NoError(t, err)
	require.Len(t, list, store.MaxDevices)
}
