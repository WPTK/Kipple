package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/greader"
)

// realVerifier accepts testPass against the seeded "web-hash" and checks any
// other stored hash with the real argon2id code (after a password change).
func realVerifier(o *Options) {
	o.Verifier = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Check: func(pw, phc string) bool {
		if phc == "web-hash" {
			return pw == testPass
		}
		return auth.CheckPassword(pw, phc)
	}})
}

func TestSettingsAuthAndOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, ep := range [][2]string{
		{"GET", "/api/settings"}, {"PATCH", "/api/settings"}, {"POST", "/api/retention/apply"},
		{"POST", "/api/account/password"}, {"POST", "/api/account/api-password"},
	} {
		require.Equal(t, http.StatusUnauthorized, h.do(ep[0], ep[1], `{"tz":"UTC"}`).Code, ep[1]+" anonymous")
		if ep[0] == "GET" {
			continue
		}
		code, _, _ := h.api(c, ep[0], ep[1], `{"tz":"UTC"}`, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		require.Equal(t, http.StatusForbidden, code, ep[1]+" cross-site")
	}
	require.Empty(t, h.sched.retentionAll)
}

func TestGetSettingsDefaults(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.api(h.login(), "GET", "/api/settings", "")
	require.Equal(t, http.StatusOK, code)
	out = vals(out)
	require.EqualValues(t, 30, out["refresh.interval_minutes"])
	require.EqualValues(t, 250, out["retention.default"])
	require.EqualValues(t, 90, out["retention.restore_days"])
	require.Equal(t, "America/New_York", out["tz"])
	require.Equal(t, "all", out["imgproxy.mode"])
	require.Equal(t, "system", out["ui.theme"])
	require.NotContains(t, out, "sys.id_high_water")
}

// vals unwraps the flat key->value map of a GET/PATCH /api/settings response.
func vals(out map[string]any) map[string]any { return out["values"].(map[string]any) }

func TestPatchSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, badKey string
	}{
		{"unknown key", `{"nope":1}`, "nope"},
		{"read-only sys key", `{"sys.id_high_water":5}`, "sys.id_high_water"},
		{"sys snapshot key", `{"sys.last_snapshot_at":1}`, "sys.last_snapshot_at"},
		{"restore_days too high", `{"retention.restore_days":181}`, "retention.restore_days"},
		{"restore_days negative", `{"retention.restore_days":-1}`, "retention.restore_days"},
		{"restore_days fractional", `{"retention.restore_days":1.5}`, "retention.restore_days"},
		{"restore_days string", `{"retention.restore_days":"90"}`, "retention.restore_days"},
		{"retention not in set", `{"retention.default":75}`, "retention.default"},
		{"retention negative", `{"retention.default":-1}`, "retention.default"},
		{"interval too low", `{"refresh.interval_minutes":4}`, "refresh.interval_minutes"},
		{"interval too high", `{"refresh.interval_minutes":1441}`, "refresh.interval_minutes"},
		{"imgproxy enum", `{"imgproxy.mode":"https_only"}`, "imgproxy.mode"},
		{"bool type", `{"stats.api_single_read_is_open":"yes"}`, "stats.api_single_read_is_open"},
		{"tz unknown", `{"tz":"Mars/Base"}`, "tz"},
		{"tz Local", `{"tz":"Local"}`, "tz"},
		{"tz number", `{"tz":5}`, "tz"},
		{"user agent newline", `{"fetch.user_agent":"a\nb"}`, "fetch.user_agent"},
		{"user agent control char", `{"fetch.user_agent":"a\u0001b"}`, "fetch.user_agent"},
		{"user agent DEL", `{"fetch.user_agent":"a\u007fb"}`, "fetch.user_agent"},
		{"theme enum", `{"ui.theme":"neon"}`, "ui.theme"},
		{"font enum", `{"ui.font_body":"Comic Sans"}`, "ui.font_body"},
		{"font size range", `{"ui.font_size":40}`, "ui.font_size"},
		{"removed line height", `{"ui.line_height":1.6}`, "ui.line_height"},
		{"removed content width", `{"ui.content_width":680}`, "ui.content_width"},
		{"density enum", `{"ui.reading_density":"huge"}`, "ui.reading_density"},
		{"ua mode enum", `{"fetch.user_agent_mode":"sometimes"}`, "fetch.user_agent_mode"},
		{"layouts not object", `{"ui.layouts":[1]}`, "ui.layouts"},
		{"layouts non-string value", `{"ui.layouts":{"all":5}}`, "ui.layouts"},
		{"fulltext_all string", `{"fetch.fulltext_all":"yes"}`, "fetch.fulltext_all"},
		{"favorites not a list", `{"library.favorites":{"t":"feed","id":"1"}}`, "library.favorites"},
		{"favorites bad kind", `{"library.favorites":[{"t":"tag","id":"1"}]}`, "library.favorites"},
		{"favorites numeric id", `{"library.favorites":[{"t":"feed","id":1}]}`, "library.favorites"},
		{"favorites non-digit id", `{"library.favorites":[{"t":"feed","id":"1a"}]}`, "library.favorites"},
		{"favorites empty id", `{"library.favorites":[{"t":"feed","id":""}]}`, "library.favorites"},
		{"favorites extra key", `{"library.favorites":[{"t":"feed","id":"1","x":1}]}`, "library.favorites"},
		{"favorites missing key", `{"library.favorites":[{"t":"feed"}]}`, "library.favorites"},
		{"favorites duplicate", `{"library.favorites":[{"t":"feed","id":"1"},{"t":"feed","id":"1"}]}`, "library.favorites"},
		{"favorites not objects", `{"library.favorites":["feed:1"]}`, "library.favorites"},
		{"favorites zero id", `{"library.favorites":[{"t":"feed","id":"0"}]}`, "library.favorites"},
		{"favorites id above int64", `{"library.favorites":[{"t":"feed","id":"9999999999999999999"}]}`, "library.favorites"},
		{"favorites leading-zero duplicate", `{"library.favorites":[{"t":"feed","id":"7"},{"t":"feed","id":"007"}]}`, "library.favorites"},
		{"valid plus invalid is all-or-nothing", `{"ui.theme":"dark","retention.restore_days":999}`, "retention.restore_days"},
		{"empty object", `{}`, ""},
		{"not an object", `[1]`, ""},
		{"not json", `{`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			c := h.login()
			code, out, _ := h.api(c, "PATCH", "/api/settings", tc.body)
			require.Equal(t, http.StatusBadRequest, code)
			if tc.badKey != "" {
				require.Equal(t, "invalid_settings", out["error"])
				require.Equal(t, []any{tc.badKey}, out["keys"])
				require.Contains(t, out["message"], tc.badKey)
			}
			// nothing was written, no side effects
			_, got, _ := h.api(c, "GET", "/api/settings", "")
			got = vals(got)
			require.Equal(t, "system", got["ui.theme"])
			require.EqualValues(t, 90, got["retention.restore_days"])
			require.Empty(t, h.sched.retentionAll)
		})
	}
}

func TestPatchSettingsListsEveryBadKey(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.api(h.login(), "PATCH", "/api/settings", `{"zzz":1,"retention.restore_days":500,"ui.theme":"dark"}`)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, []any{"retention.restore_days", "zzz"}, out["keys"])
}

func TestPatchSettingsAccepted(t *testing.T) {
	for _, tc := range []struct {
		key  string
		body string
		want any
	}{
		{"retention.restore_days", `0`, 0},
		{"retention.restore_days", `180`, 180},
		{"refresh.interval_minutes", `60`, 60},
		{"retention.default", `0`, 0},
		{"retention.default", `1000`, 1000},
		{"fetch.user_agent", `"  Kipple/1  "`, "Kipple/1"},
		{"fetch.honor_publisher_ttl", `false`, false},
		{"greader.icon_urls", `true`, true},
		{"greader.ot_includes_user_changes", `true`, true},
		{"greader.subscribe_fetch_now", `true`, true},
		{"stats.api_single_read_is_open", `true`, true},
		{"imgproxy.mode", `"all"`, "all"},
		{"tz", `"UTC"`, "UTC"},
		{"ui.theme", `"soft-green"`, "directory"}, // an old id is stored as its current alias
		{"ui.font_body", `"Literata"`, "Literata"},
		{"ui.font_ui", `""`, ""},
		{"ui.font_size", `20`, float64(20)},
		{"ui.reading_density", `"compact"`, "compact"},
		{"ui.reading_density", `"relaxed"`, "relaxed"},
		{"ui.theme", `"oled"`, "midnight"},
		{"ui.theme", `"fountain"`, "fountain"},
		{"ui.theme_night", `"carbon"`, "carbon"},
		{"ui.list_density", `"airy"`, "airy"},
		{"ui.reading_density", `"dense"`, "dense"},
		{"fetch.user_agent_mode", `"default"`, "default"},
		{"fetch.user_agent_mode", `"browser_always"`, "browser_always"},
		{"ui.mark_read_on_scroll", `true`, true},
		{"ui.layouts", `{"all":"cards","feed:3":"list"}`, map[string]any{"all": "cards", "feed:3": "list"}},
		{"fetch.fulltext_all", `true`, true},
		{"greader.icon_urls", `false`, false},
		{"library.favorites", `[]`, []any{}},
		{"library.favorites", `[{"t":"folder","id":"2"},{"t":"feed","id":"2"},{"t":"feed","id":"17"}]`, []any{map[string]any{"t": "folder", "id": "2"}, map[string]any{"t": "feed", "id": "2"}, map[string]any{"t": "feed", "id": "17"}}},
	} {
		t.Run(tc.key+"="+tc.body, func(t *testing.T) {
			h := newHarness(t)
			c := h.login()
			code, out, _ := h.api(c, "PATCH", "/api/settings", `{"`+tc.key+`":`+tc.body+`}`)
			require.Equal(t, http.StatusOK, code)
			require.EqualValues(t, tc.want, vals(out)[tc.key])
			_, got, _ := h.api(c, "GET", "/api/settings", "")
			require.EqualValues(t, tc.want, vals(got)[tc.key])
		})
	}
}

func TestPatchSettingsNullResetsToDefault(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":"http_only"}`)
	code, out, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":null}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "all", vals(out)["imgproxy.mode"])
	var n int
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM settings WHERE key = 'imgproxy.mode'").Scan(&n)
	}))
	require.Zero(t, n, "reset removes the override row")
}

func TestPatchRetentionDefaultStartsRun(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	h.api(c, "PATCH", "/api/settings", `{"ui.theme":"dark"}`)
	require.Empty(t, h.sched.retentionAll, "unrelated key: no run")
	h.api(c, "PATCH", "/api/settings", `{"retention.default":250}`)
	require.Empty(t, h.sched.retentionAll, "same value as the default: no run")
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"retention.default":100}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, []bool{false}, h.sched.retentionAll, "inheriting feeds only")
	h.api(c, "PATCH", "/api/settings", `{"retention.default":100}`)
	require.Len(t, h.sched.retentionAll, 1, "unchanged: no second run")
	h.api(c, "PATCH", "/api/settings", `{"retention.default":null}`)
	require.Len(t, h.sched.retentionAll, 2, "reset back to 250 is a change")
}

func TestPatchIntervalTakesEffectWithoutRestart(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	now := h.clk.Now().Unix()
	inherit := h.addFeed("Inherit", 0)
	override := h.addFeed("Override", 0)
	failing := h.addFeed("Failing", 0)
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET last_fetch_at = ?, next_fetch_at = ? + 1800`, now, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET interval_minutes = 120 WHERE id = ?", override); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE feeds SET consecutive_failures = 3 WHERE id = ?", failing)
		return err
	}))
	next := func(id int64) (n int64) {
		require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT next_fetch_at FROM feeds WHERE id = ?", id).Scan(&n)
		}))
		return n
	}
	// settings the scheduler reads change at once
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"refresh.interval_minutes":10}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, 10, h.db.FetchSettings(context.Background()).IntervalMinutes)
	require.Equal(t, now+600, next(inherit), "inheriting feed pulled in to last fetch + 10 min")
	require.Equal(t, now+1800, next(override), "per-feed override untouched")
	require.Equal(t, now+1800, next(failing), "backing-off feed untouched")
	require.Equal(t, 1, h.sched.wakes)
	// raising never postpones
	h.api(c, "PATCH", "/api/settings", `{"refresh.interval_minutes":600}`)
	require.Equal(t, now+600, next(inherit))
	require.Equal(t, 2, h.sched.wakes)
	// an unrelated patch does not wake
	h.api(c, "PATCH", "/api/settings", `{"ui.theme":"dark"}`)
	require.Equal(t, 2, h.sched.wakes)
}

func TestRetentionApply(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.api(h.login(), "POST", "/api/retention/apply", "")
	require.Equal(t, http.StatusAccepted, code)
	require.Equal(t, "44", out["run_id"])
	require.EqualValues(t, 7, out["total"])
	require.Equal(t, []bool{true}, h.sched.retentionAll, "every feed")
}

func TestAccountPassword(t *testing.T) {
	newPass := "a-brand-new-passphrase"
	t.Run("changes password, keeps this session, drops others", func(t *testing.T) {
		h := newHarness(t, realVerifier)
		me, other := h.login(), h.login()
		code, _, _ := h.api(me, "POST", "/api/account/password", jsonStr(map[string]string{"current": testPass, "new": newPass}))
		require.Equal(t, http.StatusNoContent, code)
		code, _, _ = h.api(me, "GET", "/api/status", "")
		require.Equal(t, http.StatusOK, code, "this session kept")
		code, _, _ = h.api(other, "GET", "/api/status", "")
		require.Equal(t, http.StatusUnauthorized, code, "other session revoked")
		// old password no longer logs in; the new one does
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
		require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(newPass)).Code)
	})
	for _, tc := range []struct {
		name, body string
		want       int
		kind       string
	}{
		{"wrong current", `{"current":"nope-nope-nope","new":"a-brand-new-passphrase"}`, http.StatusForbidden, "bad_password"},
		{"empty current", `{"current":"","new":"a-brand-new-passphrase"}`, http.StatusForbidden, "bad_password"},
		{"new too short", `{"current":"correct-horse","new":"four"}`, http.StatusBadRequest, "bad_new_password"},
		{"new missing", `{"current":"correct-horse"}`, http.StatusBadRequest, "bad_new_password"},
		{"new too long", `{"current":"correct-horse","new":"` + string(repeatByte('x', 257)) + `"}`, http.StatusBadRequest, "bad_new_password"},
		{"bad json", `{`, http.StatusBadRequest, "bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, realVerifier)
			me, other := h.login(), h.login()
			code, out, _ := h.api(me, "POST", "/api/account/password", tc.body)
			require.Equal(t, tc.want, code)
			require.Equal(t, tc.kind, out["error"])
			// unchanged: old password still logs in and other sessions live
			code, _, _ = h.api(other, "GET", "/api/status", "")
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
		})
	}
}

func repeatByte(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestAccountPasswordLockout(t *testing.T) {
	h := newHarness(t, realVerifier)
	c := h.login()
	bad := `{"current":"wrong-wrong-wrong","new":"a-brand-new-passphrase"}`
	for i := 0; i < 10; i++ {
		code, _, _ := h.api(c, "POST", "/api/account/password", bad)
		require.Equal(t, http.StatusForbidden, code, "attempt %d", i)
	}
	// locked: even the right password is refused, on either endpoint and on login
	good := jsonStr(map[string]string{"current": testPass, "new": "a-brand-new-passphrase"})
	code, _, rec := h.api(c, "POST", "/api/account/password", good)
	require.Equal(t, http.StatusTooManyRequests, code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	code, _, _ = h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","generate":true}`)
	require.Equal(t, http.StatusTooManyRequests, code)
	require.Equal(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	// the window ends
	h.clk.Advance(15*time.Minute + time.Second)
	code, _, _ = h.api(c, "POST", "/api/account/password", good)
	require.Equal(t, http.StatusNoContent, code)
}

func TestAccountPasswordSuccessClearsFailures(t *testing.T) {
	h := newHarness(t, realVerifier)
	c := h.login()
	for i := 0; i < 9; i++ {
		h.api(c, "POST", "/api/account/api-password", `{"current":"wrong-wrong-wrong","generate":true}`)
	}
	code, _, _ := h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","generate":true}`)
	require.Equal(t, http.StatusOK, code)
	for i := 0; i < 9; i++ { // the count restarted: nine more failures still do not lock
		code, _, _ = h.api(c, "POST", "/api/account/api-password", `{"current":"wrong-wrong-wrong","generate":true}`)
		require.Equal(t, http.StatusForbidden, code)
	}
}

func TestAccountAPIPassword(t *testing.T) {
	t.Run("generate returns it once and enables the API", func(t *testing.T) {
		var hook int
		h := newHarness(t, realVerifier, func(o *Options) { o.OnAPIPasswordChange = func() { hook++ } })
		c := h.login()
		_, me, _ := h.api(c, "GET", "/api/auth/me", "")
		require.Equal(t, false, me["api_enabled"])
		code, out, rec := h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","generate":true}`)
		require.Equal(t, http.StatusOK, code)
		pw, _ := out["api_password"].(string)
		require.Len(t, pw, 24)
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
		require.Equal(t, 1, hook)
		acct, _, _ := h.db.Account(context.Background())
		require.True(t, auth.CheckPassword(pw, acct.APIPasswordHash))
		require.NotEqual(t, pw, acct.APIPasswordHash)
		_, me, _ = h.api(c, "GET", "/api/auth/me", "")
		require.Equal(t, true, me["api_enabled"])
		// web password and sessions are untouched
		require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
		// a second generate yields a different password
		_, out2, _ := h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","generate":true}`)
		require.NotEqual(t, pw, out2["api_password"])
	})
	t.Run("explicit new password", func(t *testing.T) {
		h := newHarness(t, realVerifier)
		c := h.login()
		code, _, _ := h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","new":"my-own-api-passphrase"}`)
		require.Equal(t, http.StatusNoContent, code)
		acct, _, _ := h.db.Account(context.Background())
		require.True(t, auth.CheckPassword("my-own-api-passphrase", acct.APIPasswordHash))
	})
	for _, tc := range []struct {
		name, body string
		want       int
		kind       string
	}{
		{"wrong current", `{"current":"nope-nope-nope","generate":true}`, http.StatusForbidden, "bad_password"},
		{"neither new nor generate", `{"current":"correct-horse"}`, http.StatusBadRequest, "bad_request"},
		{"generate false", `{"current":"correct-horse","generate":false}`, http.StatusBadRequest, "bad_request"},
		{"both", `{"current":"correct-horse","new":"my-own-api-passphrase","generate":true}`, http.StatusBadRequest, "bad_request"},
		{"new too short", `{"current":"correct-horse","new":"four"}`, http.StatusBadRequest, "bad_new_password"},
		// A chosen API password guards the public ClientLogin: 16 minimum, not the web 5.
		{"new 15 chars", `{"current":"correct-horse","new":"fifteen-chars-x"}`, http.StatusBadRequest, "bad_new_password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hook int
			h := newHarness(t, realVerifier, func(o *Options) { o.OnAPIPasswordChange = func() { hook++ } })
			code, out, _ := h.api(h.login(), "POST", "/api/account/api-password", tc.body)
			require.Equal(t, tc.want, code)
			require.Equal(t, tc.kind, out["error"])
			require.Zero(t, hook)
			acct, _, _ := h.db.Account(context.Background())
			require.Empty(t, acct.APIPasswordHash)
		})
	}
}

// TestAPIPasswordChangeRevokesReaderToken wires the Reader API in front, as
// runServe does, and proves a change revokes the token at once and clears the
// ClientLogin memo.
func TestAPIPasswordChangeRevokesReaderToken(t *testing.T) {
	h := newHarness(t, realVerifier)
	var ga *greader.API
	h.srv.opt.OnAPIPasswordChange = func() { ga.InvalidateAccount() }
	ga = greader.New(greader.Options{DB: h.db, Verifier: h.srv.verifier, Now: h.clk.Now})
	front := ga.Front(h.mux)
	c := h.login()

	var apiPW string
	setAPI := func() {
		_, out, _ := h.api(c, "POST", "/api/account/api-password", `{"current":"`+testPass+`","generate":true}`)
		apiPW, _ = out["api_password"].(string)
	}
	setAPI()
	clientLogin := func(pw string) (int, string) {
		rec := doFront(front, "POST", "/api/greader.php/accounts/ClientLogin", "Email="+testUser+"&Passwd="+pw)
		return rec.Code, rec.Body.String()
	}
	code, body := clientLogin(apiPW)
	require.Equal(t, http.StatusOK, code, body)
	tok := authLine(body)
	require.NotEmpty(t, tok)
	tokenWorks := func() int {
		rec := doFront(front, "GET", "/api/greader.php/reader/api/0/token", "", "Authorization", "GoogleLogin auth="+tok)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, tokenWorks())

	old := apiPW
	setAPI()
	require.NotEqual(t, old, apiPW)
	require.Equal(t, http.StatusUnauthorized, tokenWorks(), "old token revoked immediately, not after the cache TTL")
	code, _ = clientLogin(old)
	require.Equal(t, http.StatusUnauthorized, code, "old password no longer works (memo cleared)")
	code, _ = clientLogin(apiPW)
	require.Equal(t, http.StatusOK, code)
}

// doFront sends a request through the Reader-front handler; kv are extra headers.
func doFront(front http.Handler, method, path, body string, kv ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "192.0.2.10:2"
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	w := httptest.NewRecorder()
	front.ServeHTTP(w, r)
	return w
}

// authLine extracts Auth=<tok> from a ClientLogin text body.
func authLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(l, "Auth="); ok {
			return v
		}
	}
	return ""
}

func TestPasswordLengthBounds(t *testing.T) {
	for _, tc := range []struct {
		name, pw string
		want     int
	}{
		{"four is too short", "abcd", http.StatusBadRequest},
		{"five is the minimum", "abcde", http.StatusNoContent},
		{"256 is the maximum", string(repeatByte('x', 256)), http.StatusNoContent},
		{"257 is too long", string(repeatByte('x', 257)), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, realVerifier)
			code, _, _ := h.api(h.login(), "POST", "/api/account/password", jsonStr(map[string]string{"current": testPass, "new": tc.pw}))
			require.Equal(t, tc.want, code)
		})
	}
}

// The CSP reads imgproxy.mode from an atomic cache that a PATCH refreshes.
func TestImgModeCacheFollowsPatch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	require.Equal(t, "all", h.srv.ImgMode(), "the default sends every image through Kipple")
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":"http_only"}`)
	require.Equal(t, 200, code)
	require.Equal(t, "http_only", h.srv.ImgMode())
	code, _, _ = h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":null}`)
	require.Equal(t, 200, code)
	require.Equal(t, "all", h.srv.ImgMode())
}

// A first-use fill that read the old mode must not store it after a PATCH has
// stored the new one: the CSP would keep the wrong img-src until a restart.
func TestImgModeCacheFillRacingPatch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	readOld := make(chan struct{})
	release := make(chan struct{})
	var paused atomic.Bool
	h.srv.imgMode.Store(nil) // start with an empty cache
	h.srv.imgModeRead = func(string) {
		if paused.CompareAndSwap(false, true) { // only the fill pauses between its read and its store
			close(readOld)
			<-release
		}
	}
	fillDone := make(chan string)
	go func() { fillDone <- h.srv.ImgMode() }()
	<-readOld

	patchDone := make(chan int)
	go func() {
		code, _, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":"http_only"}`)
		patchDone <- code
	}()
	// Unfixed, the PATCH finishes while the fill is parked; fixed, it waits for
	// the fill's store. Either way the fill is released after this.
	var code int
	select {
	case code = <-patchDone:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	require.Equal(t, "all", <-fillDone, "the fill returns what it read")
	if code == 0 {
		code = <-patchDone
	}
	require.Equal(t, 200, code)
	require.Equal(t, "http_only", h.srv.ImgMode(), "the PATCHed value is what the CSP sees")
}

func TestFavoritesLimit(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	build := func(n int) string {
		var b strings.Builder
		b.WriteString(`{"library.favorites":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"t":"feed","id":"%d"}`, i+1)
		}
		b.WriteString(`]}`)
		return b.String()
	}
	code, _, _ := h.api(c, "PATCH", "/api/settings", build(500))
	require.Equal(t, http.StatusOK, code)
	code, _, _ = h.api(c, "PATCH", "/api/settings", build(501))
	require.Equal(t, http.StatusBadRequest, code)
}

func TestNewSettingDefaultsAndBootstrap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, out, _ := h.api(c, "GET", "/api/settings", "")
	v := vals(out)
	require.Equal(t, true, v["greader.icon_urls"], "icons are sent to sync apps unless turned off")
	require.Equal(t, false, v["fetch.fulltext_all"])
	require.Equal(t, []any{}, v["library.favorites"])
	_, boot, _ := h.api(c, "GET", "/api/bootstrap", "")
	bs := boot["settings"].(map[string]any)
	require.Equal(t, []any{}, bs["library.favorites"])
	require.Equal(t, false, bs["fetch.fulltext_all"])
	require.Equal(t, true, bs["greader.icon_urls"])

	// A stored row (from before the default flipped) still wins.
	h.exec("INSERT INTO settings (key, value) VALUES ('greader.icon_urls', 'false')")
	_, out, _ = h.api(c, "GET", "/api/settings", "")
	require.Equal(t, false, vals(out)["greader.icon_urls"])
}
