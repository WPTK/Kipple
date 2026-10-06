package backup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// ServerSettingPrefixes is the one list of settings that describe this server
// and not the library: the server.* and security.* keys (public address,
// allowed host names, trusted proxies, Cloudflare Access) and the marker that
// the allowed host names of KIPPLE_ALLOWED_HOSTS were merged once (without it a
// host removed in Settings would come back from the variable). A reset and a
// wizard restore both replace the staged copy's value for every key in it with
// the live database's, so Kipple answers at the same address and a backup's
// server settings never carry over. An entry matches a key it is a prefix of.
var ServerSettingPrefixes = []string{"server.", "security.", store.SettingAllowedHostsMerged}

// stageMu orders resets: one intent at a time, in this process.
var stageMu sync.Mutex

// StageReset is the confirm of a reset: it creates a fresh database (this
// binary's current layout, no account) in the data directory, copies the server
// settings from live into it, and writes the restore marker. The next start
// applies it exactly as it applies a restore from the setup wizard, so the
// library is kept in backup/pre-restore-* and Kipple starts empty, in setup mode.
// ErrRestorePending when a restore or reset is already waiting (two requests at
// once give one winner). username names the account being erased, for the log.
func StageReset(ctx context.Context, dataDir string, live *sql.DB, kippleVersion, username string, now time.Time) error {
	stageMu.Lock()
	defer stageMu.Unlock()
	if MarkerPending(dataDir) {
		return ErrRestorePending
	}
	removeStaged(dataDir)
	staged := filepath.Join(dataDir, StagedFile)
	ok := false
	defer func() {
		if !ok {
			removeStaged(dataDir)
		}
	}()
	if err := createStaged(ctx, staged, live, kippleVersion); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	if err := writeMarker(filepath.Join(dataDir, MarkerFile), marker{
		KippleVersion: kippleVersion, CreatedAt: now.UTC().Format(time.RFC3339), Username: username,
	}, true); err != nil {
		return err
	}
	ok = true
	return nil
}

// createStaged writes the fresh database at path and closes it into one file.
func createStaged(ctx context.Context, path string, live *sql.DB, kippleVersion string) error {
	fresh, err := store.Open(ctx, store.Options{Path: path, Version: kippleVersion})
	if err != nil {
		return err
	}
	err = copyServerSettings(ctx, fresh, live)
	if cerr := fresh.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	// One file, as a staged restore is: no -wal or -shm may carry data.
	db, err := openFile(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("the staged database stayed in %s mode", mode)
	}
	return nil
}

type settingRow struct{ key, value string }

// readServerSettings reads every setting under ServerSettingPrefixes from live.
func readServerSettings(ctx context.Context, live *sql.DB) ([]settingRow, error) {
	var rows []settingRow
	for _, p := range ServerSettingPrefixes {
		rs, err := live.QueryContext(ctx, "SELECT key, value FROM settings WHERE substr(key, 1, ?) = ?", len(p), p)
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var r settingRow
			if err := rs.Scan(&r.key, &r.value); err != nil {
				_ = rs.Close()
				return nil, err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			_ = rs.Close()
			return nil, err
		}
		_ = rs.Close()
	}
	return rows, nil
}

// writeServerSettings makes the server settings of tx's database exactly rows:
// every key of ServerSettingPrefixes is deleted, then rows are stored. The one
// helper of a reset (into a fresh database) and of a restore (into the staged
// backup).
func writeServerSettings(ctx context.Context, tx *sql.Tx, rows []settingRow) error {
	if err := clearServerSettings(ctx, tx); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", r.key, r.value); err != nil {
			return err
		}
	}
	return nil
}

// clearServerSettings deletes every key of ServerSettingPrefixes.
func clearServerSettings(ctx context.Context, tx *sql.Tx) error {
	for _, p := range ServerSettingPrefixes {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE substr(key, 1, ?) = ?", len(p), p); err != nil {
			return err
		}
	}
	return nil
}

func copyServerSettings(ctx context.Context, fresh *store.DB, live *sql.DB) error {
	rows, err := readServerSettings(ctx, live)
	if err != nil {
		return err
	}
	return fresh.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error { return writeServerSettings(ctx, tx, rows) })
}
