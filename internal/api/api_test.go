package api

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/greader"
	"github.com/WPTK/kipple/internal/httpx"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

const (
	testUser   = "owner"
	testPass   = "correct-horse"
	testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

type fakeSched struct {
	mu       sync.Mutex
	refreshN int
	imported [][]int64

	submits      []sched.Priority
	reply        sched.Reply // what Submit answers at once, unless hang
	hang         bool        // Submit's reply channel never fires
	submitErr    error
	retentionAll []bool
	wakes        int
	down         chan struct{}
	holds        map[string]time.Time
}

func (f *fakeSched) Submit(p sched.Priority) (<-chan sched.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, p)
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	ch := make(chan sched.Reply, 1)
	if !f.hang {
		rep := f.reply
		rep.FeedID = p.FeedID
		ch <- rep
	}
	return ch, nil
}

func (f *fakeSched) Wake() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakes++
}

func (f *fakeSched) Shutdown() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down == nil {
		f.down = make(chan struct{})
	}
	return f.down
}

func (f *fakeSched) submitted() []sched.Priority {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sched.Priority(nil), f.submits...)
}

func (f *fakeSched) RefreshAll() (sched.RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshN++
	return sched.RunInfo{RunID: 42, Kind: "manual", Total: 3}, nil
}

func (f *fakeSched) StartImport(ids []int64) (sched.RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imported = append(f.imported, ids)
	return sched.RunInfo{RunID: 43, Kind: "import", Total: len(ids)}, nil
}

func (f *fakeSched) Status() ([]sched.RunStatus, int) {
	return []sched.RunStatus{{ID: 42, Kind: "manual", Done: 1, Total: 3}}, 2
}

func (f *fakeSched) HostHolds() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holds
}

type harness struct {
	t     *testing.T
	db    *store.DB
	srv   *Server
	hub   *events.Hub
	sched *fakeSched
	clk   *clock.Fake
	mux   *http.ServeMux
}

func newHarness(t *testing.T, tune ...func(*Options)) *harness {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.CreateAccount(context.Background(), store.Account{Username: testUser, PasswordHash: "web-hash", Secret: testSecret})
	require.NoError(t, err)

	h := &harness{t: t, db: db, hub: events.NewWithClock(clk), sched: &fakeSched{}, clk: clk, mux: http.NewServeMux()}
	opt := Options{
		DB: db, Sched: h.sched, Hub: h.hub, Now: clk.Now, Heartbeat: 20 * time.Millisecond,
		Verifier: auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Check: func(pw, phc string) bool {
			return pw == testPass && phc == "web-hash"
		}}),
	}
	for _, f := range tune {
		f(&opt)
	}
	h.srv = New(opt)
	t.Cleanup(h.srv.Close)
	h.srv.Register(h.mux)
	return h
}

// do sends a same-origin request through the mux.
func (h *harness) do(method, path, body string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Kipple-Client", "web")
	r.RemoteAddr = "10.20.30.10:5555"
	for _, m := range mod {
		m(r)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, r)
	return rec
}

func loginBody(pw string) string {
	b, _ := json.Marshal(map[string]string{"username": testUser, "password": pw})
	return string(b)
}

func (h *harness) login() *http.Cookie {
	h.t.Helper()
	rec := h.do("POST", "/api/auth/login", loginBody(testPass))
	require.Equal(h.t, http.StatusNoContent, rec.Code)
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	h.t.Fatal("no session cookie")
	return nil
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value}) }
}

func TestCookieAttributesHTTP(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	require.True(t, c.HttpOnly)
	require.False(t, c.Secure, "plain http must not set Secure")
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.Equal(t, "/", c.Path)
	require.Equal(t, 90*24*3600, c.MaxAge)
	require.Equal(t, "kipple_session", c.Name)
}

func TestCookieAttributesHTTPSProxied(t *testing.T) {
	trusted := netip.MustParseAddr("192.0.2.20")
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Addr{trusted} })

	// trusted proxy saying https: Secure
	rec := h.do("POST", "/api/auth/login", loginBody(testPass), func(r *http.Request) {
		r.RemoteAddr = "192.0.2.20:4000"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
	c := rec.Result().Cookies()[0]
	require.True(t, c.Secure)
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)

	// an untrusted peer cannot claim https
	rec = h.do("POST", "/api/auth/login", loginBody(testPass), func(r *http.Request) {
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.False(t, rec.Result().Cookies()[0].Secure)

	// real TLS: Secure
	rec = h.do("POST", "/api/auth/login", loginBody(testPass), func(r *http.Request) {
		r.TLS = &tls.ConnectionState{}
	})
	require.True(t, rec.Result().Cookies()[0].Secure)

	// logout clears with the same attributes
	rec = h.do("POST", "/api/auth/logout", "", withCookie(c), func(r *http.Request) {
		r.RemoteAddr = "192.0.2.20:4000"
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
	cleared := rec.Result().Cookies()[0]
	require.Equal(t, -1, cleared.MaxAge)
	require.True(t, cleared.Secure)
	// the session is gone
	rec = h.do("GET", "/api/status", "", withCookie(c))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestLoginLockout(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		rec := h.do("POST", "/api/auth/login", loginBody("wrong"))
		require.Equal(t, http.StatusUnauthorized, rec.Code, "attempt %d", i)
	}
	// locked: even the right password is refused
	rec := h.do("POST", "/api/auth/login", loginBody(testPass))
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	// another IP is unaffected
	rec = h.do("POST", "/api/auth/login", loginBody(testPass), func(r *http.Request) { r.RemoteAddr = "10.20.30.11:1" })
	require.Equal(t, http.StatusNoContent, rec.Code)

	// still locked just before the window ends, open just after
	h.clk.Advance(15*time.Minute - time.Second)
	require.Equal(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	h.clk.Advance(2 * time.Second)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
}

func TestLoginFailuresBelowLimitAndSuccessClears(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 9; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	// success cleared the count: nine more failures still are not a lockout
	for i := 0; i < 9; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
}

func TestLockoutUsesTrustedProxyClientIP(t *testing.T) {
	trusted := netip.MustParseAddr("192.0.2.20")
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Addr{trusted} })
	viaProxy := func(ip string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "192.0.2.20:4000"
			r.Header.Set("CF-Connecting-IP", ip)
		}
	}
	for i := 0; i < 10; i++ {
		h.do("POST", "/api/auth/login", loginBody("wrong"), viaProxy("203.0.113.9"))
	}
	require.Equal(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", loginBody(testPass), viaProxy("203.0.113.9")).Code)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), viaProxy("203.0.113.10")).Code)
}

func TestUnauthenticatedIs401(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/status"}, {"GET", "/api/health/feeds"}, {"POST", "/api/refresh"},
		{"GET", "/api/events"}, {"GET", "/api/opml"}, {"POST", "/api/opml"},
		{"GET", "/api/auth/me"}, {"POST", "/api/auth/logout"}, {"GET", "/api/unknown"},
	} {
		rec := h.do(tc.method, tc.path, "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", tc.method, tc.path)
		require.JSONEq(t, `{"error":"auth"}`, rec.Body.String())
		require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	}
	// a forged / unknown cookie is no better
	rec := h.do("GET", "/api/status", "", withCookie(&http.Cookie{Name: cookieName, Value: "forged"}))
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	// healthz and login are open
	require.Equal(t, http.StatusOK, h.do("GET", "/healthz", "").Code)
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("nope")).Code) // reachable, just wrong
}

func TestCrossOriginRejected(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	strip := func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Del("X-Kipple-Client")
	}
	for _, tc := range []struct {
		name string
		mod  []func(*http.Request)
		// clientOnly marks a failure of the X-Kipple-Client rule alone: GET
		// downloads are reachable by a plain link, so only the origin rule guards them.
		clientOnly bool
	}{
		{"cross-site fetch metadata", []func(*http.Request){func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }}, false},
		{"same-site fetch metadata", []func(*http.Request){func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }}, false},
		{"no metadata no origin", []func(*http.Request){strip, func(r *http.Request) { r.Header.Set("X-Kipple-Client", "web") }}, false},
		{"foreign origin", []func(*http.Request){strip, func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set("X-Kipple-Client", "web")
		}}, false},
		{"missing client header", []func(*http.Request){func(r *http.Request) { r.Header.Del("X-Kipple-Client") }}, true},
		{"bad client header", []func(*http.Request){func(r *http.Request) { r.Header.Set("X-Kipple-Client", "curl") }}, true},
	} {
		mods := append([]func(*http.Request){withCookie(c)}, tc.mod...)
		for _, path := range []string{"/api/refresh", "/api/auth/logout"} {
			rec := h.do("POST", path, "", mods...)
			require.Equal(t, http.StatusForbidden, rec.Code, "%s %s", tc.name, path)
			require.JSONEq(t, `{"error":"origin"}`, rec.Body.String())
		}
		rec := h.do("POST", "/api/opml", "<opml/>", mods...)
		require.Equal(t, http.StatusForbidden, rec.Code, tc.name)
		wantGet := http.StatusForbidden
		if tc.clientOnly {
			wantGet = http.StatusOK
		}
		require.Equal(t, wantGet, h.do("GET", "/api/opml", "", mods...).Code, "%s GET opml", tc.name)
	}
	require.Zero(t, h.sched.refreshN)

	// login is guarded too (login CSRF)
	rec := h.do("POST", "/api/auth/login", loginBody(testPass), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Origin matching the request host is accepted when Sec-Fetch-Site is absent
	rec = h.do("POST", "/api/refresh", "", withCookie(c), strip, func(r *http.Request) {
		r.Header.Set("Origin", "http://"+r.Host)
		r.Header.Set("X-Kipple-Client", "pwa")
	})
	require.Equal(t, http.StatusAccepted, rec.Code)

	// a plain GET needs no origin proof
	rec = h.do("GET", "/api/status", "", withCookie(c), strip)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestOriginUsesEffectiveScheme(t *testing.T) {
	trusted := netip.MustParseAddr("192.0.2.20")
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Addr{trusted} })
	c := h.login()
	via := func(origin string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Del("Sec-Fetch-Site")
			r.RemoteAddr = "192.0.2.20:1"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("Origin", origin)
		}
	}
	require.Equal(t, http.StatusAccepted, h.do("POST", "/api/refresh", "", withCookie(c), via("https://example.com")).Code)
	require.Equal(t, http.StatusForbidden, h.do("POST", "/api/refresh", "", withCookie(c), via("http://example.com")).Code)
}

func TestStatusMeHealthRefresh(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	rec := h.do("GET", "/api/status", "", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var st struct {
		Runs []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Total int    `json:"total"`
		} `json:"runs"`
		Inflight    int   `json:"inflight"`
		UnreadTotal int64 `json:"unread_total"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	require.Equal(t, "42", st.Runs[0].ID)
	require.Equal(t, 2, st.Inflight)

	rec = h.do("GET", "/api/auth/me", "", withCookie(c))
	require.JSONEq(t, `{"username":"owner","api_enabled":false}`, rec.Body.String())

	rec = h.do("POST", "/api/refresh", "", withCookie(c))
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.JSONEq(t, `{"run_id":"42","total":3,"joined":false}`, rec.Body.String())

	ctx := context.Background()
	id, err := h.db.AddFeed(ctx, store.NewFeed{URL: "https://a.example/feed.xml"})
	require.NoError(t, err)
	_, err = h.db.Reader().ExecContext(ctx, "SELECT 1")
	require.NoError(t, err)
	require.NoError(t, h.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET title = 'Alpha', consecutive_failures = 3, last_error = 'boom', last_error_class = 'http',
			last_error_at = 100, last_status = 500, redirect_to = 'https://b.example/f', redirect_kind = 'permanent',
			trimmed_unread_count = 4 WHERE id = ?`, id)
		return err
	}))
	rec = h.do("GET", "/api/health/feeds", "", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var hf struct {
		Feeds []struct {
			ID                  string   `json:"id"`
			Title               string   `json:"title"`
			Status              string   `json:"status"`
			RedirectPending     bool     `json:"redirect_pending"`
			HostThrottledUntil  *int64   `json:"host_throttled_until"`
			Notices             []string `json:"notices"`
			LastError           string   `json:"last_error"`
			ConsecutiveFailures int      `json:"consecutive_failures"`
			LastSuccessAt       *int64   `json:"last_success_at"`
			RedirectKind        string   `json:"redirect_kind"`
		} `json:"feeds"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &hf))
	require.Len(t, hf.Feeds, 1)
	f := hf.Feeds[0]
	require.Equal(t, "Alpha", f.Title)
	require.Equal(t, "erroring", f.Status, "3 failures is erroring; failing starts at 14")
	require.True(t, f.RedirectPending)
	require.Nil(t, f.HostThrottledUntil)
	require.Equal(t, "boom", f.LastError)
	require.Equal(t, 3, f.ConsecutiveFailures)
	require.Nil(t, f.LastSuccessAt)
	require.Len(t, f.Notices, 2)
}

func TestOPMLImportExport(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	doc := `<?xml version="1.0"?><opml version="2.0"><head/><body>
	<outline text="News"><outline type="rss" text="Alpha" xmlUrl="https://a.example/feed.xml"/></outline>
	</body></opml>`
	rec := h.do("POST", "/api/opml?mark_read_older_than_days=7", doc, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var res struct {
		FeedsAdded int `json:"feeds_added"`
		RunID      any `json:"run_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, 1, res.FeedsAdded)
	require.Equal(t, "43", res.RunID)
	require.Len(t, h.sched.imported, 1)

	// second import: existing feed, no new run
	rec = h.do("POST", "/api/opml", doc, withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, h.sched.imported, 1)
	require.Contains(t, rec.Body.String(), `"run_id":null`)

	require.Equal(t, http.StatusBadRequest, h.do("POST", "/api/opml?mark_read_older_than_days=999", doc, withCookie(c)).Code)
	require.Equal(t, http.StatusBadRequest, h.do("POST", "/api/opml", "not xml at all", withCookie(c)).Code)

	rec = h.do("GET", "/api/opml", "", withCookie(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
	require.Contains(t, rec.Body.String(), `xmlUrl="https://a.example/feed.xml"`)
}

// TestGreaderPrefixNeverReachesUI composes the Reader front with the UI mux the
// way cmd/kipple does.
func TestGreaderPrefixNeverReachesUI(t *testing.T) {
	h := newHarness(t)
	front := greader.New(greader.Options{DB: h.db}).Front(h.mux)
	for _, p := range []string{"/api/greader.php", "/api/greader.php/status", "/api/greader.php//api/status", "/api/greader.php/api/auth/me"} {
		r := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		front.ServeHTTP(rec, r)
		require.NotContains(t, rec.Body.String(), `"error":"auth"`, p) // never the UI's 401
	}
	// even if a request reached the UI mux, the prefix is a plain 404
	r := httptest.NewRequest("GET", "/api/greader.php/x", nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, r)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "auth")
}

// sseServer runs the mux on a real server with tiny timeouts so a stream that
// outlives WriteTimeout and ReadTimeout proves the deadline handling.
func sseServer(t *testing.T, h *harness) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(h.mux)
	ts.Config.WriteTimeout = 150 * time.Millisecond
	ts.Config.ReadTimeout = 150 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func openStream(t *testing.T, ts *httptest.Server, c *http.Cookie, lastID string) (*bufio.Reader, func()) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/api/events", nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "no-cache, no-transform", resp.Header.Get("Cache-Control"))
	return bufio.NewReader(resp.Body), func() { _ = resp.Body.Close() }
}

// readUntil reads lines until one has the prefix, failing after timeout.
func readUntil(t *testing.T, br *bufio.Reader, prefix string, timeout time.Duration) string {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		for {
			l, err := br.ReadString('\n')
			if err != nil || strings.HasPrefix(l, prefix) {
				ch <- res{l, err}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		require.NoError(t, r.err)
		return r.line
	case <-time.After(timeout):
		t.Fatalf("no line with prefix %q within %v", prefix, timeout)
		return ""
	}
}

func TestSSEHeartbeatAndSurvivesWriteAndReadTimeout(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)
	br, closeBody := openStream(t, ts, c, "")
	defer closeBody()

	readUntil(t, br, ": connected", 2*time.Second)
	// heartbeats keep coming well past WriteTimeout/ReadTimeout (150 ms)
	start := time.Now()
	for i := 0; i < 3; i++ {
		readUntil(t, br, ": ping", 2*time.Second)
	}
	for time.Since(start) < 400*time.Millisecond {
		readUntil(t, br, ": ping", 2*time.Second)
	}
	// and a real event published after both timeouts have elapsed is delivered
	h.hub.Publish("run.done", map[string]any{"run_id": 1})
	require.Equal(t, "event: run.done\n", readUntil(t, br, "event: run.done", 2*time.Second)) // heartbeat events interleave
	require.Contains(t, readUntil(t, br, "data:", time.Second), `"run_id":1`)
}

func TestSSEReplayAndResync(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)

	h.hub.Publish("one", 1)
	first := h.hub.LastID()
	h.hub.Publish("two", 2)

	br, closeBody := openStream(t, ts, c, itoa(first))
	require.Equal(t, "event: two\n", readUntil(t, br, "event: two", 2*time.Second))
	closeBody()

	br, closeBody = openStream(t, ts, c, "1") // ring cannot cover id 1
	defer closeBody()
	require.Equal(t, "event: resync\n", readUntil(t, br, "event: resync", 2*time.Second))
}

func TestSSEEndsOnHubClose(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)
	br, closeBody := openStream(t, ts, c, "")
	defer closeBody()
	readUntil(t, br, ": connected", time.Second)
	h.hub.Close()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, br); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end after hub close")
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestLoginBurstCannotExceedLockoutLimit(t *testing.T) {
	var checks atomic.Int32
	h := newHarness(t, func(o *Options) {
		o.Verifier = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 30 * time.Second, Check: func(pw, phc string) bool {
			checks.Add(1)
			time.Sleep(5 * time.Millisecond) // slow enough that the burst overlaps
			return pw == testPass
		}})
	})
	var wg sync.WaitGroup
	var wrong, locked atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch h.do("POST", "/api/auth/login", loginBody("wrong")).Code {
			case http.StatusUnauthorized:
				wrong.Add(1)
			case http.StatusTooManyRequests:
				locked.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 10, checks.Load(), "only Max passwords were ever verified")
	require.EqualValues(t, 10, wrong.Load())
	require.EqualValues(t, 30, locked.Load())
	// the right password is refused while locked, and never reaches the verifier
	require.Equal(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	require.EqualValues(t, 10, checks.Load())
}

func TestBusyAndMalformedLoginsAreNotCountedAgainstLockout(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 15; i++ {
		require.Equal(t, http.StatusBadRequest, h.do("POST", "/api/auth/login", "{not json").Code)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
}

// The API routes under httpx.Secure: every answer, errors included, forbids
// framing and carries a JSON-only policy; the image proxy keeps its own.
func TestAPIResponsesUnderSecure(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	secure := httpx.Secure(h.mux, httpx.Options{})
	get := func(method, path, body string, withC bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-Kipple-Client", "web")
		if withC {
			r.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		secure.ServeHTTP(rec, r)
		return rec
	}
	check := func(rec *httptest.ResponseRecorder, what string) {
		t.Helper()
		require.Equal(t, "default-src 'none'; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"), what)
		require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"), what)
		require.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"), what)
		require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), what)
	}
	check(get("GET", "/healthz", "", false), "healthz")
	check(get("POST", "/api/auth/login", loginBody("wrong"), false), "failed login")
	check(get("GET", "/api/status", "", false), "unauthenticated 401")
	check(get("GET", "/api/status", "", true), "status")
	check(get("GET", "/api/opml", "", true), "opml export")
	check(get("GET", "/api/nope", "", true), "404")
	require.Equal(t, "same-origin", get("GET", "/api/status", "", true).Header().Get("Cross-Origin-Resource-Policy"))
	require.Empty(t, get("GET", "/healthz", "", false).Header().Get("Cross-Origin-Resource-Policy"))
}

// stop closes the Shutdown channel, as sched.Scheduler.Stop does.
func (f *fakeSched) stop() {
	f.Shutdown()
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.down)
}

func (f *fakeSched) ApplyRetention(all bool) (sched.RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retentionAll = append(f.retentionAll, all)
	return sched.RunInfo{RunID: 44, Kind: "retention", Total: 7}, nil
}

// GET downloads answer to the Sec-Fetch-Site/Origin rule alone (a link click
// carries no X-Kipple-Client); every other GET and every write still needs it.
func TestDownloadOriginMatrix(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	plain := func(site string) func(*http.Request) {
		return func(r *http.Request) {
			r.Header.Del("X-Kipple-Client")
			r.Header.Del("Sec-Fetch-Site")
			r.Header.Del("Origin")
			if site != "" {
				r.Header.Set("Sec-Fetch-Site", site)
			}
		}
	}
	require.Equal(t, http.StatusOK, h.do("GET", "/api/opml", "", withCookie(c), plain("same-origin")).Code, "link click")
	require.Equal(t, http.StatusForbidden, h.do("GET", "/api/opml", "", withCookie(c), plain("cross-site")).Code)
	require.Equal(t, http.StatusForbidden, h.do("GET", "/api/opml", "", withCookie(c), plain("same-site")).Code)
	require.Equal(t, http.StatusForbidden, h.do("GET", "/api/opml", "", withCookie(c), plain("")).Code, "no metadata and no Origin")
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/opml", "", plain("same-origin")).Code, "auth still first")
	// Not a download: a POST to the same path keeps the header rule.
	require.Equal(t, http.StatusForbidden, h.do("POST", "/api/opml", "<opml/>", withCookie(c), plain("same-origin")).Code)
}

func TestSSEReplayFromLastEventIDQueryParam(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)
	h.hub.Publish("one", 1)
	first := h.hub.LastID()
	h.hub.Publish("two", 2)

	req, _ := http.NewRequest("GET", ts.URL+"/api/events?last_event_id="+itoa(first), nil)
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	require.Equal(t, "event: two\n", readUntil(t, br, "event: two", 2*time.Second))
}

func TestSSEEndsWhenItsSessionIsGone(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)
	br, closeBody := openStream(t, ts, c, "")
	defer closeBody()
	readUntil(t, br, ": connected", time.Second)
	h.exec("DELETE FROM sessions")
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, br); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its session")
	}
}
