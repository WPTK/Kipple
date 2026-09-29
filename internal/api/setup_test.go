package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/store"
)

// setupHarness is a server that started without an account (setup mode), driven
// through the Host gate like the real handler chain.
type setupHarness struct {
	*harness
	mgr    *setup.Manager
	dir    string
	banner *bytes.Buffer
	root   http.Handler
}

const (
	setupHost = "127.0.0.1:1919"
	setupPeer = "127.0.0.1:5555"
	setupPass = "a-real-password"
)

func newSetupHarness(t *testing.T, tune ...func(*Options)) *setupHarness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	banner := &bytes.Buffer{}
	mgr := setup.New(setup.Options{DataDir: dir, Out: banner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clk.Now})
	require.NoError(t, mgr.Begin())
	mgr.Announce("1919")

	h := &harness{t: t, db: db, hub: events.NewWithClock(clk), sched: &fakeSched{}, clk: clk, mux: http.NewServeMux()}
	opt := Options{DB: db, Sched: h.sched, Hub: h.hub, Now: clk.Now, Heartbeat: 20 * time.Millisecond, Setup: mgr,
		Gate: setup.Gate{Tailnet: true}}
	for _, f := range tune {
		f(&opt)
	}
	h.srv = New(opt)
	t.Cleanup(h.srv.Close)
	h.srv.Register(h.mux)
	h.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("spa")) }))
	return &setupHarness{harness: h, mgr: mgr, dir: dir, banner: banner, root: h.srv.HostGate(h.mux)}
}

// req sends a same-origin request from loopback to 127.0.0.1:1919 through the
// Host gate.
func (h *setupHarness) req(method, path, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = setupHost
	r.RemoteAddr = setupPeer
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Kipple-Client", "web")
	if method != "GET" && method != "HEAD" {
		r.Header.Set("Origin", "http://"+setupHost) // browsers send it on every POST
	}
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	h.root.ServeHTTP(rec, r)
	return rec
}

func (h *setupHarness) token() string {
	h.t.Helper()
	tok, ok, err := setup.ReadToken(h.dir)
	require.NoError(h.t, err)
	require.True(h.t, ok)
	return tok
}

func host(v string) func(*http.Request) { return func(r *http.Request) { r.Host = v } }
func peer(v string) func(*http.Request) { return func(r *http.Request) { r.RemoteAddr = v } }
func hdr(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}
func withCookies(cs ...*http.Cookie) func(*http.Request) {
	return func(r *http.Request) {
		for _, c := range cs {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func tokenBody(tok string) string {
	b, _ := json.Marshal(map[string]string{"token": tok})
	return string(b)
}

func (h *setupHarness) claim(tok string, mod ...func(*http.Request)) *http.Cookie {
	h.t.Helper()
	rec := h.req("POST", "/api/setup/claim", tokenBody(tok), mod...)
	require.Equal(h.t, http.StatusNoContent, rec.Code, rec.Body.String())
	c := cookieNamed(rec, setupCookieName)
	require.NotNil(h.t, c)
	return c
}

func accountBody(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func TestSetupHappyPathWithPassword(t *testing.T) {
	h := newSetupHarness(t)
	require.Contains(t, h.banner.String(), h.token(), "the banner shows the code")

	rec := h.req("GET", "/api/instance", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
	require.JSONEq(t, `{"setup":true,"auth":null}`, rec.Body.String())

	st := decode(t, h.req("GET", "/api/setup/state", ""))
	require.Equal(t, false, st["claimed"])
	require.Equal(t, map[string]any{"enabled": false, "verified": false}, st["access"])
	require.Equal(t, map[string]any{"reason": nil, "lan_reason": nil}, st["open"])
	require.Contains(t, st["token_hint"], "2026-09-24T12:00:00Z")

	// Hand-typed: lower case, spaces for dashes.
	sc := h.claim(strings.ToLower(strings.ReplaceAll(h.token(), "-", " ")))
	require.True(t, sc.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, sc.SameSite)
	require.Equal(t, "/api/setup", sc.Path)
	require.Equal(t, 3600, sc.MaxAge)
	require.False(t, sc.Secure)
	st = decode(t, h.req("GET", "/api/setup/state", "", withCookies(sc)))
	require.Equal(t, true, st["claimed"])

	rec = h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}), withCookies(sc))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"username":"reader","auth_mode":"password"}`, rec.Body.String())
	sess := cookieNamed(rec, cookieName)
	require.NotNil(t, sess)
	require.Equal(t, 90*24*3600, sess.MaxAge)
	cleared := cookieNamed(rec, setupCookieName)
	require.NotNil(t, cleared)
	require.Equal(t, -1, cleared.MaxAge, "the setup cookie is cleared")

	acct, ok, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.CreatedViaWizard, acct.CreatedVia)
	require.True(t, auth.CheckPassword(setupPass, acct.PasswordHash))

	// Setup is over: the routes answer 404 forever, the token file is gone.
	require.False(t, h.mgr.Pending())
	for _, p := range [][2]string{{"GET", "/api/setup/state"}, {"POST", "/api/setup/claim"}, {"POST", "/api/setup/account"}} {
		rec := h.req(p[0], p[1], tokenBody(h.token0()), withCookies(sc))
		require.Equal(t, http.StatusNotFound, rec.Code, p[1])
		require.JSONEq(t, `{"error":"not_found"}`, rec.Body.String())
	}
	_, ok, _ = setup.ReadToken(h.dir)
	require.False(t, ok)
	require.JSONEq(t, `{"setup":false,"auth":"password"}`, h.req("GET", "/api/instance", "").Body.String())

	// The session is a normal one, and onboarding is pending until finished.
	me := decode(t, h.req("GET", "/api/auth/me", "", withCookies(sess)))
	require.Equal(t, "password", me["auth_mode"])
	require.Equal(t, true, me["setup_pending"])
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/onboarding/complete", "", withCookies(sess)).Code)
	require.Equal(t, false, decode(t, h.req("GET", "/api/auth/me", "", withCookies(sess)))["setup_pending"])
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/onboarding/restart", "", withCookies(sess)).Code)
	require.Equal(t, true, decode(t, h.req("GET", "/api/auth/me", "", withCookies(sess)))["setup_pending"])
	require.Equal(t, http.StatusUnauthorized, h.req("POST", "/api/onboarding/restart", "").Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/onboarding/complete", "", withCookies(sess), hdr("Sec-Fetch-Site", "cross-site")).Code)

	// And the password signs in.
	b, _ := json.Marshal(map[string]string{"username": "reader", "password": setupPass})
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/login", string(b)).Code)
}

// token0 is any well-formed token (for bodies sent after setup ended).
func (h *setupHarness) token0() string { return "0000-0000-0000-0000-0000-0000" }

// A process that started with an account never registers the setup routes.
func TestSetupRoutesAbsentWithAccount(t *testing.T) {
	h := newHarness(t)
	for _, p := range [][2]string{{"GET", "/api/setup/state"}, {"POST", "/api/setup/claim"}, {"POST", "/api/setup/account"}} {
		rec := h.do(p[0], p[1], `{"token":"x"}`)
		require.Contains(t, []int{http.StatusNotFound, http.StatusUnauthorized}, rec.Code, p[1])
		require.NotEqual(t, http.StatusNoContent, rec.Code)
	}
	rec := h.do("GET", "/api/instance", "")
	require.JSONEq(t, `{"setup":false,"auth":"password"}`, rec.Body.String())
}

func TestSetupClaimLockoutAndRotation(t *testing.T) {
	h := newSetupHarness(t)
	good := h.token()
	for i := 0; i < 10; i++ {
		rec := h.req("POST", "/api/setup/claim", tokenBody("0000-0000-0000-0000-0000-0000"))
		require.Equal(t, http.StatusForbidden, rec.Code, "attempt %d", i)
		require.Equal(t, "bad_token", decode(t, rec)["error"])
	}
	// Locked: even the right token is refused from this address.
	rec := h.req("POST", "/api/setup/claim", tokenBody(good))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
	// The login lockout is separate: the same address can still sign in attempts.
	require.Equal(t, http.StatusUnauthorized, h.req("POST", "/api/auth/login", loginBody("x")).Code, "no account yet, but not locked")
	// Malformed bodies are not counted.
	h.clk.Advance(15 * time.Minute)
	for i := 0; i < 20; i++ {
		require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/claim", `{"token":""}`).Code)
		require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/claim", `not json`).Code)
	}
	// 100 wrong tokens in all (from many addresses) rotate the token.
	for i := 0; i < 90; i++ {
		rec := h.req("POST", "/api/setup/claim", tokenBody("0000-0000-0000-0000-0000-0000"), peer(fmt.Sprintf("192.0.2.%d:1", i%10+1)))
		require.Equal(t, http.StatusForbidden, rec.Code, "attempt %d", i)
	}
	now := h.token()
	require.NotEqual(t, good, now, "rotated after 100 failures")
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/setup/claim", tokenBody(good), peer("198.51.100.1:1")).Code, "the old token is dead")
	h.claim(now, peer("198.51.100.2:1"))
}

func TestSetupAccountNeedsTheSetupSession(t *testing.T) {
	h := newSetupHarness(t)
	body := accountBody(map[string]any{"username": "reader", "password": setupPass})
	rec := h.req("POST", "/api/setup/account", body)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "setup_session", decode(t, rec)["error"])
	forged := &http.Cookie{Name: setupCookieName, Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	require.Equal(t, http.StatusUnauthorized, h.req("POST", "/api/setup/account", body, withCookies(forged)).Code)

	first := h.claim(h.token())
	second := h.claim(h.token())
	require.Equal(t, http.StatusUnauthorized, h.req("POST", "/api/setup/account", body, withCookies(first)).Code, "a new claim replaces the old session")
	h.clk.Advance(time.Hour)
	require.Equal(t, http.StatusUnauthorized, h.req("POST", "/api/setup/account", body, withCookies(second)).Code, "the setup session expires")
	_, ok, _ := h.db.Account(context.Background())
	require.False(t, ok)
}

func TestSetupAccountValidation(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	for _, tc := range []struct {
		body map[string]any
		code int
		err  string
	}{
		{map[string]any{"username": "bad name", "password": setupPass}, 400, "bad_username"},
		{map[string]any{"username": "", "password": setupPass}, 400, "bad_username"},
		{map[string]any{"username": strings.Repeat("x", 65), "password": setupPass}, 400, "bad_username"},
		{map[string]any{"username": "reader"}, 400, "bad_request"},
		{map[string]any{"username": "reader", "password": setupPass, "passwordless": "open"}, 400, "bad_request"},
		{map[string]any{"username": "reader", "password": "four"}, 400, "bad_new_password"},
		{map[string]any{"username": "reader", "password": "change-me"}, 400, "bad_new_password"},
		{map[string]any{"username": "reader", "password": strings.Repeat("x", 257)}, 400, "bad_new_password"},
		{map[string]any{"username": "reader", "passwordless": "sideways"}, 400, "bad_request"},
		{map[string]any{"username": "reader", "passwordless": "open"}, 400, "ack_required"},
		{map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": false}, 400, "ack_required"},
		{map[string]any{"username": "reader", "passwordless": "access"}, 403, "access_required"},
	} {
		rec := h.req("POST", "/api/setup/account", accountBody(tc.body), withCookies(sc))
		require.Equal(t, tc.code, rec.Code, "%v: %s", tc.body, rec.Body.String())
		require.Equal(t, tc.err, decode(t, rec)["error"], "%v", tc.body)
	}
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/account", "{", withCookies(sc)).Code)
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/account", `{"username":"a","password":"`+strings.Repeat("x", 5000)+`"}`, withCookies(sc)).Code, "the body is bounded")
	_, ok, _ := h.db.Account(context.Background())
	require.False(t, ok, "nothing was created")
	require.True(t, h.mgr.Pending())
}

// Cross-site and header-less writes are refused before anything else, and no
// setup route ever answers with CORS headers.
func TestSetupCSRF(t *testing.T) {
	h := newSetupHarness(t)
	crossSite := hdr("Sec-Fetch-Site", "cross-site")
	noClient := func(r *http.Request) { r.Header.Del("X-Kipple-Client") }
	legacyOrigin := func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "http://evil.example") }
	for _, mod := range []func(*http.Request){crossSite, noClient, legacyOrigin} {
		rec := h.req("POST", "/api/setup/claim", tokenBody(h.token()), mod)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "origin", decode(t, rec)["error"])
		require.Nil(t, cookieNamed(rec, setupCookieName))
	}
	sc := h.claim(h.token())
	for _, mod := range []func(*http.Request){crossSite, noClient, legacyOrigin} {
		rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}), withCookies(sc), mod)
		require.Equal(t, http.StatusForbidden, rec.Code)
	}
	// A matching Origin (no Sec-Fetch-Site, an older browser) is fine.
	okOrigin := func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "http://"+setupHost) }
	h.claim(h.token(), okOrigin)
}

// The Host gate refuses DNS-rebinding shapes on every route in setup mode.
func TestSetupHostGate(t *testing.T) {
	h := newSetupHarness(t, func(o *Options) { o.AllowedHosts = []string{"rss.example.com"} })
	for _, hv := range []string{"evil.example:1919", "evil.example.", "EVIL.EXAMPLE:1919", "127.0.0.1.nip.io:1919",
		"localhost.evil.example", "", "evil.example:1919:1", "127.1:1919", "rss.example.com.evil.example"} {
		for _, path := range []string{"/api/instance", "/api/setup/state", "/", "/healthz", "/api/greader.php/accounts/ClientLogin"} {
			rec := h.req("GET", path, "", host(hv))
			require.Equal(t, http.StatusMisdirectedRequest, rec.Code, "%q %s", hv, path)
			require.Contains(t, rec.Body.String(), "KIPPLE_ALLOWED_HOSTS")
			require.NotContains(t, rec.Body.String(), "evil", "the refused name is not echoed")
		}
		rec := h.req("POST", "/api/setup/claim", tokenBody(h.token()), host(hv))
		require.Equal(t, http.StatusMisdirectedRequest, rec.Code, hv)
	}
	for _, hv := range []string{"127.0.0.1:1919", "[::1]:1919", "localhost:1919", "nas:1919", "nas.local", "box.tail1.ts.net", "rss.example.com", "192.168.1.10:1919"} {
		require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(hv)).Code, hv)
	}
	// Once the account exists with a password, unlisted names are only logged.
	sc := h.claim(h.token())
	require.Equal(t, http.StatusCreated, h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}), withCookies(sc)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("evil.example")).Code)
}

// N token holders and M guessers race claim+account on one server and store:
// exactly one account, created once, with exactly one session.
func TestSetupClaimRaceHasExactlyOneWinner(t *testing.T) {
	h := newSetupHarness(t)
	tok := h.token()
	const n, m = 12, 12
	var wg sync.WaitGroup
	codes := make(chan int, n+m)
	start := make(chan struct{})
	for i := 0; i < n+m; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			try := tok
			if i >= n {
				try = "0000-0000-0000-0000-0000-000" + string("0123456789ABCDEFGHJKMNPQRSTVWXYZ"[i%32])
			}
			p := peer(fmt.Sprintf("192.0.2.%d:1", i+1))
			rec := h.req("POST", "/api/setup/claim", tokenBody(try), p)
			c := cookieNamed(rec, setupCookieName)
			var cs []*http.Cookie
			if c != nil {
				cs = append(cs, c)
			}
			body := accountBody(map[string]any{"username": fmt.Sprintf("user%d", i), "password": setupPass})
			codes <- h.req("POST", "/api/setup/account", body, withCookies(cs...), p).Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict, http.StatusUnauthorized, http.StatusNotFound:
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 1, h.count("SELECT count(*) FROM account"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions"), "one session, the winner's")
	require.False(t, h.mgr.Pending())
}

// The same setup session replayed in parallel still creates one account.
func TestSetupAccountReplayRace(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	var wg sync.WaitGroup
	codes := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := accountBody(map[string]any{"username": fmt.Sprintf("user%d", i), "password": setupPass})
			codes <- h.req("POST", "/api/setup/account", body, withCookies(sc)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created := 0
	for c := range codes {
		if c == http.StatusCreated {
			created++
		} else {
			require.Contains(t, []int{http.StatusConflict, http.StatusUnauthorized, http.StatusNotFound}, c)
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions"))
}

func TestSetupOpenMode(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	open := accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true})

	// Choosing open mode must happen from where open mode would work.
	for _, tc := range []struct {
		mod    func(*http.Request)
		reason string
	}{
		{peer("192.168.1.20:5000"), "peer"},
		{peer("203.0.113.9:5000"), "peer"},
		{hdr("X-Forwarded-For", "203.0.113.9"), "forwarded"},
		{hdr("CF-Connecting-IP", "203.0.113.9"), "forwarded"},
		{hdr("Cf-Access-Jwt-Assertion", "x"), "forwarded"},
		{hdr("Forwarded", "for=203.0.113.9"), "forwarded"},
	} {
		rec := h.req("POST", "/api/setup/account", open, withCookies(sc), tc.mod)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		out := decode(t, rec)
		require.Equal(t, "open_refused", out["error"])
		require.Equal(t, tc.reason, out["reason"])
	}
	rec := h.req("POST", "/api/setup/account", open, withCookies(sc))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"username":"reader","auth_mode":"open"}`, rec.Body.String())
	first := cookieNamed(rec, cookieName)
	require.JSONEq(t, `{"setup":false,"auth":"open"}`, h.req("GET", "/api/instance", "").Body.String())

	// A stale login form gets 409, not a failed sign-in.
	b, _ := json.Marshal(map[string]string{"username": "reader", "password": "anything"})
	rec = h.req("POST", "/api/auth/login", string(b))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "open_mode", decode(t, rec)["error"])

	// /api/auth/open: the open gate, same-origin, and never counted.
	for i := 0; i < 15; i++ {
		rec = h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000"))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "peer", decode(t, rec)["reason"])
	}
	rec = h.req("POST", "/api/auth/open", "", hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, "forwarded", decode(t, rec)["reason"])
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", hdr("Sec-Fetch-Site", "cross-site")).Code, "no login CSRF")
	rec = h.req("POST", "/api/auth/open", "", peer("100.100.1.2:5000"))
	require.Equal(t, http.StatusNoContent, rec.Code, "a Tailscale peer")
	tsSess := cookieNamed(rec, cookieName)
	require.NotNil(t, tsSess)

	// DNS rebinding against open mode: every route refuses the wrong Host.
	for _, path := range []string{"/api/instance", "/api/bootstrap", "/"} {
		require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", path, "", host("evil.example:1919"), withCookies(first)).Code, path)
	}
	require.Equal(t, http.StatusMisdirectedRequest, h.req("POST", "/api/auth/open", "", host("evil.example:1919")).Code)

	// The LAN opt-in (a session holder's choice).
	rec = h.req("PATCH", "/api/settings", `{"security.open_lan":true}`, withCookies(first))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000")).Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", peer("203.0.113.9:5000")).Code, "public peers never")
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000"), hdr("X-Forwarded-For", "203.0.113.9")).Code, "a proxy on the LAN is still a proxy")
	lanSess := cookieNamed(h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000")), cookieName)
	require.NotNil(t, lanSess)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/auth/me", "", withCookies(lanSess), peer("192.168.1.20:5000")).Code)
	// Turning the opt-in off again ends that device's access at once: in open
	// mode the gate applies to every signed-in request, not only to sign-in.
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.open_lan":false}`, withCookies(first)).Code)
	rec = h.req("GET", "/api/auth/me", "", withCookies(lanSess), peer("192.168.1.20:5000"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "peer", decode(t, rec)["reason"])
	// And a session taken elsewhere, used through a proxy, is refused too.
	require.Equal(t, http.StatusForbidden, h.req("GET", "/api/bootstrap", "", withCookies(first), hdr("X-Forwarded-For", "203.0.113.9")).Code)
	// A same-machine proxy that sends no forwarding headers but rewrites Host
	// (nginx's default proxy_pass) still forwards the browser's public Origin.
	rec = h.req("POST", "/api/auth/open", "", hdr("Origin", "https://rss.example.com"))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "forwarded", decode(t, rec)["reason"])
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", func(r *http.Request) { r.Header.Del("Origin") }).Code, "no Origin, no session")

	// A Reader API password in open mode: no current password, but the open gate.
	rec = h.req("POST", "/api/account/api-password", `{"generate":true}`, withCookies(first), peer("203.0.113.9:5000"))
	require.Equal(t, http.StatusForbidden, rec.Code, "an existing session from outside cannot mint a credential")
	require.Equal(t, "open_refused", decode(t, rec)["error"])
	rec = h.req("POST", "/api/account/api-password", `{"generate":true}`, withCookies(first))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, decode(t, rec)["api_password"], 24)

	// Setting a password leaves open mode (session + same-origin are the proof)
	// and signs every other session out.
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/account/password", `{"remove":true}`, withCookies(first)).Code)
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/account/password", `{"open":true}`, withCookies(first)).Code, "already open: a no-op")
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/account/password", `{"new":"four"}`, withCookies(first)).Code)
	rec = h.req("POST", "/api/account/password", `{"new":"`+setupPass+`"}`, withCookies(first))
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	acct, _, _ := h.db.Account(context.Background())
	require.Equal(t, store.AuthStandard, acct.AuthMode)
	require.Equal(t, http.StatusUnauthorized, h.req("GET", "/api/auth/me", "", withCookies(tsSess)).Code, "other sessions are revoked")
	require.Equal(t, http.StatusOK, h.req("GET", "/api/auth/me", "", withCookies(first)).Code)
	require.Equal(t, http.StatusNotFound, h.req("POST", "/api/auth/open", "").Code, "not open any more")
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("evil.example")).Code, "the Host gate only logs now")
}

// Switching a password account to open mode needs the password and the open gate.
func TestSwitchToOpenMode(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}), withCookies(sc))
	require.Equal(t, http.StatusCreated, rec.Code)
	sess := cookieNamed(rec, cookieName)
	b, _ := json.Marshal(map[string]string{"username": "reader", "password": setupPass})
	other := cookieNamed(h.req("POST", "/api/auth/login", string(b)), cookieName)
	require.NotNil(t, other)

	body := func(cur string) string {
		b, _ := json.Marshal(map[string]any{"current": cur, "open": true})
		return string(b)
	}
	rec = h.req("POST", "/api/account/password", body("wrong-password"), withCookies(sess))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "bad_password", decode(t, rec)["error"])
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/account/password", `{"current":"x","open":true,"new":"abcdefg"}`, withCookies(sess)).Code)
	for _, mod := range []func(*http.Request){peer("192.168.1.20:5000"), hdr("CF-Connecting-IP", "203.0.113.9")} {
		rec = h.req("POST", "/api/account/password", body(setupPass), withCookies(sess), mod)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "open_refused", decode(t, rec)["error"], "it cannot be turned on from outside")
	}
	acct, _, _ := h.db.Account(context.Background())
	require.Equal(t, store.AuthStandard, acct.AuthMode)

	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/account/password", body(setupPass), withCookies(sess)).Code)
	acct, _, _ = h.db.Account(context.Background())
	require.Equal(t, store.AuthOpen, acct.AuthMode)
	require.Empty(t, acct.PasswordHash)
	require.Equal(t, http.StatusUnauthorized, h.req("GET", "/api/auth/me", "", withCookies(other)).Code, "the mode change revokes other sessions")
	require.Equal(t, "open", decode(t, h.req("GET", "/api/auth/me", "", withCookies(sess)))["auth_mode"])
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("evil.example")).Code, "the Host gate enforces at once")
}

// Setup behind Cloudflare Access still needs the token; the Access-only
// account needs a verified token on the account request itself.
func TestSetupWithAccess(t *testing.T) {
	h := newSetupHarness(t, withAccess(t))
	k, other := accessKeys(t)
	st := decode(t, h.req("GET", "/api/setup/state", ""))
	require.Equal(t, map[string]any{"enabled": true, "verified": false}, st["access"])
	st = decode(t, h.req("GET", "/api/setup/state", "", withJWT(h.jwt(k, nil))))
	require.Equal(t, map[string]any{"enabled": true, "verified": true}, st["access"])
	// Access proves an identity, not ownership: no claim without the token.
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "passwordless": "access"}), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	sc := h.claim(h.token())
	acc := accountBody(map[string]any{"username": "reader", "passwordless": "access"})
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/setup/account", acc, withCookies(sc)).Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/setup/account", acc, withCookies(sc), withJWT(h.jwt(other, nil))).Code)
	rec = h.req("POST", "/api/setup/account", acc, withCookies(sc), withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"username":"reader","auth_mode":"access"}`, rec.Body.String())
	acct, _, _ := h.db.Account(context.Background())
	require.Equal(t, store.AuthStandard, acct.AuthMode)
	require.Empty(t, acct.PasswordHash)
}

// No route, new or old, ever answers with CORS headers, preflight included.
func TestNoCORSHeadersAnywhere(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	routes := [][2]string{
		{"GET", "/api/instance"}, {"GET", "/api/setup/state"}, {"POST", "/api/setup/claim"}, {"POST", "/api/setup/account"},
		{"POST", "/api/auth/open"}, {"POST", "/api/auth/login"}, {"GET", "/api/auth/me"}, {"GET", "/api/bootstrap"},
		{"POST", "/api/onboarding/complete"}, {"POST", "/api/onboarding/restart"}, {"GET", "/api/starter-feeds"},
		{"POST", "/api/starter-feeds"}, {"GET", "/api/settings"}, {"PATCH", "/api/settings"}, {"POST", "/api/account/password"},
		{"GET", "/healthz"}, {"GET", "/"}, {"OPTIONS", "/api/setup/claim"}, {"OPTIONS", "/api/auth/open"},
	}
	for _, rt := range routes {
		for _, mod := range []func(*http.Request){
			hdr("Origin", "https://evil.example"),
			func(r *http.Request) {
				r.Header.Set("Origin", "https://evil.example")
				r.Header.Set("Access-Control-Request-Method", "POST")
				r.Header.Set("Access-Control-Request-Headers", "x-kipple-client, content-type")
			},
		} {
			rec := h.req(rt[0], rt[1], `{}`, withCookies(sc), mod)
			for k := range rec.Header() {
				require.False(t, strings.HasPrefix(strings.ToLower(k), "access-control-"), "%s %s answered %s", rt[0], rt[1], k)
			}
		}
	}
}

func TestSettingsTZEnvOverrideAndDefault(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	tzView := func() map[string]any {
		out := decode(t, h.do("GET", "/api/settings", "", withCookie(c)))
		for _, s := range out["settings"].([]any) {
			if m := s.(map[string]any); m["key"] == "tz" {
				return m
			}
		}
		t.Fatal("no tz entry")
		return nil
	}
	v := tzView()
	require.Contains(t, v, "env_override")
	require.Nil(t, v["env_override"])
	require.Equal(t, "UTC", v["default"], "new installs default to UTC")

	t.Cleanup(func() { _ = store.SetEnvZone("") })
	require.NoError(t, store.SetEnvZone("America/Chicago"))
	v = tzView()
	require.Equal(t, "America/Chicago", v["env_override"])
	for _, body := range []string{`{"tz":"Asia/Tokyo"}`, `{"tz":null}`} {
		rec := h.do("PATCH", "/api/settings", body, withCookie(c))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		out := decode(t, rec)
		require.Equal(t, "invalid_settings", out["error"])
		require.Contains(t, fmt.Sprint(out["issues"]), "set by the TZ environment variable")
	}
	// Other keys in the same PATCH are refused with it (all-or-nothing).
	require.Equal(t, http.StatusBadRequest, h.do("PATCH", "/api/settings", `{"tz":"UTC","stats.enabled":false}`, withCookie(c)).Code)

	require.NoError(t, store.SetEnvZone(""))
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", `{"tz":"Asia/Tokyo"}`, withCookie(c)).Code)
	require.Equal(t, "Asia/Tokyo", tzView()["value"])
	// Other settings carry no env_override at all.
	out := decode(t, h.do("GET", "/api/settings", "", withCookie(c)))
	for _, s := range out["settings"].([]any) {
		if m := s.(map[string]any); m["key"] != "tz" {
			require.NotContains(t, m, "env_override", m["key"])
		}
	}
}

func TestSettingsSecurityKeys(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	rec := h.do("PATCH", "/api/settings", `{"security.allowed_hosts":[" RSS.example.com. ","*.Example.org","rss.example.com"],"security.open_lan":true}`, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	vals := decode(t, rec)["values"].(map[string]any)
	require.Equal(t, []any{"rss.example.com", "*.example.org"}, vals["security.allowed_hosts"])
	require.Equal(t, true, vals["security.open_lan"])
	for _, bad := range []string{`{"security.allowed_hosts":"rss.example.com"}`, `{"security.allowed_hosts":["https://x.example"]}`,
		`{"security.allowed_hosts":["*"]}`, `{"security.allowed_hosts":[1]}`, `{"security.open_lan":"yes"}`} {
		require.Equal(t, http.StatusBadRequest, h.do("PATCH", "/api/settings", bad, withCookie(c)).Code, bad)
	}
	var many []string
	for i := 0; i < 65; i++ {
		many = append(many, fmt.Sprintf("%q", fmt.Sprintf("h%d.example.com", i)))
	}
	require.Equal(t, http.StatusBadRequest, h.do("PATCH", "/api/settings", `{"security.allowed_hosts":[`+strings.Join(many, ",")+`]}`, withCookie(c)).Code)
}

// A name added in Settings passes the Host gate at once (the cache is dropped).
func TestAllowedHostsSettingReachesTheGate(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}), withCookies(sc))
	require.Equal(t, http.StatusCreated, rec.Code)
	sess := cookieNamed(rec, cookieName)
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("reader.example.net")).Code)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.allowed_hosts":["reader.example.net"]}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("reader.example.net")).Code)
}

func TestStarterFeeds(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	out := decode(t, h.do("GET", "/api/starter-feeds", "", withCookie(c)))
	require.Equal(t, true, out["available"])
	cats := out["categories"].([]any)
	require.NotEmpty(t, cats)
	first := cats[0].(map[string]any)["feeds"].([]any)[0].(map[string]any)
	id := first["id"].(string)
	require.Equal(t, false, first["subscribed"])

	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/starter-feeds", "").Code)
	rec := h.do("POST", "/api/starter-feeds", `{"ids":["no-such-feed"]}`, withCookie(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "unknown_id", decode(t, rec)["error"])
	// URLs are never taken from the client.
	rec = h.do("POST", "/api/starter-feeds", `{"ids":[],"url":"http://127.0.0.1/feed","urls":["http://127.0.0.1/"]}`, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"added":0,"existing":0,"run_id":null}`, rec.Body.String())
	require.Equal(t, 0, h.count("SELECT count(*) FROM feeds"))

	rec = h.do("POST", "/api/starter-feeds", `{"ids":["`+id+`","`+id+`"],"folders":true}`, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := decode(t, rec)
	require.EqualValues(t, 1, res["added"])
	require.EqualValues(t, 0, res["existing"])
	require.NotNil(t, res["run_id"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM feeds WHERE allow_private_net = 0"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM folders WHERE name = '"+cats[0].(map[string]any)["title"].(string)+"'"))
	rec = h.do("POST", "/api/starter-feeds", `{"ids":["`+id+`"]}`, withCookie(c))
	require.JSONEq(t, `{"added":0,"existing":1,"run_id":null}`, rec.Body.String())
	out = decode(t, h.do("GET", "/api/starter-feeds", "", withCookie(c)))
	require.Equal(t, true, out["categories"].([]any)[0].(map[string]any)["feeds"].([]any)[0].(map[string]any)["subscribed"])
	require.Equal(t, http.StatusForbidden, h.do("POST", "/api/starter-feeds", `{"ids":[]}`, withCookie(c), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }).Code)
}

// In password mode the gate logs an unlisted Host (once an hour) instead of refusing.
func TestHostGateLogsOnlyInPasswordMode(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t, func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(&logs, nil)) })
	root := h.srv.HostGate(h.mux)
	get := func(hv string) int {
		r := httptest.NewRequest("GET", "/api/instance", nil)
		r.Host = hv
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, r)
		return rec.Code
	}
	require.Equal(t, http.StatusOK, get("rss.example.com"))
	require.Equal(t, http.StatusOK, get("other.example.com"))
	require.Equal(t, 1, strings.Count(logs.String(), "not in the allowed list"), "once an hour")
	h.clk.Advance(time.Hour)
	require.Equal(t, http.StatusOK, get("rss.example.com"))
	require.Equal(t, 2, strings.Count(logs.String(), "not in the allowed list"))
}

// A row that appears while setup is pending (a lost race, or an error reported
// after the insert committed) still ends setup mode in this process.
func TestSetupEndsWhenTheRowExistsWhateverTheAnswer(t *testing.T) {
	h := newSetupHarness(t)
	sc := h.claim(h.token())
	_, err := h.db.CreateAccount(context.Background(), store.Account{Username: "first", PasswordHash: "h", Secret: testSecret, CreatedVia: store.CreatedViaWizard})
	require.NoError(t, err)
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "second", "password": setupPass}), withCookies(sc))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "already_set_up", decode(t, rec)["error"])
	require.False(t, h.mgr.Pending())
	require.JSONEq(t, `{"setup":false,"auth":"password"}`, h.req("GET", "/api/instance", "").Body.String())
	_, ok, _ := setup.ReadToken(h.dir)
	require.False(t, ok)
}

// A mode never read enforces (fail closed); a failed re-read after a change
// keeps the last known mode instead of forgetting open mode.
func TestModeSnapshotFailsClosed(t *testing.T) {
	h := newHarness(t)
	require.True(t, h.srv.enforceHosts(&modeSnapshot{failed: true}))
	require.False(t, h.srv.enforceHosts(&modeSnapshot{mode: store.AuthStandard}))

	s := newSetupHarness(t)
	sc := s.claim(s.token())
	require.Equal(t, http.StatusCreated, s.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}), withCookies(sc)).Code)
	require.Equal(t, store.AuthOpen, s.srv.snapshot(context.Background()).mode)
	s.srv.invalidateMode()
	require.NoError(t, s.db.Close()) // every read fails from here on
	snap := s.srv.snapshot(context.Background())
	require.Equal(t, store.AuthOpen, snap.mode, "the last known mode, not a forgotten one")
	require.Equal(t, http.StatusMisdirectedRequest, s.req("GET", "/api/instance", "", host("evil.example")).Code)
}

// In a container even this computer arrives from the bridge gateway, which the
// gate cannot tell from the LAN: open mode then needs the LAN opt-in, chosen
// with the account and stored in the same transaction.
func TestSetupOpenModeFromAContainerGateway(t *testing.T) {
	h := newSetupHarness(t)
	gw := peer("172.17.0.1:40000")
	st := decode(t, h.req("GET", "/api/setup/state", "", gw))
	require.Equal(t, map[string]any{"reason": "peer", "lan_reason": nil}, st["open"])
	st = decode(t, h.req("GET", "/api/setup/state", ""))
	require.Equal(t, map[string]any{"reason": nil, "lan_reason": nil}, st["open"], "a bare binary on this computer")
	st = decode(t, h.req("GET", "/api/setup/state", "", peer("203.0.113.9:1")))
	require.Equal(t, map[string]any{"reason": "peer", "lan_reason": "peer"}, st["open"], "not from the internet at all")

	sc := h.claim(h.token(), gw)
	open := map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}
	rec := h.req("POST", "/api/setup/account", accountBody(open), withCookies(sc), gw)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "peer", decode(t, rec)["reason"])
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/account",
		accountBody(map[string]any{"username": "reader", "password": setupPass, "open_lan": true}), withCookies(sc), gw).Code, "open_lan is for open mode only")
	open["open_lan"] = true
	rec = h.req("POST", "/api/setup/account", accountBody(open), withCookies(sc), gw)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	sess := cookieNamed(rec, cookieName)
	vals := decode(t, h.req("GET", "/api/settings", "", withCookies(sess), gw))["values"].(map[string]any)
	require.Equal(t, true, vals["security.open_lan"])
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", gw).Code)
}

// An Access-only account cannot switch to open mode: its proof is an Access
// header, which the open gate refuses. It is told to set a password first.
func TestSwitchToOpenNeedsAPassword(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "", store.AuthStandard, sessionID(c.Value)))
	rec := h.do("POST", "/api/account/password", `{"open":true}`, withCookie(c), func(r *http.Request) {
		r.Header.Set("Origin", "http://example.com")
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "password_required", decode(t, rec)["error"])
}
