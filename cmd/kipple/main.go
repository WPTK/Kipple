// Command kipple is a single-user, self-hosted RSS reader.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// Load the IANA time zone database into the binary so TZ (e.g.
	// America/New_York) resolves without /usr/share/zoneinfo, which
	// distroless images don't have.
	_ "time/tzdata"

	"github.com/WPTK/kipple/internal/api"
	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/buildinfo"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/favicon"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/ftrun"
	"github.com/WPTK/kipple/internal/greader"
	"github.com/WPTK/kipple/internal/httpx"
	"github.com/WPTK/kipple/internal/imgcache"
	"github.com/WPTK/kipple/internal/lock"
	"github.com/WPTK/kipple/internal/maint"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/setup"
	"github.com/WPTK/kipple/internal/stats"
	"github.com/WPTK/kipple/internal/store"
	kweb "github.com/WPTK/kipple/internal/web"
)

// version, commit and buildDate are set at build time via -ldflags "-X main.version=..." (the Dockerfile
// passes them from its build args, since .git is not in the build context).
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// buildInfo is what this binary was built from.
func buildInfo() buildinfo.Info {
	return buildinfo.Info{Version: version, Commit: commit, BuildDate: buildDate}.Normalized()
}

func init() {
	// mime's built-in table (and /etc/mime.types, which distroless doesn't
	// have anyway) is missing these; without them the bundled fonts and PWA
	// manifest are served as application/octet-stream.
	for ext, typ := range map[string]string{
		".woff2":       "font/woff2",
		".woff":        "font/woff",
		".ttf":         "font/ttf",
		".webmanifest": "application/manifest+json",
	} {
		if err := mime.AddExtensionType(ext, typ); err != nil {
			panic(fmt.Sprintf("config: mime type %q: %v", ext, err))
		}
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kipple:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		return runServe()
	case "api-password":
		return runAPIPassword(args[1:])
	case "password":
		return runPassword(args[1:])
	case "restore":
		return runRestore(args[1:])
	case "import":
		return runImport(args[1:])
	case "healthcheck":
		return runHealthcheck(args[1:])
	case "version":
		return runVersion(args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown command %q (want serve, healthcheck, import, api-password, password, restore or version)", cmd)
	}
}

// setLocal makes loc the process's local time zone (time.Local). It is called
// only at start-up, before any goroutine that reads the clock exists: time.Local
// is a plain global that every time.Now reads. A zone with the same name as the
// current one is left alone, so a repeated start never writes the global again.
func setLocal(loc *time.Location) {
	if loc.String() == localZone().String() {
		return
	}
	writeLocalZone(loc)
}

// localZone and writeLocalZone read and write time.Local (seams for tests). The
// tests replace both with a fake (fakeLocalZone): one test binary runs runServe
// many times while goroutines of earlier tests still call time.Now, so a test
// may never write the real global (issue #165).
var (
	localZone      = func() *time.Location { return time.Local }
	writeLocalZone = func(loc *time.Location) { time.Local = loc }
)

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := ensureDataDir(cfg.DataDir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	// One server per data directory, and no restore under a live server: the OS
	// lock goes with the process, so a crash never leaves a stale one.
	dataLock, err := lock.Acquire(filepath.Join(cfg.DataDir, "kipple.lock"))
	if errors.Is(err, lock.ErrLocked) {
		return fmt.Errorf("another kipple is already running on %s (kipple.lock is held)", cfg.DataDir)
	}
	if err != nil {
		return fmt.Errorf("data dir lock: %w", err)
	}
	defer func() { _ = dataLock.Release() }()
	// A restore confirmed in the setup wizard is applied now, under the lock and
	// before the database opens (it is idempotent: an interrupted one finishes).
	if err := applyStagedRestore(cfg.DataDir, logger); err != nil {
		return err
	}
	// One shutdown budget (shutdown.go): started by the stop signal, drawn on by
	// every stage and by the deferred closes below.
	var budget shutdownBudget
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(cfg.DataDir, "kipple.db"), Logger: logger, Version: version})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	// db.Close (WAL checkpoint, pools) runs last, after the scheduler has drained.
	defer closeWithin(&budget, logger, "closing store", 0, db.Close)
	// A feed delete the last run did not finish (it was marked first, then purged in batches): finish it now.
	if ids, err := db.ResumeFeedDeletes(context.Background()); err != nil {
		logger.Warn("resuming interrupted feed deletes", "err", err)
	} else if len(ids) > 0 {
		logger.Info("finished interrupted feed deletes", "feeds", len(ids))
	}

	// The database is open and at this binary's schema: remember which release did that (the message of a later
	// refused downgrade and the About screen use it). A failure must never keep Kipple from starting.
	if err := db.RecordVersion(context.Background(), version); err != nil {
		logger.Warn("recording the running version", "err", err)
	}

	// The reachability settings, seeded from their variables the first time.
	reachLive, err := openReach(context.Background(), db, cfg, logger)
	if err != nil {
		return fmt.Errorf("reachability settings: %w", err)
	}

	if err := ensureAccount(context.Background(), db, cfg, logger); err != nil {
		return fmt.Errorf("account: %w", err)
	}
	// TZ only gives a new install its time zone setting; the setting owns the zone from then on.
	if err := db.SeedZone(context.Background(), cfg.TZ); err != nil {
		return fmt.Errorf("TZ: %w", err)
	}
	// Nothing but this goroutine runs yet (the pools keep no timers), so this is the
	// one safe moment: log timestamps follow the time zone setting as of this start;
	// a later change of the setting reaches them after a restart.
	setLocal(store.Zone(context.Background(), db.Reader()))
	// The background work (fetching, maintenance, icons) starts only once an
	// account exists: at once when there is one, else when the claim creates it.
	var startWork func()
	setupMgr, err := startSetupMode(context.Background(), db, cfg, logger, func() {
		// The account exists now: a reset's request to ignore the environment is spent.
		if err := setup.SetIgnoreEnvAccount(cfg.DataDir, false); err != nil {
			logger.Warn("cannot remove "+setup.NoEnvAccountFile, "err", err)
		}
		startWork()
	})
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	// The image cache is optional: if it cannot open (a read-only volume, say), the
	// proxy still works and streams every image straight from its source.
	imgc, err := imgcache.Open(imgcache.Options{
		Dir:      filepath.Join(cfg.DataDir, "imgcache"),
		MaxBytes: int64(db.IntSetting(context.Background(), "imgproxy.cache_mb", store.DefaultImgCacheMB)) << 20,
		Logger:   logger,
	})
	if err != nil {
		logger.Error("image cache unavailable; images stream uncached", "err", err)
		imgc = nil
	} else {
		// Runs before db.Close (defers unwind last-in first) and after the HTTP drain.
		defer closeWithin(&budget, logger, "closing image cache", storeCloseReserve, imgc.Close)
	}

	hub := events.New()
	// One verifier for the whole process: the web login and ClientLogin share its
	// single argon2id slot, so they can never hash at the same time.
	verifier := auth.NewVerifier(nil, auth.VerifierOptions{})
	client := fetch.NewClient(fetch.ClientOptions{Version: version, PublicURL: reachLive.PublicURL})
	// One full-text runner for the process: the ingest pool and the on-demand
	// endpoint join each other's extractions and share the per-article-host limit.
	ftRunner := ftrun.New(ftrun.Options{
		DB: db, Log: logger,
		Extractor: extract.New(extract.Options{Transport: client.Transport, UserAgent: client.DefaultUserAgent, Timeout: 15 * time.Second, Logger: logger}),
	})
	scheduler := sched.New(db, client, hub, nil, logger, sched.Options{
		Workers: cfg.FetchWorkers, PerHost: cfg.FetchPerHost, Tick: cfg.SchedTick, Runner: ftRunner,
	})
	maintenance := maint.New(maint.Options{DB: db, Logger: logger, ImgCache: imgc})
	// The favicon finder (design §4.11): one lookup at a time, off the fetch path,
	// through the same guarded transport, and never while a scheduler run is
	// active (Busy is a lock-free read that also reports busy once stopping).
	icons := favicon.New(favicon.Options{
		DB: db, Guard: client.Transport, UserAgent: client.DefaultUserAgent, Logger: logger,
		Busy: scheduler.Busy,
	})
	// Joined before the store closes on every return path (defers unwind last-in
	// first); idempotent, and immediate when it never started.
	defer closeWithin(&budget, logger, "stopping the favicon finder", storeCloseReserve, func() error { icons.Stop(); return nil })

	// The Reader API claims /api/greader.php and its root aliases ahead of the
	// mux, so no ServeMux ever sees a Reader path (design §6.1).
	recorder := stats.New(time.Now)
	readerAPI := greader.New(greader.Options{
		DB: db, Logger: logger, Wake: scheduler.Wake, Events: hub, Stats: recorder,
		Reach: reachLive, LogForms: cfg.LogGreaderForms,
		Verifier: verifier,
		FetchNow: func(ctx context.Context, feedID int64, wait time.Duration) {
			ch, err := scheduler.Submit(sched.Priority{FeedID: feedID, Full: true, Trigger: fetch.TriggerSubscribe})
			if err != nil {
				scheduler.Wake() // stopping; nothing more to do, and harmless if not
				return
			}
			t := time.NewTimer(wait)
			defer t.Stop()
			select {
			case <-ch:
			case <-t.C:
			case <-scheduler.Shutdown():
			case <-ctx.Done():
			}
		},
	})

	// The stop signal, or a confirmed restore (Options.Restart): both run the
	// same clean shutdown, and the restart policy starts Kipple again.
	sigCtx, stop := stopSignals()
	defer stop()
	ctx, restart := context.WithCancel(sigCtx)
	defer restart()

	tailnet := setup.TailnetCheck()
	_ = tailnet() // the first scan now, not on the first request
	openGate := setup.Gate{Trusted: reachLive.Trusted, Tailnet: tailnet}
	mux := http.NewServeMux()
	uiAPI := api.New(api.Options{
		DB: db, Sched: scheduler, Hub: hub, Logger: logger,
		Reach: reachLive, ReaderLastSeen: readerAPI.LastSeen, Verifier: verifier,
		Stats: recorder, Version: version, Build: buildInfo(), WebBuild: kweb.BuildID(), DataDir: cfg.DataDir, Guard: client.Transport, UserAgent: client.DefaultUserAgent, Runner: ftRunner, ImgCache: imgc,
		OnAPIPasswordChange: readerAPI.InvalidateAccount,
		Setup:               setupMgr,
		Gate:                openGate,
		EnvAccount:          cfg.Username != "" && cfg.Password != "",
		Restart: func() {
			logger.Info("stopping so the next start applies the restore or reset")
			restart()
		},
	})
	defer closeWithin(&budget, logger, "closing the UI API", storeCloseReserve, func() error { uiAPI.Close(); return nil })
	maintenance.SetOnAutoRead(uiAPI.PublishAutoRead) // the nightly auto-read step publishes through the API
	uiAPI.Register(mux)
	webHandler, err := newWebHandler(uiAPI.ImgMode)
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}
	mux.Handle("/", webHandler)
	// The background work starts only once every handler is built, so a failed
	// setup returns with nothing running (no fetch or maintenance racing the
	// deferred store close), and only with an account (see startSetupMode).
	startWork = func() { startBackground(scheduler, maintenance, icons) }
	if !setupMgr.Pending() {
		startWork()
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           rootHandler(readerAPI.Front, mux, uiAPI.ImgMode, reachLive.Trusted, openGate.TailscaleServeRequest, logger, uiAPI.HostGate),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // request only; SSE is a response stream
		// WriteTimeout would kill /api/events; the SSE handler replaces it with a
		// per-write deadline through http.ResponseController (internal/api/sse.go).
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		ln, err := listen(cfg.Addr)
		if err != nil {
			serveErr <- err
			return
		}
		logger.Info("listening", "addr", ln.Addr().String(), "version", version, "commit", buildInfo().Commit)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// Shutdown order (design §4.10): stop the scheduler and cancel the favicon
	// finder's lookup (it then writes nothing), close SSE, drain HTTP, wait for the
	// workers, join the finder and stop maintenance, then (deferred) checkpoint and
	// close the store, all inside one budget (shutdown.go).
	stopAll := func() error {
		return runShutdown(&budget, shutdownSteps{
			stopWork:  func() { scheduler.Stop(); icons.Cancel(); hub.Close() },
			drainHTTP: srv.Shutdown,
			cutHTTP:   func() { _ = srv.Close() },
			stopped:   scheduler.Stopped(),
			stopMaint: func() { icons.Stop(); maintenance.Stop(); client.CloseIdle() },
		}, logger)
	}

	return superviseServe(ctx, serveErr, stopAll, &budget, logger)
}

// applyStagedRestore applies a restore confirmed in the setup wizard or a reset
// confirmed in Settings (backup.ApplyStaged) and logs what it did.
func applyStagedRestore(dataDir string, logger *slog.Logger) error {
	done, err := backup.ApplyStaged(dataDir, time.Now(), localZone())
	if err != nil {
		return err
	}
	if done.Stale && done.StaleErr != nil {
		logger.Error("a restore or reset confirmed more than a week ago is not applied, but its marker could not be removed; delete restore-pending.json and restore-staged.db in the data folder",
			"confirmed_at", done.MarkerTime.UTC().Format(time.RFC3339), "err", done.StaleErr)
	} else if done.Stale {
		logger.Warn("discarded a restore or reset confirmed more than a week ago and never applied; nothing was changed",
			"confirmed_at", done.MarkerTime.UTC().Format(time.RFC3339))
	}
	if done.Restored {
		if err := backup.RecordRestoreGap(context.Background(), dataDir, time.Now()); err != nil {
			logger.Warn("the restore is applied; its statistics gap could not be recorded, so comparisons may read the days since the backup as quiet", "err", err)
		}
		logger.Info("applied the restore or reset confirmed in the browser; every web session was signed out",
			"username", done.Username, "backup_created_at", done.CreatedAt, "backup_kipple_version", done.KippleVersion,
			"previous_database", done.Pre)
	}
	return nil
}

// stopSignals is the context the stop signal (SIGINT, SIGTERM) cancels (a seam
// for tests, which stop runServe by cancelling it).
var stopSignals = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// newWebHandler builds the SPA handler (a seam for tests).
var newWebHandler = func(imgMode func() string) (http.Handler, error) {
	return kweb.NewHandler(kweb.WithImgMode(imgMode))
}

// startBackground starts the scheduler, maintenance and the favicon finder (a
// seam for tests).
var startBackground = func(s *sched.Scheduler, m *maint.Maint, icons *favicon.Finder) {
	s.Start()
	m.Start()
	icons.Start()
}

// rootHandler is the server's whole handler chain: the Reader API claims its
// paths ahead of the mux, untrusted forwarding headers are logged, and
// httpx.Secure puts the security headers (frame-ancestors, X-Frame-Options,
// CSP by content type, Permissions-Policy on pages) on every response, the SPA,
// the UI API, the Reader API and the image proxy alike. httpx.Compress, outermost, gzips the
// responses worth it (JSON, text, scripts) for clients that accept it.
//
// hostGate (the UI API's HostGate, design 5.2) runs inside httpx.Secure, so a
// refused request still carries the security headers; nil installs none.
func rootHandler(readerFront func(http.Handler) http.Handler, mux http.Handler, imgMode func() string,
	trusted func() []netip.Prefix, tailscaleServe func(*http.Request) bool, logger *slog.Logger, hostGate func(http.Handler) http.Handler) http.Handler {
	h := auth.WarnUntrustedProxyHeaders(readerFront(mux), trusted, tailscaleServe, logger, nil)
	if hostGate != nil {
		h = hostGate(h)
	}
	return httpx.Compress(httpx.Secure(h, httpx.Options{ImgMode: imgMode, TrustedProxies: trusted}))
}

// serveDrainWait bounds the wait for ListenAndServe to report after a shutdown.
var serveDrainWait = 5 * time.Second

// superviseServe waits for either the listener to fail or a signal, runs
// stopAll, and always reads serveErr so the serve goroutine is never left
// behind. A shutdown that a signal asked for is a normal exit (nil) even when
// the grace period ran out (that is only logged); a listener that failed is
// returned. The wait for the listener after a signal is capped by what is left
// of budget (less closeReserve), with a small floor; nil means serveDrainWait.
func superviseServe(ctx context.Context, serveErr <-chan error, stopAll func() error, budget *shutdownBudget, logger *slog.Logger) error {
	select {
	case err := <-serveErr:
		if stopErr := stopAll(); stopErr != nil {
			logger.Warn("shutdown after listener exit", "err", stopErr)
		}
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		if err := stopAll(); err != nil {
			logger.Warn("shutdown was not clean", "err", err)
		}
		wait := serveDrainWait
		if budget != nil {
			wait = max(budget.left(closeReserve, serveDrainWait), 100*time.Millisecond)
		}
		select {
		case err := <-serveErr:
			return err // nil after Shutdown/Close (http.ErrServerClosed is mapped to nil)
		case <-time.After(wait):
			logger.Warn("listener did not report after shutdown")
			return nil
		}
	}
}

// runVersion prints the version alone (its output is read by docs/RELEASING.md step 10, so it never
// changes), or with -v the full build info.
func runVersion(args []string, out io.Writer) error {
	switch {
	case len(args) == 0:
		_, err := fmt.Fprintln(out, version)
		return err
	case len(args) == 1 && (args[0] == "-v" || args[0] == "--verbose"):
		_, err := fmt.Fprint(out, buildInfo().Report(store.LatestVersion(), kweb.BuildID()))
		return err
	}
	return errors.New("usage: kipple version [-v]")
}
