package api

import (
	"context"
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
	code, _, _ := h.api(d.sess, "PATCH", "/api/settings", `{"ui.theme":"linen","ui.reading_density":"airy"}`)
	require.Equal(t, http.StatusOK, code)
	def, _, merged = get()
	require.Equal(t, "linen", def["ui.theme"])
	require.Equal(t, "linen", merged["ui.theme"])
	require.Equal(t, "airy", merged["ui.reading_density"])
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
		{"theme schedule is a flag", `{"ui.theme":"schedule"}`, "ui.theme"},
		{"schedule flag type", `{"ui.theme_schedule":"yes"}`, "ui.theme_schedule"},
		{"night start 24h", `{"ui.theme_night_start":"24:00"}`, "ui.theme_night_start"},
		{"day start no pad", `{"ui.theme_day_start":"7:00"}`, "ui.theme_day_start"},
		{"day start type", `{"ui.theme_day_start":700}`, "ui.theme_day_start"},
		{"font", `{"ui.font_body":"Comic Sans"}`, "ui.font_body"},
		{"reading density", `{"ui.reading_density":"huge"}`, "ui.reading_density"},
		{"layout", `{"client.layout":"list"}`, "client.layout"},
		{"order type", `{"client.order":1}`, "client.order"},
		{"width low", `{"client.sidebar_width":100}`, "client.sidebar_width"},
		{"width fractional", `{"client.list_width":300.5}`, "client.list_width"},
		{"text size", `{"client.text_size":1.3}`, "client.text_size"},
		{"bool type", `{"client.large_targets":"yes"}`, "client.large_targets"},
		{"font_everywhere type", `{"client.font_everywhere":"yes"}`, "client.font_everywhere"},
		{"highlight type", `{"client.highlight_keywords":"no"}`, "client.highlight_keywords"},
		{"voice newline", `{"client.voice":"a\nb"}`, "client.voice"},
		{"voice long", `{"client.voice":"` + strings.Repeat("v", 201) + `"}`, "client.voice"},
		{"paper name newline", `{"client.paper_name":"The\nDaily"}`, "client.paper_name"},
		{"paper name tab", `{"client.paper_name":"The\tDaily"}`, "client.paper_name"},
		{"paper name DEL", `{"client.paper_name":"The\u007fDaily"}`, "client.paper_name"},
		{"paper name NEL", `{"client.paper_name":"The\u0085Daily"}`, "client.paper_name"},
		{"paper name C1", `{"client.paper_name":"The\u009bDaily"}`, "client.paper_name"},
		{"paper name line separator", `{"client.paper_name":"The\u2028Daily"}`, "client.paper_name"},
		{"paper name paragraph separator", `{"client.paper_name":"The\u2029Daily"}`, "client.paper_name"},
		{"paper name long", `{"client.paper_name":"` + strings.Repeat("é", 61) + `"}`, "client.paper_name"},
		{"paper name type", `{"client.paper_name":1}`, "client.paper_name"},
		{"override bad layout", `{"client.list_overrides":{"feed":{"1":{"layout":"grid"}},"folder":{}}}`, "client.list_overrides"},
		{"override bad order", `{"client.list_overrides":{"feed":{"1":{"order":"rank"}}}}`, "client.list_overrides"},
		{"override bad view", `{"client.list_overrides":{"folder":{"1":{"view":"starred"}}}}`, "client.list_overrides"},
		{"override unknown field", `{"client.list_overrides":{"feed":{"1":{"layout":"cards","density":"dense"}}}}`, "client.list_overrides"},
		{"override empty entry", `{"client.list_overrides":{"feed":{"1":{}}}}`, "client.list_overrides"},
		{"override bare layout", `{"client.list_overrides":{"feed":{"1":"cards"}}}`, "client.list_overrides"},
		{"override bad id", `{"client.list_overrides":{"feed":{"abc":{"layout":"cards"}}}}`, "client.list_overrides"},
		{"override extra key", `{"client.list_overrides":{"tag":{"1":{"layout":"cards"}}}}`, "client.list_overrides"},
		{"old override key", `{"client.layout_overrides":{"feed":{"1":"cards"}}}`, "client.layout_overrides"},
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
	body := `{"ui.theme":"system","ui.theme_schedule":true,"ui.theme_day":"linen","ui.theme_night":"carbon","ui.theme_night_start":"22:30","ui.theme_day_start":"06:15","ui.font_body":"Atkinson Hyperlegible Next",
	 "ui.reading_density":"airy","ui.list_density":"dense","ui.mark_read_on_scroll":true,
	 "client.layout":"headlines","client.list_overrides":{"feed":{"12":{"layout":"inbox","order":"oldest","view":"all"}},"folder":{"3":{"layout":"compact"},"8":{"view":"unread"},"5":{"layout":"gazette"}}},
	 "client.paper_name":"` + strings.Repeat("é", 60) + `",
	 "client.order":"oldest","client.search_order":"relevance","client.inbox_thumbs":"off","client.peek_seen":true,"client.article_width":"full",
	 "client.list_width":400,"client.sidebar_width":300,"client.link_target":"same","client.unread_badge":"dot",
	 "client.text_size":0.875,"client.adjust_separately":true,"client.shortcuts":false,"client.spacing":"roomy",
	 "client.motion":"on","client.font_everywhere":true,"client.large_targets":true,"client.highlight_keywords":false,"client.listen":true,"client.voice":"Samantha","client.rate":1.2,
	 "client.collapsed_folders":["4","9"]}`
	code, out, _ := d.call("PATCH", "/api/device", body)
	require.Equal(t, http.StatusOK, code, out)
	m := out["merged"].(map[string]any)
	require.Equal(t, "relevance", m["client.search_order"])
	require.Equal(t, "system", m["ui.theme"])
	require.Equal(t, true, m["ui.theme_schedule"])
	require.Equal(t, "22:30", m["ui.theme_night_start"])
	require.Equal(t, "06:15", m["ui.theme_day_start"])
	require.Equal(t, "headlines", m["client.layout"])
	require.Equal(t, "easy", m["ui.font_body"], "a display name is stored as the font id")
	for _, bad := range []string{"\"rank\"", "1"} {
		c2, o2, _ := d.call("PATCH", "/api/device", `{"client.search_order":`+bad+`}`)
		require.NotEqual(t, http.StatusOK, c2, bad)
		_ = o2
	}
	require.Equal(t, map[string]any{
		"feed":   map[string]any{"12": map[string]any{"layout": "inbox", "order": "oldest", "view": "all"}},
		"folder": map[string]any{"3": map[string]any{"layout": "compact"}, "8": map[string]any{"view": "unread"}, "5": map[string]any{"layout": "gazette"}},
	}, m["client.list_overrides"])
	require.Equal(t, strings.Repeat("é", 60), m["client.paper_name"], "60 characters, not 60 bytes")
	_, other, _ := h.newDev().call("GET", "/api/device", "")
	require.Equal(t, "", other["merged"].(map[string]any)["client.paper_name"], "blank (the web app's default name) unless a device names it")
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

// listOverridesOf is a valid client.list_overrides value whose compact JSON is as long as it can be
// without passing n bytes (feed entries of {"layout":"headlines"}).
func listOverridesOf(n int) string {
	var parts []string
	for i := 1; ; i++ {
		next := append(parts, fmt.Sprintf(`"%d":{"layout":"headlines"}`, 1000000+i))
		if len(`{"feed":{`+strings.Join(next, ",")+`},"folder":{}}`) > n {
			break
		}
		parts = next
	}
	return `{"feed":{` + strings.Join(parts, ",") + `},"folder":{}}`
}

// client.list_overrides has its own byte budget: over it, the key alone is refused (400 naming it,
// so a client puts aside only that key), and the rest of the patch is not applied either (all or nothing).
func TestListOverridesBudget(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	fits := listOverridesOf(store.MaxListOverridesBytes)
	code, out, _ := d.call("PATCH", "/api/device", `{"client.list_overrides":`+fits+`}`)
	require.Equal(t, http.StatusOK, code, out)

	over := listOverridesOf(store.MaxListOverridesBytes + 40)
	require.Greater(t, len(over), store.MaxListOverridesBytes)
	code, out, _ = d.call("PATCH", "/api/device", `{"client.layout":"cards","client.list_overrides":`+over+`}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "invalid_settings", out["error"])
	require.Equal(t, []any{"client.list_overrides"}, out["keys"])
	_, out, _ = d.call("GET", "/api/device", "")
	require.NotContains(t, out["profile"], "client.layout")

	// The defaults for new devices are held to the same budget.
	c := h.login()
	code, out, _ = h.api(c, "PATCH", "/api/settings", `{"ui.device_defaults":{"client.list_overrides":`+over+`}}`)
	require.Equal(t, http.StatusBadRequest, code, out)
}

func TestDeviceProfileSizeLimit413(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	// The list overrides fit their own budget (just under 4 KB) and the collapsed folders fit too,
	// but together they pass the 8 KB the stored profile is held to.
	overrides := listOverridesOf(store.MaxListOverridesBytes - 100)
	folders := make([]string, 200)
	for i := range folders {
		folders[i] = fmt.Sprintf(`"%019d"`, i+1)
	}
	body := `{"client.list_overrides":` + overrides + `,"client.collapsed_folders":[` + strings.Join(folders, ",") + `]}`
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
	require.NoError(t, h.db.ReplaceDeviceProfile(context.Background(), phoneID, map[string]any{"ui.theme": "fountain", "client.layout": "inbox"}))
	for _, k := range []string{"user_agent", "client", "created_at", "last_seen_at"} {
		require.Contains(t, second, k)
	}

	// copy-from replaces this device's overrides
	laptop.call("PATCH", "/api/device", `{"ui.reading_density":"dense","client.order":"oldest"}`)
	code, out, _ = laptop.call("POST", "/api/device/copy-from/"+phoneID, "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]any{"ui.theme": "fountain", "client.layout": "inbox"}, out["profile"])
	require.Equal(t, "fountain", out["merged"].(map[string]any)["ui.theme"])
	require.Equal(t, "standard", out["merged"].(map[string]any)["ui.reading_density"])
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
	phone.call("PATCH", "/api/device", `{"ui.theme":"fountain","ui.reading_density":"airy","client.layout":"cards","client.sidebar_width":300}`)
	code, out, _ := phone.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "fountain", out["defaults"].(map[string]any)["ui.theme"])
	require.Equal(t, "cards", out["defaults"].(map[string]any)["client.layout"])

	// a device with no overrides starts from those defaults
	_, out, _ = tablet.call("GET", "/api/device", "")
	require.Empty(t, out["profile"])
	m := out["merged"].(map[string]any)
	require.Equal(t, "fountain", m["ui.theme"])
	require.Equal(t, "airy", m["ui.reading_density"])
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

// Make default merges this device's client.* keys into the stored defaults; the
// merge is held to the same 8 KB cap as a direct write of ui.device_defaults.
func TestMakeDeviceDefaultSizeCap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	stored := `{"ui.device_defaults":{"client.list_overrides":` + listOverridesOf(store.MaxListOverridesBytes) + `}}`
	code, _, _ := h.api(c, "PATCH", "/api/settings", stored)
	require.Equal(t, http.StatusOK, code, "under the cap on its own")

	folders := make([]string, 200)
	for i := range folders {
		folders[i] = fmt.Sprintf(`"%019d"`, i)
	}
	phone := h.newDev()
	code, _, _ = phone.call("PATCH", "/api/device", `{"client.collapsed_folders":[`+strings.Join(folders, ",")+`]}`)
	require.Equal(t, http.StatusOK, code, "under the cap on its own")

	code, out, _ := phone.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusRequestEntityTooLarge, code)
	require.Equal(t, "too_large", out["error"])
	_, out, _ = phone.call("GET", "/api/settings", "")
	dd := vals(out)["ui.device_defaults"].(map[string]any)
	require.NotContains(t, dd, "client.collapsed_folders", "nothing was written")

	// A merge that fits still goes through.
	code, _, _ = phone.call("PATCH", "/api/device", `{"client.collapsed_folders":["1"]}`)
	require.Equal(t, http.StatusOK, code)
	code, _, _ = phone.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
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
	for _, k := range []string{"ui.theme", "ui.theme_day", "ui.theme_night", "ui.theme_schedule", "ui.theme_night_start", "ui.theme_day_start", "ui.font_body",
		"ui.reading_density", "ui.list_density", "ui.mark_read_on_scroll"} {
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

// A fixed theme ends the schedule when the patch does not name it (a client that predates the schedule sends
// only ui.theme), so a later "system" from that client is plain follow-system again.
func TestFixedThemeEndsSchedule(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	code, out, _ := d.call("PATCH", "/api/device", `{"ui.theme":"graphite"}`)
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, out["profile"], "ui.theme_schedule", "nothing to end: no key added")

	d.call("PATCH", "/api/device", `{"ui.theme":"system","ui.theme_schedule":true}`)
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":"graphite"}`)
	require.Equal(t, false, out["merged"].(map[string]any)["ui.theme_schedule"])
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":"system"}`)
	m := out["merged"].(map[string]any)
	require.Equal(t, "system", m["ui.theme"])
	require.Equal(t, false, m["ui.theme_schedule"])

	// A patch that names the flag keeps what it says.
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":"linen","ui.theme_schedule":true}`)
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"])
	// Other keys leave it alone.
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme_day":"airmail"}`)
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"])

	// Clearing the device's theme leaves the account default in force: a fixed one ends the schedule too.
	code, _, _ = h.api(d.sess, "PATCH", "/api/settings", `{"ui.theme":"paper"}`)
	require.Equal(t, http.StatusOK, code)
	d.call("PATCH", "/api/device", `{"ui.theme":"system","ui.theme_schedule":true}`)
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":null}`)
	m = out["merged"].(map[string]any)
	require.Equal(t, "paper", m["ui.theme"])
	require.Equal(t, false, m["ui.theme_schedule"])
}

// The account defaults follow the same rule: PATCH /api/settings with a fixed theme and no
// ui.theme_schedule turns the account's schedule off, so devices without their own override never
// inherit a fixed theme with the flag stuck on.
func TestAccountFixedThemeEndsSchedule(t *testing.T) {
	h := newHarness(t)
	sess := h.login()
	values := func(out map[string]any) map[string]any { return out["values"].(map[string]any) }

	code, out, _ := h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"system","ui.theme_schedule":true}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, values(out)["ui.theme_schedule"])
	// A device with its own "system" theme that inherits the account's schedule flag.
	inherits := h.newDev()
	_, out, _ = inherits.call("PATCH", "/api/device", `{"ui.theme":"system"}`)
	require.NotContains(t, out["profile"], "ui.theme_schedule")
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"])

	// Another key leaves it alone.
	_, out, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme_day":"airmail"}`)
	require.Equal(t, true, values(out)["ui.theme_schedule"])
	// Resetting the theme to its default ("system") keeps the schedule.
	_, out, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":null}`)
	require.Equal(t, true, values(out)["ui.theme_schedule"])

	code, out, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"graphite"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "graphite", values(out)["ui.theme"])
	require.Equal(t, false, values(out)["ui.theme_schedule"])
	// That device keeps showing its schedule: the flag it inherited was pinned on its own profile.
	_, out, _ = inherits.call("GET", "/api/device", "")
	require.Equal(t, true, out["profile"].(map[string]any)["ui.theme_schedule"])
	require.Equal(t, "system", out["merged"].(map[string]any)["ui.theme"])
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"])

	// A patch that names the flag keeps what it says.
	_, out, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"linen","ui.theme_schedule":true}`)
	require.Equal(t, true, values(out)["ui.theme_schedule"])

	// A fixed theme sent with the flag reset (null, default off) keeps an inheriting device's schedule too.
	nullDev := h.newDev()
	nullDev.call("PATCH", "/api/device", `{"ui.theme":"system"}`)
	_, out, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"paper","ui.theme_schedule":null}`)
	require.Equal(t, false, values(out)["ui.theme_schedule"])
	_, out, _ = nullDev.call("GET", "/api/device", "")
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"])
	_, _, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"linen","ui.theme_schedule":true}`)

	// A refused write pins nothing: the pin and the account write are one transaction.
	other := h.newDev()
	other.call("PATCH", "/api/device", `{"ui.theme":"system"}`)
	code, _, _ = h.api(sess, "PATCH", "/api/settings", `{"ui.theme":"paper","library.saved_searches":[{"id":"a","name":"n","q":"x","scope":{"feed_id":"999999"}}]}`)
	require.Equal(t, http.StatusBadRequest, code)
	_, out, _ = other.call("GET", "/api/device", "")
	require.NotContains(t, out["profile"], "ui.theme_schedule")
	merged, err := h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "linen", merged["ui.theme"])
	require.Equal(t, true, merged["ui.theme_schedule"])

	// A device without overrides sees the account's state.
	d := h.newDev()
	_, out, _ = d.call("PATCH", "/api/settings", `{"ui.theme":"paper"}`)
	require.Equal(t, false, values(out)["ui.theme_schedule"])
	_, out, _ = d.call("GET", "/api/device", "")
	require.Equal(t, false, out["merged"].(map[string]any)["ui.theme_schedule"])
}

// Make-default copies the device's theme into the account defaults under the same rule: a fixed theme
// ends the account's schedule unless the device carries its own schedule flag.
func TestMakeDefaultFixedThemeEndsSchedule(t *testing.T) {
	h := newHarness(t)
	d := h.newDev()
	// The device picks a fixed theme while the account's schedule is off: no flag of its own.
	_, out, _ := d.call("PATCH", "/api/device", `{"ui.theme":"graphite"}`)
	require.NotContains(t, out["profile"], "ui.theme_schedule")
	code, _, _ := d.call("PATCH", "/api/settings", `{"ui.theme":"system","ui.theme_schedule":true}`)
	require.Equal(t, http.StatusOK, code)

	code, _, _ = d.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	merged, err := h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "graphite", merged["ui.theme"])
	require.Equal(t, false, merged["ui.theme_schedule"], "the account no longer has a fixed theme with the schedule on")

	// A device that names the flag itself passes it on as it is.
	_, _, _ = d.call("PATCH", "/api/device", `{"ui.theme":"system","ui.theme_schedule":true}`)
	code, _, _ = d.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	merged, err = h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "system", merged["ui.theme"])
	require.Equal(t, true, merged["ui.theme_schedule"])

	// A device holding a fixed theme with its own flag still on never passes that pair to the account.
	_, out, _ = d.call("PATCH", "/api/device", `{"ui.theme":"paper","ui.theme_schedule":true}`)
	require.Equal(t, true, out["profile"].(map[string]any)["ui.theme_schedule"])
	code, _, _ = d.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	merged, err = h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "paper", merged["ui.theme"])
	require.Equal(t, false, merged["ui.theme_schedule"])

	// The web app's own profile names the flag off with a fixed theme; Make default from it still keeps the
	// schedule a device with its own "system" theme inherits from the account.
	_, _, _ = d.call("PATCH", "/api/settings", `{"ui.theme":"system","ui.theme_schedule":true}`)
	inherits := h.newDev()
	_, _, _ = inherits.call("PATCH", "/api/device", `{"ui.theme":"system"}`)
	_, _, _ = d.call("PATCH", "/api/device", `{"ui.theme":"graphite","ui.theme_schedule":false}`)
	code, _, _ = d.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	merged, err = h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "graphite", merged["ui.theme"])
	require.Equal(t, false, merged["ui.theme_schedule"])
	_, out, _ = inherits.call("GET", "/api/device", "")
	require.Equal(t, true, out["merged"].(map[string]any)["ui.theme_schedule"], "pinned on the inheriting device")
	_, _, _ = d.call("PATCH", "/api/device", `{"ui.theme":"paper","ui.theme_schedule":null}`)
	_, _, _ = d.call("PATCH", "/api/settings", `{"ui.theme":"paper","ui.theme_schedule":false}`)

	// Nor does a device whose own flag is on under the account's fixed theme it inherits.
	_, _, _ = d.call("PATCH", "/api/device", `{"ui.theme":null,"ui.theme_schedule":true}`)
	code, _, _ = d.call("POST", "/api/device/make-default", "")
	require.Equal(t, http.StatusOK, code)
	merged, err = h.db.MergedSettings(t.Context())
	require.NoError(t, err)
	require.Equal(t, "paper", merged["ui.theme"])
	require.Equal(t, false, merged["ui.theme_schedule"])
}
