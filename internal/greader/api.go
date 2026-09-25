package greader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/store"
)

// apiPrefix is the only public mount point (design §6.1).
const apiPrefix = "/api/greader.php"

// Options configures New.
type Options struct {
	DB     *store.DB
	Logger *slog.Logger
	// Wake asks the scheduler for a tick (non-blocking); optional.
	Wake func()
	// Events receives items.state and feed.changed notifications; optional.
	Events *events.Hub
	// Failures and Verifier default to the design settings; tests inject fakes.
	Failures *auth.FailureTracker
	Verifier *auth.Verifier
	// TrustedProxies are the peers allowed to set CF-Connecting-IP.
	TrustedProxies []netip.Addr
	// PublicURL builds iconUrl when greader.icon_urls is on.
	PublicURL string
	// LogForms (KIPPLE_LOG_GREADER_FORMS) adds redacted, truncated form values to the debug log.
	LogForms bool
	// Now and Sleep default to the wall clock (tests).
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)
}

// API is the Reader API handler set.
type API struct {
	db     *store.DB
	log    *slog.Logger
	wake   func()
	opt    Options
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration)
	fails  *auth.FailureTracker
	routes map[string]route

	acct atomic.Pointer[acctSnap]

	verMu  sync.Mutex
	ver    *auth.Verifier
	verFor string

	seenMu sync.Mutex
	seen   map[string]time.Time
}

// route is one endpoint under /reader/api/0/.
type route struct {
	h      func(*call)
	post   bool // writes: POST only, T checked
	raw    bool // body is not a form (subscription/import): no T, raw body kept
	prefix bool // matches name + anything after it
}

// call is one authenticated (or login) request.
type call struct {
	a      *API
	w      *statusWriter
	r      *http.Request
	path   string // cleaned path below the mount, e.g. /reader/api/0/edit-tag
	name   string // path below /reader/api/0/
	p      *Params
	family string
	acct   *acctSnap // account snapshot taken once per request
}

// New builds the API.
func New(opt Options) *API {
	a := &API{
		db: opt.DB, log: opt.Logger, wake: opt.Wake, opt: opt,
		now: opt.Now, sleep: opt.Sleep, fails: opt.Failures,
		routes: map[string]route{}, seen: map[string]time.Time{},
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.sleep == nil {
		a.sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-ctx.Done():
			}
		}
	}
	if a.fails == nil {
		a.fails = auth.NewFailureTracker()
	}
	if a.wake == nil {
		a.wake = func() {}
	}
	a.routes["token"] = route{h: (*call).token}
	a.routes["user-info"] = route{h: (*call).userInfo}
	a.registerRoutes()
	return a
}

// LastSeen returns when each client family last called the API (updated at most
// once a minute per family), for the health view.
func (a *API) LastSeen() map[string]time.Time {
	a.seenMu.Lock()
	defer a.seenMu.Unlock()
	out := make(map[string]time.Time, len(a.seen))
	for k, v := range a.seen {
		out[k] = v
	}
	return out
}

// classify implements §6.1 steps 1-3: collapse '/' runs, strip leading mount
// prefixes and decide whether the Reader handler owns the path.
func classify(p string) (rest string, ok bool) {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '/' && i > 0 && p[i-1] == '/' {
			continue
		}
		b.WriteByte(p[i])
	}
	p = b.String()
	prefixed := false
	for p == apiPrefix || strings.HasPrefix(p, apiPrefix+"/") {
		p = p[len(apiPrefix):]
		prefixed = true
	}
	switch {
	case p == "" || p == "/" || p == "/check/compatibility" || strings.HasPrefix(p, "/icon/"):
		return p, prefixed
	case p == "/accounts/ClientLogin" || strings.HasPrefix(p, "/reader/api/0/"):
		return p, true // root forms exist for the LAN "Reader" account type
	}
	return "", false
}

// hasMountPrefix reports whether the path (after slash collapse) is under the
// Reader mount prefix, so a non-Reader path below it is a 404, never the web mux.
func hasMountPrefix(p string) bool {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p == apiPrefix || strings.HasPrefix(p, apiPrefix+"/")
}

// Front returns a handler that claims every Reader API path and passes the rest
// to next. No ServeMux ever sees a Reader path.
func (a *API) Front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := classify(r.URL.Path)
		if !ok {
			if hasMountPrefix(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		a.serve(w, r, rest)
	})
}

func (a *API) serve(w http.ResponseWriter, r *http.Request, rest string) {
	start := time.Now()
	sw := &statusWriter{ResponseWriter: w}
	sw.Header().Set("Cache-Control", "private, no-cache")
	c := &call{a: a, w: sw, r: r, path: rest, family: family(r.UserAgent())}
	defer func() { a.logRequest(c, time.Since(start)) }()

	switch {
	case rest == "" || rest == "/":
		c.text(http.StatusOK, "OK")
		return
	case rest == "/check/compatibility":
		c.text(http.StatusOK, "PASS")
		return
	case strings.HasPrefix(rest, "/icon/"):
		c.icon(strings.TrimPrefix(rest, "/icon/"))
		return
	case rest == "/accounts/ClientLogin":
		c.p = readParamsLimit(r, false, maxLoginBody)
		if c.p.tooMany {
			c.text(http.StatusBadRequest, "Bad Request")
			return
		}
		c.clientLogin()
		return
	}

	c.name = strings.TrimPrefix(rest, "/reader/api/0/")
	rt, found := a.lookup(c.name)
	acct, err := a.account(r.Context())
	if err != nil {
		a.log.Error("greader: load account", "err", err)
		c.text(http.StatusInternalServerError, "Internal Server Error")
		return
	}
	c.acct = acct
	// Authenticate by header before parsing any body; only a POST that may carry
	// T in its body is read without it, and then only up to a small cap.
	hdrOK := c.headerOK(acct)
	if !acct.enabled || (!hdrOK && (r.Method != http.MethodPost || rt.raw)) {
		c.p = &Params{}
		c.unauthorized()
		return
	}
	limit := int64(maxBody)
	if !hdrOK {
		limit = maxLoginBody
	}
	c.p = readParamsLimit(r, rt.raw, limit)
	if c.p.tooMany {
		c.text(http.StatusBadRequest, "Bad Request")
		return
	}
	if !c.authenticate(acct, rt) {
		c.unauthorized()
		return
	}
	a.touch(c.family)
	if !found {
		a.log.Warn("greader: unknown endpoint", "path", rest, "ua", r.UserAgent())
		c.json(http.StatusOK, []any{})
		return
	}
	if rt.post && r.Method != http.MethodPost {
		c.w.Header().Set("Allow", "POST")
		c.text(http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	rt.h(c)
}

func (a *API) lookup(name string) (route, bool) {
	if rt, ok := a.routes[name]; ok && !rt.prefix {
		return rt, true
	}
	for n, rt := range a.routes {
		if rt.prefix && strings.HasPrefix(name, n) {
			return rt, true
		}
	}
	return route{}, false
}

// touch records the family's last-seen time, at most once a minute.
func (a *API) touch(family string) {
	now := a.now()
	a.seenMu.Lock()
	if now.Sub(a.seen[family]) >= time.Minute {
		a.seen[family] = now
	}
	a.seenMu.Unlock()
}

// family classifies the client by User-Agent prefix (design §6.3).
func family(ua string) string {
	switch {
	case strings.HasPrefix(ua, "Reeder"):
		return "reeder"
	case strings.HasPrefix(ua, "NetNewsWire"):
		return "netnewswire"
	case strings.HasPrefix(ua, "Unread"):
		return "unread"
	}
	return "api"
}

// ---- account, token, auth ----

const acctTTL = 5 * time.Second

type acctSnap struct {
	enabled  bool // account exists and has an API password
	username string
	hash     string
	secret   string
	token    string
	loaded   time.Time
}

// account returns the cached account snapshot (refreshed every few seconds, so
// a password changed by `kipple api-password` revokes tokens promptly).
func (a *API) account(ctx context.Context) (*acctSnap, error) {
	if s := a.acct.Load(); s != nil && a.now().Sub(s.loaded) < acctTTL {
		return s, nil
	}
	acc, ok, err := a.db.Account(ctx)
	if err != nil {
		return nil, err
	}
	s := &acctSnap{loaded: a.now()}
	if ok && acc.APIPasswordHash != "" {
		s.enabled, s.username, s.hash, s.secret = true, acc.Username, acc.APIPasswordHash, acc.Secret
		s.token = makeToken(acc.Username, acc.Secret, acc.APIPasswordHash)
	}
	a.acct.Store(s)
	return s, nil
}

// makeToken is username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + hash)):
// 64 hex after the slash, never containing '='.
func makeToken(username, secret, apiHash string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("greader-token-v1|" + apiHash))
	return username + "/" + hex.EncodeToString(h.Sum(nil))
}

func (a *API) verifier(s *acctSnap) *auth.Verifier {
	if a.opt.Verifier != nil {
		return a.opt.Verifier
	}
	a.verMu.Lock()
	defer a.verMu.Unlock()
	if a.ver == nil || a.verFor != s.secret {
		a.ver, a.verFor = auth.NewVerifier([]byte(s.secret), auth.VerifierOptions{}), s.secret
	}
	return a.ver
}

func tokEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// authenticate applies §6.3: the Authorization header, or (POST only) a valid
// T. When the header authenticated, T must be the token, "" or "x".
func (c *call) authenticate(s *acctSnap, rt route) bool {
	if !s.enabled {
		return false
	}
	hdrOK := c.headerOK(s)
	if c.r.Method != http.MethodPost || rt.raw {
		return hdrOK
	}
	t := c.p.Get("T")
	if hdrOK {
		return t == "" || t == "x" || tokEqual(t, s.token)
	}
	return t != "" && tokEqual(t, s.token)
}

// headerOK reports whether the Authorization header carries the account token.
func (c *call) headerOK(s *acctSnap) bool {
	if !s.enabled {
		return false
	}
	if f := strings.Fields(c.r.Header.Get("Authorization")); len(f) == 2 && strings.HasPrefix(f[1], "auth=") {
		return tokEqual(strings.TrimPrefix(f[1], "auth="), s.token)
	}
	return false
}

func (c *call) unauthorized() {
	h := c.w.Header()
	h.Set("X-Reader-Google-Bad-Token", "true")
	h.Set("Google-Bad-Token", "true")
	c.text(http.StatusUnauthorized, "Unauthorized!")
}

// clientLogin is POST (or GET) accounts/ClientLogin (design §6.3).
func (c *call) clientLogin() {
	a := c.a
	ctx := c.r.Context()
	ip := auth.ClientIP(c.r, a.opt.TrustedProxies)
	fail := func() {
		if d := a.fails.Fail(ip); d > 0 {
			a.sleep(ctx, d)
		}
		c.w.Header().Set("Google-Bad-Token", "true")
		c.w.Header().Set("X-Reader-Google-Bad-Token", "true")
		c.text(http.StatusUnauthorized, "Error=BadAuthentication\n")
	}
	s, err := a.account(ctx)
	if err != nil {
		a.log.Error("greader: load account", "err", err)
		c.text(http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !s.enabled {
		fail()
		return
	}
	email, pass := c.p.Get("Email"), c.p.Get("Passwd")
	// The password is always verified, even when the email is wrong.
	ok, busy := a.verifier(s).VerifyBusy(ctx, "api", pass, s.hash)
	if busy {
		// Hashing slot unavailable: says nothing about the password, so no failure is recorded.
		c.w.Header().Set("Retry-After", "5")
		c.text(http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	if !ok || !strings.EqualFold(email, s.username) {
		fail()
		return
	}
	a.fails.Clear(ip)
	if c.p.Get("output") == "json" {
		c.json(http.StatusOK, map[string]any{"SID": s.token, "LSID": nil, "Auth": s.token})
		return
	}
	c.text(http.StatusOK, "SID="+s.token+"\nLSID=null\nAuth="+s.token+"\n")
}

// token is GET token: the same token as plain text.
func (c *call) token() {
	s := c.acct
	c.text(http.StatusOK, s.token+"\n")
}

// userInfo is GET user-info (Reeder calls it right after ClientLogin).
func (c *call) userInfo() {
	s := c.acct
	c.json(http.StatusOK, map[string]string{
		"userId": "1", "userName": s.username, "userProfileId": "1", "userEmail": s.username,
	})
}

// ---- responses ----

func (c *call) text(status int, body string) {
	h := c.w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	c.w.WriteHeader(status)
	_, _ = c.w.Write([]byte(body))
}

// marshal encodes v without HTML escaping (titles are plain JSON strings).
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("{}")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func (c *call) json(status int, v any) {
	c.w.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.w.WriteHeader(status)
	_, _ = c.w.Write(marshal(v))
}

// jsonETag writes a list response with ETag = "sha256(body)[:16]" and answers a
// matching If-None-Match with 304 (design §6.9).
func (c *call) jsonETag(v any) {
	body := marshal(v)
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	h := c.w.Header()
	h.Set("ETag", etag)
	for _, cand := range strings.Split(c.r.Header.Get("If-None-Match"), ",") {
		cand = strings.TrimPrefix(strings.TrimSpace(cand), "W/")
		if cand == etag {
			c.w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	c.w.WriteHeader(http.StatusOK)
	_, _ = c.w.Write(body)
}

// ok is the text/plain OK every write endpoint answers.
func (c *call) ok() { c.text(http.StatusOK, "OK") }

// serverError answers a genuine database failure (the only 5xx).
func (c *call) serverError(what string, err error) {
	c.a.log.Error("greader: "+what, "err", err, "path", c.path)
	c.text(http.StatusInternalServerError, "Internal Server Error")
}

// publish sends an SSE event when a hub is attached.
func (a *API) publish(typ string, data any) {
	if a.opt.Events != nil {
		a.opt.Events.Publish(typ, data)
	}
}
