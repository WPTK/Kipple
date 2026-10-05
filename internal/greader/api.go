package greader

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/stats"
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
	// FetchNow fetches a just-subscribed feed at once and waits up to wait for it; optional. It is
	// used only while greader.subscribe_fetch_now is on and never for a feed that already existed.
	FetchNow func(ctx context.Context, feedID int64, wait time.Duration)
	// Stats records star/unstar rows from edit-tag (design §8); optional.
	Stats stats.Recorder
	// Events receives items.state and feed.changed notifications; optional.
	Events *events.Hub
	// Failures defaults to the design settings. Verifier must be the one
	// instance shared with api.Options.Verifier (nil builds a private one, tests
	// only). Tests inject fakes.
	Failures *auth.FailureTracker
	Verifier *auth.Verifier
	// TrustedProxies are the peers allowed to set CF-Connecting-IP.
	TrustedProxies []netip.Prefix
	// PublicURL builds iconUrl when greader.icon_urls is on.
	PublicURL string
	// LogForms (KIPPLE_LOG_GREADER_FORMS) adds redacted, truncated form values to the debug log.
	LogForms bool
	// FulltextHold is how long a new item of a full-text feed is held back from
	// the Reader API while its extraction is pending (design §6.5). Zero means
	// DefaultFulltextHold, negative disables the hold; it is capped at
	// MaxFulltextHold so it stays inside the ot slack.
	FulltextHold time.Duration
	// Now defaults to the wall clock (tests).
	Now func() time.Time
}

const (
	// DefaultFulltextHold is the default hold window, measured from the item's crawl time.
	DefaultFulltextHold = 30 * time.Second
	// MaxFulltextHold caps the hold at half the 120 s ot slack (design §3), so an
	// ot taken while an item was held still reaches it on the next sync.
	MaxFulltextHold = 60 * time.Second
)

// holdCut is the id (crawl time in microseconds) above which a pending full-text
// item is held; 0 disables the hold. Read once per request.
func (a *API) holdCut() int64 {
	w := a.opt.FulltextHold
	switch {
	case w < 0:
		return 0
	case w == 0:
		w = DefaultFulltextHold
	case w > MaxFulltextHold:
		w = MaxFulltextHold
	}
	return a.now().Add(-w).UnixMicro()
}

// API is the Reader API handler set.
type API struct {
	db     *store.DB
	log    *slog.Logger
	wake   func()
	opt    Options
	now    func() time.Time
	fails  *auth.FailureTracker
	ver    *auth.Verifier
	routes map[string]route

	acct atomic.Pointer[acctSnap]
	// acctMu makes "generation unchanged, so store" atomic against
	// InvalidateAccount, which bumps acctGen. A snapshot read before an
	// invalidation is therefore never cached after it.
	acctMu  sync.Mutex
	acctGen uint64
	// afterAcctRead is a test hook run between the DB read and the store.
	afterAcctRead func()

	seenMu sync.Mutex
	seen   map[string]time.Time
}

// route is one endpoint under /reader/api/0/.
type route struct {
	h      func(*call)
	post   bool // writes: POST only, T checked
	raw    bool // body is not a form (subscription/import): no T, raw body kept
	repair bool // glue the unencoded tail of a label name in the POST body (design §6.2)
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
	// fetchSpent is the time this request has already waited for subscribe_fetch_now fetches.
	fetchSpent time.Duration
}

// New builds the API.
func New(opt Options) *API {
	a := &API{
		db: opt.DB, log: opt.Logger, wake: opt.Wake, opt: opt,
		now: opt.Now, fails: opt.Failures, ver: opt.Verifier,
		routes: map[string]route{}, seen: map[string]time.Time{},
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.fails == nil {
		a.fails = auth.NewFailureTracker()
		a.fails.Now = a.now
	}
	if a.ver == nil {
		a.ver = auth.NewVerifier(nil, auth.VerifierOptions{})
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
		c.p = readParamsLimit(r, false, maxLoginBody, false)
		if c.rejectParams() {
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
	c.p = readParamsLimit(r, rt.raw, limit, rt.repair)
	if c.rejectParams() {
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

// rejectParams answers 413 for a body longer than its read cap (never act on a
// truncated parse: the T or an id may have been cut off) and 400 for too many
// pairs. It reports whether it answered.
func (c *call) rejectParams() bool {
	switch {
	case c.p.truncated:
		c.text(http.StatusRequestEntityTooLarge, "Request Entity Too Large")
	case c.p.tooMany:
		c.text(http.StatusBadRequest, "Bad Request")
	default:
		return false
	}
	return true
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
	a.acctMu.Lock()
	gen := a.acctGen
	a.acctMu.Unlock()
	acc, ok, err := a.db.Account(ctx)
	if err != nil {
		return nil, err
	}
	if a.afterAcctRead != nil {
		a.afterAcctRead()
	}
	s := &acctSnap{loaded: a.now()}
	if ok && acc.APIPasswordHash != "" {
		s.enabled, s.username, s.hash, s.secret = true, acc.Username, acc.APIPasswordHash, acc.Secret
		s.token = makeToken(acc.Username, acc.Secret, acc.APIPasswordHash)
	}
	a.acctMu.Lock()
	if a.acctGen == gen {
		a.acct.Store(s)
	}
	a.acctMu.Unlock()
	return s, nil
}

// makeToken is username + "/" + hex(HMAC-SHA256(secret, "greader-token-v1|" + hash)):
// 64 hex after the slash, never containing '='.
func makeToken(username, secret, apiHash string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("greader-token-v1|" + apiHash))
	return username + "/" + hex.EncodeToString(h.Sum(nil))
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
	bad := func() {
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
		bad()
		return
	}
	email, pass := c.p.Get("Email"), c.p.Get("Passwd")
	emailOK := strings.EqualFold(email, s.username)
	a.ver.SetSecret([]byte(s.secret))
	// A remembered success costs no hashing, so it is not paced: a signed-in
	// client keeps working while its address is over budget. It leaves the
	// failure count alone (another client may share the address).
	if emailOK && a.ver.Remembered("api", pass, s.hash) {
		c.loginOK(s)
		return
	}
	// Admission before any hashing (design §6.3): one attempt per client hashes
	// at a time and, over budget, waits its turn (auth.FailureTracker); later
	// ones wait (bounded) rather than fail. Only an attempt that could not start is refused unchecked.
	if !a.fails.Acquire(ctx, ip) {
		bad()
		return
	}
	failed, proved := false, false
	defer func() {
		a.fails.Finish(ip, failed)
		if proved {
			a.fails.Forget(ip)
		}
	}()
	// The password is always verified, even when the email is wrong.
	ok, busy := a.ver.VerifyBusy(ctx, "api", pass, s.hash)
	if busy {
		// Hashing slot unavailable (design §6.3: a 401 after the 5 s wait). It says
		// nothing about the password, so nothing is counted and there is no
		// Retry-After.
		bad()
		return
	}
	if !ok || !emailOK {
		failed = true
		bad()
		return
	}
	proved = true
	c.loginOK(s)
}

// loginOK answers a successful ClientLogin.
func (c *call) loginOK(s *acctSnap) {
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

// userInfo is GET user-info (some clients call it right after ClientLogin).
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

// serverError answers a genuine database failure (the only 5xx). A folder change
// the store refuses (store.FolderRefused: a name too long or with control
// characters, a path another folder has, the nesting rules, a merge it cannot
// do) is client data, never a 5xx: it is logged and answered OK with nothing
// changed, like any other ignored value (a non-2xx wedges a client's sync queue),
// and the client sees the old folders again on its next sync.
func (c *call) serverError(what string, err error) {
	if store.FolderRefused(err) {
		c.a.log.Warn("greader: "+what+": folder change refused", "err", err, "path", c.path, "ua", c.r.UserAgent())
		c.ok()
		return
	}
	if errors.Is(err, store.ErrMaintenance) {
		// A search index rebuild owns the writer for up to 45 s. Reader clients
		// retry a 503 (edit-tag and friends are queued and sent again).
		c.a.log.Info("greader: "+what+": deferred by maintenance", "err", err, "path", c.path)
		c.w.Header().Set("Retry-After", strconv.Itoa(int(store.MaintenanceRetryAfter/time.Second)))
		c.text(http.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	c.a.log.Error("greader: "+what, "err", err, "path", c.path)
	c.text(http.StatusInternalServerError, "Internal Server Error")
}

// publish sends an SSE event when a hub is attached.
func (a *API) publish(typ string, data any) {
	if a.opt.Events != nil {
		a.opt.Events.Publish(typ, data)
	}
}

// InvalidateAccount drops the cached account snapshot, so a changed API
// password revokes every token on the next request instead of within acctTTL.
func (a *API) InvalidateAccount() {
	a.acctMu.Lock()
	a.acctGen++
	a.acct.Store(nil)
	a.acctMu.Unlock()
}
