package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	mgr     *setup.Manager
	started *atomic.Int32 // how many times the background work was started
	root    http.Handler
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
	started := &atomic.Int32{}
	mgr := setup.NewPending(func() { started.Add(1) })

	h := &harness{t: t, db: db, hub: events.NewWithClock(clk), sched: &fakeSched{}, clk: clk, mux: http.NewServeMux()}
	opt := Options{DB: db, Sched: h.sched, Hub: h.hub, Now: clk.Now, Heartbeat: 20 * time.Millisecond, Setup: mgr,
		Gate: setup.Gate{Tailnet: func() []netip.Addr { return []netip.Addr{tailnetLocal} }}}
	for _, f := range tune {
		f(&opt)
	}
	h.srv = New(opt)
	t.Cleanup(h.srv.Close)
	h.srv.Register(h.mux)
	h.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("spa")) }))
	return &setupHarness{harness: h, mgr: mgr, started: started, root: h.srv.HostGate(h.mux)}
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

// createAccount posts the account form (no setup code: the first request wins).
func (h *setupHarness) createAccount(fields map[string]any, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	return h.req("POST", "/api/setup/account", accountBody(fields), mod...)
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
	t.Parallel()
	h := newSetupHarness(t)

	// The first screen asks one thing: is there an account? Nothing else is
	// asked before it exists.
	rec := h.req("GET", "/api/instance", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
	st := decode(t, rec)
	require.Equal(t, true, st["setup"])
	require.Nil(t, st["auth"])
	require.Equal(t, map[string]any{"enabled": false, "verified": false}, st["access"])
	require.Equal(t, map[string]any{"reason": nil}, st["open"])
	require.Zero(t, h.started.Load())

	rec = h.createAccount(map[string]any{"username": "reader", "password": setupPass})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"username":"reader","auth_mode":"password"}`, rec.Body.String())
	sess := cookieNamed(rec, cookieName)
	require.NotNil(t, sess, "the claim signs this browser in with the normal session cookie")
	require.Equal(t, 90*24*3600, sess.MaxAge)
	for _, c := range rec.Result().Cookies() {
		require.Equal(t, cookieName, c.Name, "no other cookie: there is no setup session")
	}

	acct, ok, err := h.db.Account(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, store.CreatedViaWizard, acct.CreatedVia)
	require.True(t, auth.CheckPassword(setupPass, acct.PasswordHash))

	// Setup is over: the route answers 404 forever, and the background work started once.
	require.False(t, h.mgr.Pending())
	require.EqualValues(t, 1, h.started.Load())
	rec = h.createAccount(map[string]any{"username": "other", "password": setupPass})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.JSONEq(t, `{"error":"not_found"}`, rec.Body.String())
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

// While Kipple has no account, only /api/instance, the account route, /healthz
// and the app shell answer; everything else says there is nobody to sign in as.
func TestUnclaimedRouteTable(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	// The authenticated API: 401, whatever is asked.
	for _, rt := range [][2]string{
		{"GET", "/api/auth/me"}, {"POST", "/api/auth/logout"}, {"GET", "/api/bootstrap"}, {"GET", "/api/items"},
		{"GET", "/api/settings"}, {"PATCH", "/api/settings"}, {"GET", "/api/status"}, {"POST", "/api/refresh"},
		{"GET", "/api/events"}, {"POST", "/api/account/password"}, {"POST", "/api/backup"}, {"GET", "/api/starter-feeds"},
		{"POST", "/api/starter-feeds"}, {"POST", "/api/onboarding/complete"}, {"GET", "/api/stats/summary"},
		{"GET", "/api/feeds/1/icon"}, {"GET", "/img/x/y/z"},
	} {
		rec := h.req(rt[0], rt[1], `{}`)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s: %s", rt[0], rt[1], rec.Body.String())
		require.Empty(t, rec.Result().Cookies(), "%s %s", rt[0], rt[1])
	}
	// A sign-in has nobody to sign in as: 409 setup_required, not "wrong password".
	for _, path := range []string{"/api/auth/login", "/api/auth/open"} {
		rec := h.req("POST", path, `{"username":"a","password":"b"}`)
		require.Equal(t, http.StatusConflict, rec.Code, path)
		require.Equal(t, "setup_required", decode(t, rec)["error"], path)
		require.Nil(t, cookieNamed(rec, cookieName), path)
	}
	// What does answer.
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "").Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/healthz", "").Code)
	require.Equal(t, "spa", h.req("GET", "/", "").Body.String())
	require.Equal(t, http.StatusBadRequest, h.createAccount(map[string]any{"username": "bad name", "password": setupPass}).Code, "the claim route is mounted")
	// Nothing has started and nothing was created.
	require.Zero(t, h.started.Load())
	require.Zero(t, h.count("SELECT count(*) FROM account"))
	require.Zero(t, h.count("SELECT count(*) FROM sessions"))
}

// A process that started with an account never registers the setup routes, so
// the /api/ catch-all answers a signed-out caller 401. (The process that did the
// claim itself keeps the route and answers 404: TestSetupHappyPathWithPassword.)
func TestSetupRoutesAbsentWithAccount(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, p := range [][2]string{{"GET", "/api/setup/state"}, {"POST", "/api/setup/claim"}, {"POST", "/api/setup/account"}} {
		rec := h.do(p[0], p[1], `{"username":"x","password":"abcdefgh"}`)
		require.Equal(t, http.StatusUnauthorized, rec.Code, p[1])
	}
	rec := h.do("GET", "/api/instance", "")
	require.JSONEq(t, `{"setup":false,"auth":"password"}`, rec.Body.String())
}

func TestSetupAccountValidation(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
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
		rec := h.req("POST", "/api/setup/account", accountBody(tc.body))
		require.Equal(t, tc.code, rec.Code, "%v: %s", tc.body, rec.Body.String())
		require.Equal(t, tc.err, decode(t, rec)["error"], "%v", tc.body)
	}
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/account", "{").Code)
	require.Equal(t, http.StatusBadRequest, h.req("POST", "/api/setup/account", `{"username":"a","password":"`+strings.Repeat("x", 5000)+`"}`).Code, "the body is bounded")
	_, ok, _ := h.db.Account(context.Background())
	require.False(t, ok, "nothing was created")
	require.True(t, h.mgr.Pending())
}

// Cross-site and header-less writes are refused before anything else: the claim
// has no secret, so these (plus the Host gate) are what keep a page in the
// owner's browser from creating the account.
func TestSetupCSRF(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	crossSite := hdr("Sec-Fetch-Site", "cross-site")
	noClient := func(r *http.Request) { r.Header.Del("X-Kipple-Client") }
	legacyOrigin := func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "http://evil.example") }
	noOrigin := func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Del("Origin") }
	for _, mod := range []func(*http.Request){crossSite, noClient, legacyOrigin, noOrigin} {
		rec := h.createAccount(map[string]any{"username": "reader", "password": setupPass}, mod)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "origin", decode(t, rec)["error"])
		require.Nil(t, cookieNamed(rec, cookieName))
	}
	require.Zero(t, h.count("SELECT count(*) FROM account"), "no refused request created the account")
	require.True(t, h.mgr.Pending())
	// A matching Origin (no Sec-Fetch-Site, an older browser) is fine.
	okOrigin := func(r *http.Request) { r.Header.Del("Sec-Fetch-Site"); r.Header.Set("Origin", "http://"+setupHost) }
	require.Equal(t, http.StatusCreated, h.createAccount(map[string]any{"username": "reader", "password": setupPass}, okOrigin).Code)
}

// The Host gate refuses DNS-rebinding shapes on every route in setup mode.
func TestSetupHostGate(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t, func(o *Options) { o.AllowedHosts = []string{"rss.example.com"} })
	for _, hv := range []string{"evil.example:1919", "evil.example.", "EVIL.EXAMPLE:1919", "127.0.0.1.nip.io:1919",
		"localhost.evil.example", "", "evil.example:1919:1", "127.1:1919", "rss.example.com.evil.example"} {
		for _, path := range []string{"/api/instance", "/", "/healthz", "/api/greader.php/accounts/ClientLogin"} {
			rec := h.req("GET", path, "", host(hv))
			require.Equal(t, http.StatusMisdirectedRequest, rec.Code, "%q %s", hv, path)
			require.Contains(t, rec.Body.String(), "KIPPLE_ALLOWED_HOSTS")
			require.NotContains(t, rec.Body.String(), "evil", "the refused name is not echoed")
		}
		rec := h.createAccount(map[string]any{"username": "reader", "password": setupPass}, host(hv))
		require.Equal(t, http.StatusMisdirectedRequest, rec.Code, hv)
	}
	require.Zero(t, h.count("SELECT count(*) FROM account"), "a rebinding page cannot create the account")
	for _, hv := range []string{"127.0.0.1:1919", "[::1]:1919", "localhost:1919", "nas:1919", "nas.local", "box.tail1.ts.net", "rss.example.com", "192.168.1.10:1919"} {
		require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host(hv)).Code, hv)
	}
	// Once the account exists with a password, unlisted names are only logged.
	require.Equal(t, http.StatusCreated, h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass})).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("evil.example")).Code)
}

// Many browsers claim at once on one server and store: exactly one 201 with
// exactly one session; every loser gets 409 already_set_up and no session.
func TestSetupClaimRaceHasExactlyOneWinner(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	const n = 24
	var wg sync.WaitGroup
	recs := make(chan *httptest.ResponseRecorder, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			recs <- h.createAccount(map[string]any{"username": fmt.Sprintf("user%d", i), "password": setupPass}, peer(fmt.Sprintf("192.0.2.%d:1", i+1)))
		}(i)
	}
	close(start)
	wg.Wait()
	close(recs)
	created, lost := 0, 0
	for rec := range recs {
		switch rec.Code {
		case http.StatusCreated:
			created++
			require.NotNil(t, cookieNamed(rec, cookieName))
		case http.StatusConflict:
			lost++
			require.Equal(t, "already_set_up", decode(t, rec)["error"])
			require.Empty(t, rec.Result().Cookies(), "a loser is never signed in")
		case http.StatusNotFound: // arrived after the winner finished
			lost++
			require.Empty(t, rec.Result().Cookies())
		default:
			t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
		}
	}
	require.Equal(t, 1, created)
	require.Equal(t, n-1, lost)
	require.Equal(t, 1, h.count("SELECT count(*) FROM account"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM sessions"), "one session, the winner's")
	require.False(t, h.mgr.Pending())
	require.EqualValues(t, 1, h.started.Load(), "the background work started once")
}

// Account creation is one at a time (a flood costs one hash, not one per
// request): a claim waits while another holds the slot, and one whose client
// went away creates nothing.
func TestSetupClaimsAreOneAtATime(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	h.srv.setupSlot <- struct{}{} // another claim is mid-hash
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- h.createAccount(map[string]any{"username": "reader", "password": setupPass})
	}()
	select {
	case rec := <-done:
		t.Fatalf("the claim did not wait for the slot: %d", rec.Code)
	case <-time.After(100 * time.Millisecond):
	}
	require.Zero(t, h.count("SELECT count(*) FROM account"))
	<-h.srv.setupSlot
	rec := <-done
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// A client that went away while queued gives up without creating anything.
	g := newSetupHarness(t)
	g.srv.setupSlot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec = g.createAccount(map[string]any{"username": "reader", "password": setupPass}, func(r *http.Request) { *r = *r.WithContext(ctx) })
	require.Equal(t, http.StatusOK, rec.Code, "nothing is written for a cancelled request")
	require.Zero(t, g.count("SELECT count(*) FROM account"))
	require.True(t, g.mgr.Pending())
}

func TestSetupOpenMode(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	open := accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true})

	// Choosing open mode must happen from where open mode would work.
	for _, tc := range []struct {
		mod    func(*http.Request)
		reason string
	}{
		{peer("[2001:db8::5]:5000"), "peer"},
		{peer("203.0.113.9:5000"), "peer"},
		{hdr("X-Forwarded-For", "203.0.113.9"), "forwarded"},
		{hdr("CF-Connecting-IP", "203.0.113.9"), "forwarded"},
		{hdr("Cf-Access-Jwt-Assertion", "x"), "forwarded"},
		{hdr("Forwarded", "for=203.0.113.9"), "forwarded"},
	} {
		rec := h.req("POST", "/api/setup/account", open, tc.mod)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		out := decode(t, rec)
		require.Equal(t, "open_refused", out["error"])
		require.Equal(t, tc.reason, out["reason"])
	}
	rec := h.req("POST", "/api/setup/account", open)
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
		rec = h.req("POST", "/api/auth/open", "", peer("203.0.113.9:5000"))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "peer", decode(t, rec)["reason"])
	}
	rec = h.req("POST", "/api/auth/open", "", hdr("X-Forwarded-For", "203.0.113.9"))
	require.Equal(t, "forwarded", decode(t, rec)["reason"])
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", hdr("Sec-Fetch-Site", "cross-site")).Code, "no login CSRF")
	rec = h.req("POST", "/api/auth/open", "", peer("100.100.1.2:5000"), arrivedOn("100.100.100.1:1919"))
	require.Equal(t, http.StatusNoContent, rec.Code, "a Tailscale peer")
	tsSess := cookieNamed(rec, cookieName)
	require.NotNil(t, tsSess)

	// DNS rebinding against open mode: every route refuses the wrong Host.
	for _, path := range []string{"/api/instance", "/api/bootstrap", "/"} {
		require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", path, "", host("evil.example:1919"), withCookies(first)).Code, path)
	}
	require.Equal(t, http.StatusMisdirectedRequest, h.req("POST", "/api/auth/open", "", host("evil.example:1919")).Code)

	// One rule, no setting: the local network is let in as it is.
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000")).Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", peer("203.0.113.9:5000")).Code, "public peers never")
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000"), hdr("X-Forwarded-For", "203.0.113.9")).Code, "a proxy on the LAN is still a proxy")
	lanSess := cookieNamed(h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000")), cookieName)
	require.NotNil(t, lanSess)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/auth/me", "", withCookies(lanSess), peer("192.168.1.20:5000")).Code)
	// In open mode the gate applies to every signed-in request, not only to
	// sign-in: the same session from a public address is refused.
	rec = h.req("GET", "/api/auth/me", "", withCookies(lanSess), peer("203.0.113.9:5000"))
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
	t.Parallel()
	h := newSetupHarness(t)
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}))
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
	for _, mod := range []func(*http.Request){peer("203.0.113.9:5000"), hdr("CF-Connecting-IP", "203.0.113.9")} {
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

// Setup behind Cloudflare Access: the Access-only account needs a verified token
// on the account request itself; the first screen says whether one is present.
func TestSetupWithAccess(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t, withAccess(t))
	k, other := accessKeys(t)
	st := decode(t, h.req("GET", "/api/instance", ""))
	require.Equal(t, map[string]any{"enabled": true, "verified": false}, st["access"])
	st = decode(t, h.req("GET", "/api/instance", "", withJWT(h.jwt(k, nil))))
	require.Equal(t, map[string]any{"enabled": true, "verified": true}, st["access"])

	acc := accountBody(map[string]any{"username": "reader", "passwordless": "access"})
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/setup/account", acc).Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/setup/account", acc, withJWT(h.jwt(other, nil))).Code)
	rec := h.req("POST", "/api/setup/account", acc, withJWT(h.jwt(k, nil)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"username":"reader","auth_mode":"access"}`, rec.Body.String())
	acct, _, _ := h.db.Account(context.Background())
	require.Equal(t, store.AuthStandard, acct.AuthMode)
	require.Empty(t, acct.PasswordHash)
}

// No route, new or old, ever answers with CORS headers, preflight included.
func TestNoCORSHeadersAnywhere(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	routes := [][2]string{
		{"GET", "/api/instance"}, {"POST", "/api/setup/account"},
		{"POST", "/api/auth/open"}, {"POST", "/api/auth/login"}, {"GET", "/api/auth/me"}, {"GET", "/api/bootstrap"},
		{"POST", "/api/onboarding/complete"}, {"POST", "/api/onboarding/restart"}, {"GET", "/api/starter-feeds"},
		{"POST", "/api/starter-feeds"}, {"GET", "/api/settings"}, {"PATCH", "/api/settings"}, {"POST", "/api/account/password"},
		{"GET", "/healthz"}, {"GET", "/"}, {"OPTIONS", "/api/setup/account"}, {"OPTIONS", "/api/auth/open"},
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
			rec := h.req(rt[0], rt[1], `{}`, mod)
			for k := range rec.Header() {
				require.False(t, strings.HasPrefix(strings.ToLower(k), "access-control-"), "%s %s answered %s", rt[0], rt[1], k)
			}
		}
	}
}

func TestSettingsTZDefaultAndOwnedByTheSetting(t *testing.T) {
	t.Parallel()
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
	require.Equal(t, "UTC", v["default"], "new installs default to UTC")
	require.NotContains(t, v, "env_override", "the setting is the only owner of the zone")

	// Whatever TZ says at start, the setting can always be changed and reset.
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", `{"tz":"Asia/Tokyo"}`, withCookie(c)).Code)
	require.Equal(t, "Asia/Tokyo", tzView()["value"])
	require.Equal(t, http.StatusOK, h.do("PATCH", "/api/settings", `{"tz":null}`, withCookie(c)).Code)
	require.Equal(t, "UTC", tzView()["value"])
}

func TestSettingsSecurityKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	rec := h.do("PATCH", "/api/settings", `{"security.allowed_hosts":[" RSS.example.com. ","*.Example.org","rss.example.com"] }`, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	vals := decode(t, rec)["values"].(map[string]any)
	require.Equal(t, []any{"rss.example.com", "*.example.org"}, vals["security.allowed_hosts"])
	require.NotContains(t, vals, "security.open_lan")
	for _, bad := range []string{`{"security.allowed_hosts":"rss.example.com"}`, `{"security.allowed_hosts":["https://x.example"]}`,
		`{"security.allowed_hosts":["*"]}`, `{"security.allowed_hosts":[1]}`, `{"security.open_lan":true}`, `{"security.open_lan":null}`} {
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
	t.Parallel()
	h := newSetupHarness(t)
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}))
	require.Equal(t, http.StatusCreated, rec.Code)
	sess := cookieNamed(rec, cookieName)
	require.Equal(t, http.StatusMisdirectedRequest, h.req("GET", "/api/instance", "", host("reader.example.net")).Code)
	require.Equal(t, http.StatusOK, h.req("PATCH", "/api/settings", `{"security.allowed_hosts":["reader.example.net"]}`, withCookies(sess)).Code)
	require.Equal(t, http.StatusOK, h.req("GET", "/api/instance", "", host("reader.example.net")).Code)
}

func TestStarterFeeds(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
// after the insert committed) still ends setup mode in this process, and the
// loser gets no session.
func TestSetupEndsWhenTheRowExistsWhateverTheAnswer(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	_, err := h.db.CreateAccount(context.Background(), store.Account{Username: "first", PasswordHash: "h", Secret: testSecret, CreatedVia: store.CreatedViaWizard})
	require.NoError(t, err)
	rec := h.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "second", "password": setupPass}))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Equal(t, "already_set_up", decode(t, rec)["error"])
	require.False(t, h.mgr.Pending())
	require.JSONEq(t, `{"setup":false,"auth":"password"}`, h.req("GET", "/api/instance", "").Body.String())
	require.EqualValues(t, 1, h.started.Load(), "an account that appeared elsewhere still starts the background work")
}

// A mode never read enforces (fail closed); a failed re-read after a change
// keeps the last known mode instead of forgetting open mode.
func TestModeSnapshotFailsClosed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.True(t, h.srv.enforceHosts(&modeSnapshot{failed: true}))
	require.False(t, h.srv.enforceHosts(&modeSnapshot{mode: store.AuthStandard}))

	s := newSetupHarness(t)
	require.Equal(t, http.StatusCreated, s.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true})).Code)
	require.Equal(t, store.AuthOpen, s.srv.snapshot(context.Background()).mode)
	require.NoError(t, s.db.Close()) // every read fails from here on
	s.srv.noteMode(context.Background(), nil)
	snap := s.srv.snapshot(context.Background())
	require.Equal(t, store.AuthOpen, snap.mode, "the last known mode, not a forgotten one")
	require.Equal(t, http.StatusMisdirectedRequest, s.req("GET", "/api/instance", "", host("evil.example")).Code)

	// A switch to open mode made here is in the fallback before any re-read.
	p := newSetupHarness(t)
	rec := p.req("POST", "/api/setup/account", accountBody(map[string]any{"username": "reader", "password": setupPass}))
	require.Equal(t, http.StatusCreated, rec.Code)
	sess := cookieNamed(rec, cookieName)
	require.Equal(t, store.AuthStandard, p.srv.snapshot(context.Background()).mode)
	require.Equal(t, http.StatusNoContent, p.req("POST", "/api/account/password", `{"current":"`+setupPass+`","open":true}`, withCookies(sess)).Code)
	require.NoError(t, p.db.Close())
	p.srv.noteMode(context.Background(), nil) // a re-read that fails keeps the switch
	require.Equal(t, store.AuthOpen, p.srv.snapshot(context.Background()).mode)
	require.Equal(t, http.StatusMisdirectedRequest, p.req("GET", "/api/instance", "", host("evil.example")).Code)
}

// In a container even this computer arrives from the bridge gateway, a private
// address: open mode works from there like from any local peer, with no setting.
func TestSetupOpenModeFromAContainerGateway(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	gw := peer("172.17.0.1:40000")
	st := decode(t, h.req("GET", "/api/instance", "", gw))
	require.Equal(t, map[string]any{"reason": nil}, st["open"])
	st = decode(t, h.req("GET", "/api/instance", ""))
	require.Equal(t, map[string]any{"reason": nil}, st["open"], "a bare binary on this computer")
	st = decode(t, h.req("GET", "/api/instance", "", peer("203.0.113.9:1")))
	require.Equal(t, map[string]any{"reason": "peer"}, st["open"], "not from the internet at all")

	open := map[string]any{"username": "reader", "passwordless": "open", "acknowledge_open": true}
	require.Equal(t, http.StatusForbidden, h.createAccount(open, peer("203.0.113.9:1")).Code)
	rec := h.createAccount(open, gw)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", gw).Code)
}

// security.open_lan is gone: a row an old install stored changes nothing (the
// gate does not read it and settings never list it), and it cannot be written.
func TestRemovedOpenLANSettingIsIgnored(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	require.NoError(t, h.db.SetSettings(context.Background(), map[string]any{"security.open_lan": false}))
	sess := h.openAccount(nil)
	vals := decode(t, h.req("GET", "/api/settings", "", withCookies(sess)))["values"].(map[string]any)
	require.NotContains(t, vals, "security.open_lan")
	rec := h.req("PATCH", "/api/settings", `{"security.open_lan":true}`, withCookies(sess))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unknown setting")
	// With the stored row at false (loopback-and-tailnet only on the old version) the LAN is in.
	require.Equal(t, http.StatusNoContent, h.req("POST", "/api/auth/open", "", peer("192.168.1.20:5000")).Code)
	require.Equal(t, http.StatusForbidden, h.req("POST", "/api/auth/open", "", peer("203.0.113.9:5000")).Code)
}

// Password-mode accounts never meet the open gate: a signed-in session works
// from a LAN or public peer, behind a forwarding header, and an anonymous
// request there gets the plain 401 of password mode, never an open_refused.
func TestPasswordModeIgnoresTheOpenGate(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	rec := h.createAccount(map[string]any{"username": "reader", "password": setupPass})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	sess := cookieNamed(rec, cookieName)
	require.NotNil(t, sess)
	for _, p := range []string{"127.0.0.1:5000", "192.168.1.20:5000", "203.0.113.9:5000"} {
		for _, mods := range [][]func(*http.Request){{peer(p)}, {peer(p), hdr("X-Forwarded-For", "198.51.100.1")}, {peer(p), hdr("CF-Connecting-IP", "198.51.100.1")}} {
			rec := h.req("GET", "/api/auth/me", "", append(mods, withCookies(sess))...)
			require.Equal(t, http.StatusOK, rec.Code, "%s: %s", p, rec.Body.String())
			rec = h.req("GET", "/api/auth/me", "", mods...)
			require.Equal(t, http.StatusUnauthorized, rec.Code, p)
			require.Equal(t, "auth", decode(t, rec)["error"], p)
		}
	}
}

// apiRoutes are the routes api.go registers, read from its source: the method
// (empty for any), the path and whether the handler is wrapped in s.authed.
func apiRoutes(t *testing.T) (routes []struct {
	method, path string
	authed       bool
}) {
	t.Helper()
	src, err := os.ReadFile("api.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`(?m)^\s*handle\("(?:([A-Z]+) )?(/[^"]*)", (s\.authed\()?`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, struct {
			method, path string
			authed       bool
		}{m[1], m[2], m[3] != ""})
	}
	return routes
}

// Every route that is not on the short documented list of non-authed ones is
// behind `authed`, and `authed` runs the open gate on every request of an
// open-mode account: with a valid session, a forwarded, proxied or public
// request is refused on all of them. A route added later that skips `authed`
// fails here unless it is added to the list on purpose.
func TestOpenGateCoversEveryRoute(t *testing.T) {
	t.Parallel()
	h := newSetupHarness(t)
	sess := h.openAccount(nil)
	nonAuthed := map[string]bool{"GET /healthz": true, "GET /api/instance": true, "POST /api/auth/open": true, "POST /api/auth/login": true}
	routes := apiRoutes(t)
	require.Greater(t, len(routes), 70, "the route table was read")
	subst := regexp.MustCompile(`\{[^}]*\}`)
	variants := map[string][]func(*http.Request){
		"forwarded":  {hdr("X-Forwarded-For", "203.0.113.9")},
		"cloudflare": {hdr("CF-Connecting-IP", "203.0.113.9")},
		"public":     {peer("203.0.113.9:5000")},
		"lan proxy":  {peer("192.168.1.20:5000"), hdr("Forwarded", "for=203.0.113.9")},
	}
	for _, rt := range routes {
		method := rt.method
		if method == "" {
			method = "GET"
		}
		key := method + " " + rt.path
		if !rt.authed {
			require.True(t, nonAuthed[key], "%s is registered without s.authed: add it to the documented list only if it must be open", key)
			continue
		}
		path := subst.ReplaceAllString(rt.path, "1")
		for name, mods := range variants {
			rec := h.req(method, path, "", append(mods, withCookies(sess))...)
			require.Equal(t, http.StatusForbidden, rec.Code, "%s (%s): %s", key, name, rec.Body.String())
			require.Equal(t, "open_refused", decode(t, rec)["error"], "%s (%s)", key, name)
		}
	}
	for key := range nonAuthed {
		found := false
		for _, rt := range routes {
			m := rt.method
			found = found || m+" "+rt.path == key
		}
		require.True(t, found, "%s is on the list but not registered", key)
	}
}

// An Access-only account cannot switch to open mode: its proof is an Access
// header, which the open gate refuses. It is told to set a password first.
func TestSwitchToOpenNeedsAPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	require.NoError(t, h.db.SetPasswordHash(context.Background(), "", store.AuthStandard, sessionID(c.Value)))
	rec := h.do("POST", "/api/account/password", `{"open":true}`, withCookie(c), func(r *http.Request) {
		r.Header.Set("Origin", "http://example.com")
	})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "password_required", decode(t, rec)["error"])
}
