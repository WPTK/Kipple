package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

// runImport implements `kipple import [flags] <file|->`. It opens the store like
// serve does but starts no scheduler and no listener, so it works with serve
// stopped or running (SQLite WAL + busy_timeout serialize the one write
// transaction). New feeds are inserted with next_fetch_at = now; the serve
// scheduler fetches them as ordinary due feeds on its next tick (design §4.9).
func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	days := fs.Int("mark-read-older-than-days", 0, "mark items older than N days (1-365) read on each new feed's first fetch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kipple import [-mark-read-older-than-days N] <file.opml | ->")
	}
	if *days < 0 || *days > 365 {
		return fmt.Errorf("-mark-read-older-than-days must be 0 or 1-365")
	}

	var in io.Reader = os.Stdin
	if p := fs.Arg(0); p != "-" {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		in = f
	}
	doc, err := opml.Parse(in)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err := ensureDataDir(cfg.DataDir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(cfg.DataDir, "kipple.db"), Logger: logger, NoMigrate: true, NoCheckpoint: true})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Warn("closing store", "err", err)
		}
	}()

	res, err := opml.Import(ctx, db, doc, opml.ImportOptions{MarkReadOlderThanDays: *days})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "imported %d new feeds (%d folders created, %d already present); the running server fetches them on its next tick\n",
		res.FeedsAdded, res.FoldersCreated, len(res.FeedsExisting))
	return nil
}
