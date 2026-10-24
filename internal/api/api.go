// Package api is the web UI's JSON API (design §7): cookie-session login, the
// same-origin guard on state-changing requests, status and feed health, manual
// refresh, the SSE stream and OPML import/export. The Reader API under
// /api/greader.php is separate and is claimed ahead of this mux (design §6.1).
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/buildinfo"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/ftrun"
	"github.com/WPTK/kipple/internal/imgcache"
	"github.com/WPTK/kipple/internal/imgproxy"
	"github.com/WPTK/kipple/internal/reach"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/stats"
	"github.com/WPTK/kipple/internal/store"
)

const (
	cookieName = "kipple_session"
	// hostCookieName is the session cookie over https. The browser keeps a __Host- cookie only when it is Secure, has
	// Path=/ and no Domain, so a page on a sibling subdomain cannot set one for the whole parent domain: the only
	// session cookie the server reads is this host's own, the one its clear reaches. Over plain http the browser
	// refuses the prefix, so cookieName is used there.
	hostCookieName = "__Host-" + cookieName
	// markerName is the session marker: a cookie script can read, set and cleared with the session cookie, so the
	// app knows at launch, without a request, whether to expect a session. It holds no secret and is only a hint;
	// the session cookie stays the truth (docs/design.md §7). Over https it is hostMarkerName instead.
	markerName = "kipple_signed_in"
	// hostMarkerName is the marker over https. The browser keeps a __Host- cookie only when it is Secure, has Path=/
	// and no Domain, so a page on a sibling subdomain cannot set one for the whole parent domain: the only marker the
	// app reads is this host's own, the one the server's clear reaches.
	hostMarkerName = "__Host-" + markerName
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
	ApplyRetention(all bool) (sched.RunInfo, error)
	StartImport(feedIDs []int64) (sched.RunInfo, error)
	// Status is the active runs and the fetches in flight; ok is false when the dispatcher did not answer in time.
	Status() (runs []sched.RunStatus, inflight int, ok bool)
	// HostHolds is the per-host politeness deadlines still in the future.
	HostHolds() map[string]time.Time
	// Submit queues a per-feed priority job; Wake nudges a tick; Shutdown closes
	// when the scheduler stops, so a waiting handler can bail out.
	Submit(p sched.Priority) (<-chan sched.Reply, error)
	Wake()
	Shutdown() <-chan struct{}
}

// Options configures New.
type Options struct {
	DB     *store.DB
	Sched  Scheduler
	Hub    *events.Hub
	Logger *slog.Logger
	// Reach is the reachability settings in force (public URL, allowed host names,
	// trusted proxies, Cloudflare Access; package reach). PATCH /api/settings
	// updates it. Nil opens one on DB (tests).
	Reach *reach.Live
	// ReaderLastSeen reports when a Reader API client last called (greader.API.LastSeen; the zero
	// time if none has); optional.
	ReaderLastSeen func() time.Time
	// Verifier must be the one instance shared with greader.Options.Verifier
	// (nil builds a private one, tests only).
	Verifier *auth.Verifier
	// OnAPIPasswordChange runs after the Reader API password changes (drops the
	// Reader API cached token at once); optional.
	OnAPIPasswordChange func()
	// Backups builds and serves backup exports; nil builds one on DB (tests).
	Backups *backup.Manager
	// Stats records open and star events; nil builds the SQL recorder on Now.
	Stats stats.Recorder
	// Version is reported by /api/bootstrap and used in the proxy User-Agent.
	Version string
	// Build is the commit and build date the binary was built from (buildinfo), for /api/about; its Version
	// is ignored in favor of Version.
	Build buildinfo.Info
	// WebBuild is the build id of the embedded web bundle (internal/web BuildID); /api/bootstrap reports it so
	// a page can tell the server was rebuilt since it loaded. Empty for a build that carries none.
	WebBuild string
	// DataDir is the data directory; /api/about only reports whether it is writable, never where it is.
	DataDir string
	// UserAgent returns Kipple's own outgoing User-Agent (fetch.Client.DefaultUserAgent,
	// which names the public URL in force); the image proxy, web feed discovery and
	// article extraction send it. Nil builds one from Reach (tests).
	UserAgent func() string
	// Guard supplies the SSRF-guarded HTTP transports of the image proxy and
	// full-text extraction (fetch.Client.Transport); nil builds a private client.
	Guard func(allowPrivate, insecureTLS, noHTTP2 bool) http.RoundTripper
	// Runner runs full-text extractions. Share the scheduler's so the ingest pool
	// and the on-demand endpoint join each other's runs and share the per-host
	// limit; nil builds a private one.
	Runner *ftrun.Runner
	// ImgCache is the on-disk cache under the image proxy; nil serves every image
	// straight from its source (tests).
	ImgCache *imgcache.Cache
	// CountsInterval is the minimum gap between `counts` events (default 1 s).
	CountsInterval time.Duration
	// PreviewBudget bounds one filter preview scan (default 5 s); tests shorten it.
	PreviewBudget time.Duration
	// Now defaults to the wall clock. Heartbeat defaults to 15 s.
	Now       func() time.Time
	Heartbeat time.Duration

	// Setup is setup mode (docs/design.md §7.1e): when it is pending at
	// Register, the setup route is mounted. Nil means never in setup mode.
	Setup *setup.Manager
	// Gate is the open gate; its Trusted defaults to the trusted proxies of Reach.
	Gate setup.Gate
	// Restore is the setup wizard's restore (setup mode only); nil builds one on
	// DataDir when there is one.
	Restore *backup.Restorer
	// Restart shuts the process down cleanly after a restore or reset is
	// confirmed, so the next start applies it; nil does nothing (tests).
	Restart func()
	// EnvAccount: KIPPLE_USERNAME and KIPPLE_PASSWORD are set, so a start with no
	// account would create one from them (the reset dialog warns about it).
	EnvAccount bool
}

// Server holds the handlers.
type Server struct {
	started time.Time // when this server was built, for /api/about
	opt     Options
	db      *store.DB
	log     *slog.Logger
	now     func() time.Time
	fails   *auth.FailureTracker
	reach   *reach.Live // the reachability settings in force (Options.Reach)
	// setupSlot admits one account creation or restore confirm at a time
	// (setupAccount, restoreConfirm), so the two exclude each other.
	setupSlot chan struct{}
	// exportSlot admits one statistics export at a time (statsExport): a download needs
	// no origin proof, so a page elsewhere can start one, and each streams the whole table.
	// A second request waits for the slot instead of being refused, because a browser
	// saves whatever a download link answers, an error included.
	exportSlot    chan struct{}
	exportWaiting atomic.Int32  // requests queued for exportSlot
	exportWait    time.Duration // the longest one waits

	mode       modeCache // the Host gate's cached auth mode and allowed hosts
	hostWarnMu sync.Mutex
	hostWarnAt time.Time

	// statsGate admits one stats summary computation at a time (it holds up to three reader connections).
	statsGate chan struct{}

	verifier *auth.Verifier // shared with the Reader API (one argon2 slot per process)
	rec      stats.Recorder

	runner *ftrun.Runner // full-text extraction, shared with the ingest pool

	backups *backup.Manager
	restore *backup.Restorer // the setup wizard's restore; nil outside setup mode

	imgMu       sync.Mutex // guards imgSecret and imgH
	imgSecret   []byte
	imgSecretAt time.Time // when imgSecret was last read from the account row
	imgHSecret  []byte    // the secret imgH was built with
	imgClosed   bool      // Close ran: imageHandler builds no new handler
	imgH        *imgproxy.Handler
	imgMode     atomic.Pointer[string] // cached imgproxy.mode for the CSP; refreshed on PATCH
	imgModeMu   sync.Mutex             // serializes refreshImgMode's read and store
	imgModeRead func(string)           // tests: runs between refreshImgMode's read and its store

	// bgMu and bgClosed admit background runs onto apply.wg (bgStart). The
	// apply.stop that Close calls before apply.wg.Wait sets bgClosed under bgMu,
	// so every wg.Add either happens before that Wait or is refused.
	bgMu     sync.Mutex
	bgClosed bool

	autoReadAdmitted func() // tests: runs once startAutoRead has admitted a run, before its goroutine starts
	removeProved     func() // tests: runs after a password removal was proven, before its write

	apply    applyState    // the retroactive filter apply run
	autoRead autoReadState // the auto-read catch-up run
	devRegs  deviceRegs    // per-session device registrations (currentDevice)

	pubMu  sync.Mutex // serializes query+publish so counts events never arrive out of order
	cmu    sync.Mutex // guards the counts coalescer
	clast  time.Time
	ctimer *time.Timer
	closed bool
}

// New builds the API server.
func New(opt Options) *Server {
	s := &Server{started: time.Now(), opt: opt, db: opt.DB, log: opt.Logger, now: opt.Now, verifier: opt.Verifier, statsGate: make(chan struct{}, 1)}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.fails = auth.NewFailureTracker()
	s.fails.Now = s.now
	s.setupSlot = make(chan struct{}, 1)
	s.exportSlot = make(chan struct{}, 1)
	s.exportWait = defaultExportWait
	s.reach = opt.Reach
	if s.reach == nil {
		// Tests only: production passes the process's one Live in.
		s.reach = reach.Fixed(reach.State{})
		if s.db != nil {
			l, err := reach.Open(context.Background(), s.db, reach.Options{Logger: s.log, NoPrefetch: true})
			if err != nil {
				panic(err)
			}
			s.reach = l
		}
	}
	if s.opt.Gate.Trusted == nil {
		s.opt.Gate.Trusted = s.reach.Trusted
	}
	if s.verifier == nil {
		s.verifier = auth.NewVerifier(nil, auth.VerifierOptions{})
	}
	if s.opt.Guard == nil || s.opt.UserAgent == nil {
		// Tests only: production passes the process's fetch.Client pieces in. The
		// User-Agent string is built in one place, fetch.NewClient.
		c := fetch.NewClient(fetch.ClientOptions{Version: opt.Version, PublicURL: s.reach.PublicURL})
		if s.opt.Guard == nil {
			s.opt.Guard = c.Transport
		}
		if s.opt.UserAgent == nil {
			s.opt.UserAgent = c.DefaultUserAgent
		}
	}
	s.runner = opt.Runner
	if s.runner == nil {
		s.runner = ftrun.New(ftrun.Options{
			DB: s.db, Log: s.log,
			Extractor: extract.New(extract.Options{Transport: s.opt.Guard, UserAgent: s.opt.UserAgent, Timeout: extractBudget}),
		})
	}
	s.backups = opt.Backups
	if s.backups == nil {
		s.backups = backup.New(backup.Options{DB: s.db, Logger: s.log, Version: opt.Version})
	}
	s.restore = opt.Restore
	if s.restore == nil && opt.Setup.Pending() && opt.DataDir != "" {
		ro := backup.RestorerOptions{DataDir: opt.DataDir, Logger: s.log}
		if s.db != nil {
			ro.Live = s.db.Reader()
		}
		s.restore = backup.NewRestorer(ro)
	}
	s.rec = opt.Stats
	if s.rec == nil {
		s.rec = stats.New(s.now)
	}
	bgCtx, cancel := context.WithCancel(context.Background())
	s.apply.ctx = bgCtx
	s.apply.budget = applyBudget
	s.apply.stop = func() {
		s.bgMu.Lock()
		s.bgClosed = true
		s.bgMu.Unlock()
		cancel()
	}
	if s.opt.CountsInterval <= 0 {
		s.opt.CountsInterval = time.Second
	}
	if s.opt.Heartbeat <= 0 {
		s.opt.Heartbeat = heartbeatDefault
	}
	// Know the mode from the start, so a later failed read has a last known one.
	if s.db != nil {
		s.snapshot(context.Background())
	}
	return s
}

// bgStart registers one background run (a filter apply or an auto-read
// catch-up) on s.apply.wg, or reports false once Close has begun. The run's
// goroutine must call s.apply.wg.Done when it ends.
func (s *Server) bgStart() bool {
	s.bgMu.Lock()
	defer s.bgMu.Unlock()
	if s.bgClosed || s.apply.ctx == nil {
		return false
	}
	s.apply.wg.Add(1)
	return true
}

// Register mounts /healthz and /api/ on mux. Everything under /api/ that is not
// a known route answers here (401 or 404 JSON), never the SPA.
func (s *Server) Register(mux *http.ServeMux) {
	handle := mux.HandleFunc // security headers come from httpx.Secure around the root handler
	handle("GET /healthz", s.healthz)
	handle("GET /api/instance", s.instance)
	s.registerSetup(mux)
	handle("POST /api/auth/open", s.authOpen)
	handle("POST /api/auth/login", s.login)
	handle("POST /api/auth/logout", s.authed(s.logout))
	handle("GET /api/auth/me", s.authed(s.me))
	handle("GET /api/status", s.authed(s.status))
	handle("GET /api/health/feeds", s.authed(s.healthFeeds))
	handle("POST /api/refresh", s.authed(s.refresh))
	handle("GET /api/events", s.authed(s.events))
	handle("GET /api/bootstrap", s.authed(s.bootstrap))
	handle("GET /api/about", s.authed(s.about))
	handle("GET /img/{sig}/{flags}/{u}", s.authed(s.image))
	handle("GET /api/feeds/latest", s.authed(s.feedsLatest))
	handle("GET /api/feeds/{id}/icon", s.authed(s.feedIcon))
	handle("GET /api/imgcache", s.authed(s.imgcacheStats))
	handle("POST /api/imgcache/clear", s.authed(s.imgcacheClear))
	handle("GET /api/items", s.authed(s.listItems))
	handle("POST /api/items/mark-read", s.authed(s.markRead))
	handle("POST /api/items/unmute", s.authed(s.unmuteItems))
	handle("GET /api/items/{id}", s.authed(s.getItem))
	handle("POST /api/items/{id}/fulltext", s.authed(s.itemFulltext))
	handle("POST /api/items/{id}/open", s.authed(s.openItem))
	handle("PUT /api/items/{id}/star", s.authed(s.starItem))
	handle("GET /api/catchup", s.authed(s.catchup))
	handle("POST /api/catchup/seen", s.authed(s.catchupSeen))
	handle("POST /api/maintenance/fts-rebuild", s.authed(s.ftsRebuild))
	handle("POST /api/stats/events", s.authed(s.statsEvents))
	handle("GET /api/stats/summary", s.authed(s.statsSummary))
	handle("GET /api/stats/export", s.authed(s.statsExport))
	handle("GET /api/stats/dictionary", s.authed(s.statsDictionary))
	handle("POST /api/stats/delete", s.authed(s.statsDelete))
	handle("GET /api/settings", s.authed(s.getSettings))
	handle("PATCH /api/settings", s.authed(s.patchSettings))
	handle("GET /api/device", s.authed(s.getDevice))
	handle("PATCH /api/device", s.authed(s.patchDevice))
	handle("PUT /api/device/name", s.authed(s.putDeviceName))
	handle("POST /api/device/make-default", s.authed(s.makeDeviceDefault))
	handle("POST /api/device/copy-from/{id}", s.authed(s.copyDeviceFrom))
	handle("GET /api/devices", s.authed(s.listDevices))
	handle("DELETE /api/devices/{id}", s.authed(s.deleteDevice))
	handle("GET /api/retention/preview", s.authed(s.retentionPreview))
	handle("POST /api/retention/apply", s.authed(s.retentionApply))
	handle("POST /api/account/password", s.authed(s.accountPassword))
	handle("POST /api/account/api-password", s.authed(s.accountAPIPassword))
	handle("POST /api/onboarding/complete", s.authed(s.onboardingComplete))
	handle("GET /api/starter-feeds", s.authed(s.starterFeeds))
	handle("POST /api/starter-feeds", s.authed(s.starterSubscribe))
	handle("GET /api/reset", s.authed(s.resetInfo))
	handle("POST /api/reset", s.authed(s.resetKipple))
	handle("POST /api/backup", s.authed(s.backupCreate))
	handle("GET /api/backup/jobs/{id}", s.authed(s.backupJob))
	handle("GET /api/backup/{token}", s.authed(s.backupDownload))
	handle("POST /api/opml", s.authed(s.opmlImport))
	handle("GET /api/opml", s.authed(s.opmlExport))
	handle("POST /api/feeds", s.authed(s.addFeed))
	handle("GET /api/feeds/{id}", s.authed(s.getFeed))
	handle("POST /api/reorder", s.authed(s.reorder))
	handle("PATCH /api/feeds/{id}", s.authed(s.patchFeed))
	handle("DELETE /api/feeds/{id}", s.authed(s.deleteFeed))
	handle("POST /api/feeds/delete", s.authed(s.deleteFeeds))
	handle("POST /api/feeds/delete-preview", s.authed(s.deletePreview))
	handle("POST /api/feeds/{id}/refresh", s.authed(s.refreshFeed))
	handle("POST /api/feeds/{id}/mark-fetch-read", s.authed(s.markFetchRead))
	handle("POST /api/feeds/{id}/trimmed-unread/reset", s.authed(s.resetTrimmedUnread))
	handle("POST /api/feeds/{id}/redirect/keep", s.authed(s.keepRedirect))
	handle("POST /api/archive/purge-unstarred", s.authed(s.purgeArchive))
	handle("POST /api/folders", s.authed(s.createFolder))
	handle("PATCH /api/folders/{id}", s.authed(s.patchFolder))
	handle("DELETE /api/folders/{id}", s.authed(s.deleteFolder))
	handle("GET /api/health/feeds/{id}/log", s.authed(s.feedLog))
	handle("GET /api/filters", s.authed(s.listFilters))
	handle("POST /api/filters", s.authed(s.createFilter))
	handle("POST /api/filters/preview", s.authed(s.previewFilter))
	handle("PATCH /api/filters/{id}", s.authed(s.patchFilter))
	handle("DELETE /api/filters/{id}", s.authed(s.deleteFilter))
	handle("POST /api/filters/{id}/apply", s.authed(s.applyFilterRoute))
	handle("POST /api/library/auto-read/preview", s.authed(s.previewAutoRead))
	handle("POST /api/library/auto-read/run", s.authed(s.runAutoReadRoute))
	handle("GET /api/saved-searches", s.authed(s.listSavedSearches))
	handle("POST /api/saved-searches", s.authed(s.createSavedSearch))
	handle("POST /api/saved-searches/reorder", s.authed(s.reorderSavedSearches))
	handle("PATCH /api/saved-searches/{id}", s.authed(s.patchSavedSearch))
	handle("DELETE /api/saved-searches/{id}", s.authed(s.deleteSavedSearch))
	handle("/api/", s.authed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	}))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte("ok"))
}

// authed requires a valid session cookie, then (for state-changing methods and
// the backup job poll) the same-origin guard. Auth failures win over origin
// failures, so an anonymous caller learns nothing about the guard.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/greader.php") {
			http.NotFound(w, r) // never a UI route (design §6.1)
			return
		}
		ok, refusal, err := s.signedIn(w, r)
		if err != nil {
			// A failed lookup is not a missing session: answering 401 would sign the app out.
			s.serverError(w, "session lookup", err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "auth")
			return
		}
		if needsOriginCheck(r) && !s.sameOrigin(r) {
			writeError(w, http.StatusForbidden, "origin")
			return
		}
		if refusal != "" {
			writeOpenRefused(w, refusal)
			return
		}
		h(w, r)
	}
}

// signedIn is whether r may use the signed-in API, as authed and GET /api/instance both decide it: a live session
// and, in open mode, the open gate. refusal is the gate's reason when the session is live but the gate refuses.
// In open mode the session is only a convenience, the gate is the access check, so it applies to every request (a
// device that left the tailnet or the local network loses access at once).
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request) (ok bool, refusal string, err error) {
	ok, err = s.sessionOK(w, r)
	if err != nil || !ok {
		return ok, "", err
	}
	if snap := s.snapshot(r.Context()); snap.mode == store.AuthOpen || snap.failed {
		refusal = s.gateRefusal(r, snap, false)
	}
	return true, refusal, nil
}

func needsOriginCheck(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return true
	}
	// The job poll returns the download token, so it gets the whole rule (session,
	// same-origin, X-Kipple-Client) though it is a GET. Every other GET, the downloads
	// included, is a read another origin cannot see (SameSite=Lax cookie, same-origin
	// CORP, nosniff), and a plain link or new tab cannot carry those headers.
	return strings.HasPrefix(r.URL.Path, "/api/backup/jobs/")
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

func (s *Server) scheme(r *http.Request) string { return s.reach.Scheme(r) }

// sessionOK validates the cookie, sliding the session (and re-issuing the
// cookie) at most hourly. An error is a failed lookup, not a missing session.
// It also brings the session marker in line with the answer: set for a live
// session that arrived without it (one made before the marker existed),
// cleared for a request that carries it with no live session (expired, signed
// out elsewhere, revoked by a password change). Only the marker is cleared
// there, never the session cookie: a stale request that answers after a new
// sign-in would otherwise sign that new session out.
func (s *Server) sessionOK(w http.ResponseWriter, r *http.Request) (bool, error) {
	_, merr := r.Cookie(s.marker(r))
	marked := merr == nil
	c, err := s.sessionValue(r)
	if err != nil || c.Value == "" {
		if hasMarker(r) {
			s.clearMarker(w, r)
		}
		return false, nil
	}
	now := s.now()
	st, err := s.db.CheckSession(r.Context(), sessionID(c.Value), now.Unix(), now.Add(sessionTTL).Unix())
	if err != nil {
		return false, err
	}
	switch {
	case st == store.SessionRenewed:
		s.setCookie(w, r, c.Value)
	case st == store.SessionNone && hasMarker(r):
		s.clearMarker(w, r)
	case st != store.SessionNone && !marked:
		s.setMarker(w, r)
	}
	return st != store.SessionNone, nil
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

// sessionName is the session cookie's name for r: hostCookieName over https, where the browser enforces what the
// prefix promises, cookieName over plain http, where it would refuse a __Host- cookie.
func (s *Server) sessionName(r *http.Request) string {
	if s.scheme(r) == "https" {
		return hostCookieName
	}
	return cookieName
}

// sessionValue is the session cookie r carries under this scheme's name. A cookie under the other name is never read
// as the session: over https the bare name is the one a page on a sibling subdomain can set, so it cannot pose as it.
// The only things done with one are to clear it from the browser and to end the session behind it (endHeldSessions).
func (s *Server) sessionValue(r *http.Request) (*http.Cookie, error) {
	return r.Cookie(s.sessionName(r))
}

// maxHeldSessions caps how many distinct session cookie values one request ends: a browser holds a handful at most
// (the host's own, a leftover from the other scheme, a planted parent-domain copy), and the write stays one small
// transaction however many a client sends. 16 is plenty: a __Host- cookie cannot be planted, and a bare one reaches the
// auth routes only from the few parent paths that match them (about six), so a browser never holds that many. The
// cookie under this scheme's name is walked first, so the signed-in row is never the one cut off, even for a
// hand-built client.
const maxHeldSessions = 16

// heldSessionIDs is the id of the session behind every distinct value of a session cookie r holds, under either name,
// at most maxHeldSessions. A browser can send the same name twice (a copy set for the parent domain beside the host's
// own), and r.Cookie returns just the first, so this walks them all.
func heldSessionIDs(r *http.Request, first string) []string {
	var ids []string
	seen := map[string]bool{}
	cookies := r.Cookies()
	sort.SliceStable(cookies, func(i, j int) bool { return cookies[i].Name == first && cookies[j].Name != first })
	for _, c := range cookies {
		if (c.Name != cookieName && c.Name != hostCookieName) || c.Value == "" || seen[c.Value] {
			continue
		}
		if len(ids) == maxHeldSessions {
			break
		}
		seen[c.Value] = true
		ids = append(ids, sessionID(c.Value))
	}
	return ids
}

// endHeldSessions ends, in one write, the session behind every session cookie r holds, under either name. Sign-out
// does it so the browser ends with no session cookie of either name and no row it held stays live for the rest of its
// 90 days. Sign-in does it too, after the new session exists: its answer replaces or clears every name the browser
// held, so a row it held would be stranded. Ending a session the request already holds a cookie for gives nobody
// anything new.
func (s *Server) endHeldSessions(r *http.Request) error {
	return s.db.DeleteSessions(r.Context(), heldSessionIDs(r, s.sessionName(r)))
}

// setCookie issues the session cookie: HttpOnly, SameSite=Lax, Path=/,
// persistent (WebKit drops session cookies in Home Screen apps), Secure when
// the scheme is https (reach.State.Scheme: TLS, a trusted proxy, or the host of
// an https public URL), so a LAN visit over plain http still signs in. It also
// clears one the browser sent under the other name, and the session marker goes
// with it, with the same lifetime and rules.
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, value string) {
	name := s.sessionName(r)
	s.sessionCookie(w, r, name, value, int(sessionTTL/time.Second))
	other := cookieName
	if name == cookieName {
		other = hostCookieName
	}
	if _, err := r.Cookie(other); err == nil {
		s.clearNamed(w, r, other)
	}
	s.setMarker(w, r)
}

// clearCookie clears the session cookie under both names, whatever the scheme is now, and its marker.
func (s *Server) clearCookie(w http.ResponseWriter, r *http.Request) {
	s.clearNamed(w, r, hostCookieName)
	s.clearNamed(w, r, cookieName)
	s.clearMarker(w, r)
}

// marker is the session marker's name for r: hostMarkerName over https, where the browser enforces what the prefix
// promises, markerName over plain http, where it would refuse a __Host- cookie.
func (s *Server) marker(r *http.Request) string {
	if s.scheme(r) == "https" {
		return hostMarkerName
	}
	return markerName
}

// setMarker sets the marker under this scheme's name, and clears one the browser sent under the other name (set while
// the scheme was the other one), so a browser never keeps a marker its session does not back.
func (s *Server) setMarker(w http.ResponseWriter, r *http.Request) {
	name := s.marker(r)
	s.sessionCookie(w, r, name, "1", int(sessionTTL/time.Second))
	other := markerName
	if name == markerName {
		other = hostMarkerName
	}
	if _, err := r.Cookie(other); err == nil {
		s.clearMarkerNamed(w, r, other)
	}
}

// hasSessionCookie says whether r carries a session cookie under either name, for the same reason as hasMarker.
func hasSessionCookie(r *http.Request) bool {
	for _, name := range []string{hostCookieName, cookieName} {
		if _, err := r.Cookie(name); err == nil {
			return true
		}
	}
	return false
}

// hasMarker says whether r carries a marker under either name. The scheme can change while a browser holds one (a
// trusted proxy's address or the public URL changed), so a check for a stale marker looks for both.
func hasMarker(r *http.Request) bool {
	for _, name := range []string{hostMarkerName, markerName} {
		if _, err := r.Cookie(name); err == nil {
			return true
		}
	}
	return false
}

// clearMarker clears the marker under both names, whatever the scheme is now: one set while the scheme was the other
// one goes too. The __Host- clear always carries Secure, Path=/ and no Domain, or the browser ignores it.
func (s *Server) clearMarker(w http.ResponseWriter, r *http.Request) {
	s.clearMarkerNamed(w, r, hostMarkerName)
	s.clearMarkerNamed(w, r, markerName)
}

func (s *Server) clearMarkerNamed(w http.ResponseWriter, r *http.Request, name string) {
	s.clearNamed(w, r, name)
}

// clearNamed clears one of the session cookie's or marker's names. A __Host- clear always carries Secure, Path=/ and
// no Domain, or the browser ignores it.
func (s *Server) clearNamed(w http.ResponseWriter, r *http.Request, name string) {
	if name == hostMarkerName || name == hostCookieName {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: true})
		return
	}
	s.sessionCookie(w, r, name, "", -1)
}

// sessionCookie writes the session cookie or its marker. They differ only in HttpOnly: the marker is for the
// app's script to read, and holds nothing a reader of it could use.
func (s *Server) sessionCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: name == cookieName || name == hostCookieName, SameSite: http.SameSiteLaxMode, Secure: s.scheme(r) == "https",
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

func (s *Server) clientIP(r *http.Request) string { return auth.ClientIP(r, s.reach.Trusted()) }
