// Package api is the web UI's JSON API (design §7): cookie-session login, the
// same-origin guard on state-changing requests, status and feed health, manual
// refresh, the SSE stream and OPML import/export. The Reader API under
// /api/greader.php is separate and is claimed ahead of this mux (design §6.1).
package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/stats"
	"github.com/WPTK/kipple/internal/store"
)

const (
	cookieName = "kipple_session"
	// sessionTTL is the cookie Max-Age and the sliding session expiry. The plan
	// (Cloudflare Access notes) says 90 days; design §7 says 30. The plan wins.
	sessionTTL = 90 * 24 * time.Hour

	heartbeatDefault = 15 * time.Second
	// writeSlack is each SSE write's own deadline, replacing the server-wide
	// WriteTimeout for the life of the stream.
	writeSlack = 10 * time.Second
)

// Scheduler is what the API needs from sched.Scheduler.
type Scheduler interface {
	RefreshAll() (sched.RunInfo, error)
	StartImport(feedIDs []int64) (sched.RunInfo, error)
	Status() ([]sched.RunStatus, int)
}

// Options configures New.
type Options struct {
	DB             *store.DB
	Sched          Scheduler
	Hub            *events.Hub
	Logger         *slog.Logger
	TrustedProxies []netip.Addr
	// Clients reports Reader client families' last-seen times (greader.API.LastSeen); optional.
	Clients func() map[string]time.Time
	// Lockout defaults to the plan settings. Verifier must be the one instance
	// shared with greader.Options.Verifier (nil builds a private one, tests only).
	Lockout  *auth.Lockout
	Verifier *auth.Verifier
	// Stats records open and star events; nil builds the SQL recorder on Now.
	Stats stats.Recorder
	// Version is reported by /api/bootstrap.
	Version string
	// CountsInterval is the minimum gap between `counts` events (default 1 s).
	CountsInterval time.Duration
	// Now defaults to the wall clock. Heartbeat defaults to 15 s.
	Now       func() time.Time
	Heartbeat time.Duration
}

// Server holds the handlers.
type Server struct {
	opt  Options
	db   *store.DB
	log  *slog.Logger
	now  func() time.Time
	lock *auth.Lockout

	verifier *auth.Verifier // shared with the Reader API (one argon2 slot per process)
	rec      stats.Recorder

	cmu    sync.Mutex // guards the counts coalescer
	clast  time.Time
	ctimer *time.Timer
	closed bool
}

// New builds the API server.
func New(opt Options) *Server {
	s := &Server{opt: opt, db: opt.DB, log: opt.Logger, now: opt.Now, lock: opt.Lockout, verifier: opt.Verifier}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.lock == nil {
		s.lock = auth.NewLockout(s.now)
	}
	if s.verifier == nil {
		s.verifier = auth.NewVerifier(nil, auth.VerifierOptions{})
	}
	s.rec = opt.Stats
	if s.rec == nil {
		s.rec = stats.New(s.now)
	}
	if s.opt.CountsInterval <= 0 {
		s.opt.CountsInterval = time.Second
	}
	if s.opt.Heartbeat <= 0 {
		s.opt.Heartbeat = heartbeatDefault
	}
	return s
}

// Register mounts /healthz and /api/ on mux. Everything under /api/ that is not
// a known route answers here (401 or 404 JSON), never the SPA.
func (s *Server) Register(mux *http.ServeMux) {
	handle := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, noFraming(h)) }
	handle("GET /healthz", s.healthz)
	handle("POST /api/auth/login", s.login)
	handle("POST /api/auth/logout", s.authed(s.logout))
	handle("GET /api/auth/me", s.authed(s.me))
	handle("GET /api/status", s.authed(s.status))
	handle("GET /api/health/feeds", s.authed(s.healthFeeds))
	handle("POST /api/refresh", s.authed(s.refresh))
	handle("GET /api/events", s.authed(s.events))
	handle("GET /api/bootstrap", s.authed(s.bootstrap))
	handle("GET /api/feeds/{id}/icon", s.authed(s.feedIcon))
	handle("GET /api/items", s.authed(s.listItems))
	handle("POST /api/items/mark-read", s.authed(s.markRead))
	handle("GET /api/items/{id}", s.authed(s.getItem))
	handle("POST /api/items/{id}/open", s.authed(s.openItem))
	handle("PUT /api/items/{id}/star", s.authed(s.starItem))
	handle("POST /api/maintenance/fts-rebuild", s.authed(s.ftsRebuild))
	handle("POST /api/stats/events", s.authed(s.statsEvents))
	handle("POST /api/opml", s.authed(s.opmlImport))
	handle("GET /api/opml", s.authed(s.opmlExport))
	handle("/api/", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))
}

// noFraming forbids embedding a response in a frame (clickjacking). Both
// headers are sent: CSP frame-ancestors is the standard, X-Frame-Options covers
// older browsers.
func noFraming(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		h(w, r)
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte("ok"))
}

// authed requires a valid session cookie, then (for state-changing methods and
// the OPML download) the same-origin guard. Auth failures win over origin
// failures, so an anonymous caller learns nothing about the guard.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/greader.php") {
			http.NotFound(w, r) // never a UI route (design §6.1)
			return
		}
		if !s.sessionOK(w, r) {
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
		if needsOriginCheck(r) && !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "origin")
			return
		}
		h(w, r)
	}
}

func needsOriginCheck(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	return r.URL.Path == "/api/opml" || r.URL.Path == "/api/stats/export.csv"
}

// sameOrigin is design §7's same-origin enforcement.
func (s *Server) sameOrigin(r *http.Request) bool {
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" {
		if sfs != "same-origin" {
			return false
		}
	} else if o := r.Header.Get("Origin"); o == "" || o != s.scheme(r)+"://"+r.Host {
		return false
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/stats/events" {
		return true // sendBeacon cannot set headers; rules 1 and 2 still applied (design §7)
	}
	c := r.Header.Get("X-Kipple-Client")
	return c == "web" || c == "pwa"
}

func (s *Server) scheme(r *http.Request) string { return auth.EffectiveScheme(r, s.opt.TrustedProxies) }

// sessionOK validates the cookie, sliding the session (and re-issuing the
// cookie) at most hourly.
func (s *Server) sessionOK(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	now := s.now()
	st, err := s.db.CheckSession(r.Context(), sessionID(c.Value), now.Unix(), now.Add(sessionTTL).Unix())
	if err != nil {
		s.log.Error("api: session lookup", "err", err)
		return false
	}
	if st == store.SessionRenewed {
		s.setCookie(w, r, c.Value)
	}
	return st != store.SessionNone
}

func sessionID(cookieValue string) string {
	sum := sha256.Sum256([]byte(cookieValue))
	return hex.EncodeToString(sum[:])
}

func newCookieValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// setCookie issues the session cookie: HttpOnly, SameSite=Lax, Path=/,
// persistent (WebKit drops session cookies in Home Screen apps), Secure only
// when the effective scheme is https.
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: value, Path: "/", MaxAge: int(sessionTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.scheme(r) == "https",
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.scheme(r) == "https",
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, kind string) {
	writeJSON(w, code, map[string]string{"error": kind})
}

func (s *Server) clientIP(r *http.Request) string { return auth.ClientIP(r, s.opt.TrustedProxies) }
