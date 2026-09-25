// Command kipple is a single-user, self-hosted RSS reader.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
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
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/greader"
	"github.com/WPTK/kipple/internal/sched"
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
	case "import":
		return runImport(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want serve, import, api-password or version)", cmd)
	}
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(cfg.DataDir, "kipple.db"), Logger: logger})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	// db.Close (WAL checkpoint, pools) runs last, after the scheduler has drained.
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error("closing store", "err", err)
		}
	}()

	if err := ensureAccount(context.Background(), db, cfg, logger); err != nil {
		return fmt.Errorf("account: %w", err)
	}

	hub := events.New()
	client := fetch.NewClient(fetch.ClientOptions{Version: version, PublicURL: cfg.PublicURL})
	scheduler := sched.New(db, client, hub, nil, logger, sched.Options{
		Workers: cfg.FetchWorkers, PerHost: cfg.FetchPerHost, Tick: cfg.SchedTick,
	})
	scheduler.Start()

	// The Reader API claims /api/greader.php and its root aliases ahead of the
	// mux, so no ServeMux ever sees a Reader path (design §6.1).
	readerAPI := greader.New(greader.Options{
		DB: db, Logger: logger, Wake: scheduler.Wake, Events: hub,
		TrustedProxies: cfg.TrustedProxyIPs, PublicURL: cfg.PublicURL, LogForms: cfg.LogGreaderForms,
	})

	webHandler, err := kweb.NewHandler()
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}

	mux := http.NewServeMux()
	api.New(api.Options{
		DB: db, Sched: scheduler, Hub: hub, Logger: logger,
		TrustedProxies: cfg.TrustedProxyIPs, Clients: readerAPI.LastSeen,
	}).Register(mux)
	mux.Handle("/", webHandler)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           readerAPI.Front(mux),
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

	// Shutdown order (design §4.10): stop the scheduler, close SSE, drain HTTP,
	// wait for the workers, then (deferred) checkpoint and close the store.
	stopAll := func() error {
		scheduler.Stop()
		hub.Close()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutErr := srv.Shutdown(shutdownCtx)
		select {
		case <-scheduler.Stopped():
		case <-time.After(15 * time.Second):
			logger.Error("scheduler did not drain in time")
		}
		if shutErr != nil {
			return fmt.Errorf("shutdown: %w", shutErr)
		}
		return nil
	}

	select {
	case err := <-serveErr:
		_ = stopAll()
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		if err := stopAll(); err != nil {
			return err
		}
		return <-serveErr
	}
}
