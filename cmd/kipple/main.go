// Command kipple is a single-user, self-hosted RSS reader.
package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/WPTK/kipple/internal/stats"
	"github.com/WPTK/kipple/internal/store"
	kweb "github.com/WPTK/kipple/internal/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

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
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want serve, healthcheck, import, api-password, password, restore or version)", cmd)
	}
}

// applyTZ makes tz (an IANA name, resolved from the embedded tzdata) the
// process's local time zone.
func applyTZ(tz string) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("TZ %q: %w", tz, err)
	}
	time.Local = loc
	return nil
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := applyTZ(cfg.TZ); err != nil {
		return err
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
	// One shutdown budget (shutdown.go): started by the stop signal, drawn on by
	// every stage and by the deferred closes below.
	var budget shutdownBudget
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(cfg.DataDir, "kipple.db"), Logger: logger})
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

	if err := ensureAccount(context.Background(), db, cfg, logger); err != nil {
		return fmt.Errorf("account: %w", err)
	}
	accessV, err := accessVerifier(cfg, logger)
	if err != nil {
		return fmt.Errorf("access: %w", err)
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
	client := fetch.NewClient(fetch.ClientOptions{Version: version, PublicURL: cfg.PublicURL})
	// One full-text runner for the process: the ingest pool and the on-demand
	// endpoint join each other's extractions and share the per-article-host limit.
	ftRunner := ftrun.New(ftrun.Options{
		DB: db, Log: logger,
		Extractor: extract.New(extract.Options{Transport: client.Transport, UserAgent: client.DefaultUserAgent(), Timeout: 15 * time.Second, Logger: logger}),
	})
	scheduler := sched.New(db, client, hub, nil, logger, sched.Options{
		Workers: cfg.FetchWorkers, PerHost: cfg.FetchPerHost, Tick: cfg.SchedTick, Runner: ftRunner,
	})
	maintenance := maint.New(maint.Options{DB: db, Logger: logger, ImgCache: imgc})
	// The favicon finder (design §4.11): one lookup at a time, off the fetch path,
	// through the same guarded transport, and never while a scheduler run is
	// active (Busy is a lock-free read that also reports busy once stopping).
	icons := favicon.New(favicon.Options{
		DB: db, Guard: client.Transport, UserAgent: client.DefaultUserAgent(), Logger: logger,
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
		TrustedProxies: cfg.TrustedProxyIPs, PublicURL: cfg.PublicURL, LogForms: cfg.LogGreaderForms,
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

	mux := http.NewServeMux()
	uiAPI := api.New(api.Options{
		DB: db, Sched: scheduler, Hub: hub, Logger: logger,
		TrustedProxies: cfg.TrustedProxyIPs, Clients: readerAPI.LastSeen, Verifier: verifier,
		Stats: recorder, Version: version, PublicURL: cfg.PublicURL, Guard: client.Transport, UserAgent: client.DefaultUserAgent(), Runner: ftRunner, ImgCache: imgc,
		OnAPIPasswordChange: readerAPI.InvalidateAccount, Access: accessV,
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
	// deferred store close).
	startBackground(scheduler, maintenance, icons)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           rootHandler(readerAPI.Front, mux, uiAPI.ImgMode, cfg.TrustedProxyIPs, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // request only; SSE is a response stream
		// WriteTimeout would kill /api/events; the SSE handler replaces it with a
		// per-write deadline through http.ResponseController (internal/api/sse.go).
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
			stopMaint: func() { icons.Stop(); maintenance.Stop() },
		}, logger)
	}

	return superviseServe(ctx, serveErr, stopAll, &budget, logger)
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
// the UI API, the Reader API and the image proxy alike.
func rootHandler(readerFront func(http.Handler) http.Handler, mux http.Handler, imgMode func() string,
	trusted []netip.Addr, logger *slog.Logger) http.Handler {
	return httpx.Secure(
		auth.WarnUntrustedProxyHeaders(readerFront(mux), trusted, logger, nil),
		httpx.Options{ImgMode: imgMode, TrustedProxies: trusted})
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
