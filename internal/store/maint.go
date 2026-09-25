package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// The functions here are the SQL half of the maintenance goroutine (design
// §2.6, package internal/maint). Every purge is one bounded batch: it takes
// the commit gate, then one short WithWrite, so a night's purge is many small
// writes that fetch commits and edit-tags interleave with, never one long one.

const (
	// LedgerDays is how long a trimmed ledger row (tombstone) outlives the last
	// time its uid was seen in the feed document (design §5).
	LedgerDays = 180

	// SnapshotName is the nightly snapshot a host backup copies (design §2.6).
	SnapshotName = "kipple-snapshot.db"
	snapshotTmp  = "kipple-snapshot.tmp"
)

// BackupDir returns the directory for the nightly and pre-migration snapshots.
func (d *DB) BackupDir() string { return d.backupDir }

// batch runs fn in one write transaction behind the commit gate.
func (d *DB) batch(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) (int64, error)) (int64, error) {
	release, err := d.AcquireGate(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var n int64
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		n, err = fn(ctx, tx)
		return err
	})
	return n, err
}

// PurgeStubs deletes up to limit restore stubs whose ledger row was trimmed
// more than retention.restore_days before now. The ledger row stays as a
// tombstone. It returns the rows deleted.
func (d *DB) PurgeStubs(ctx context.Context, now int64, limit int) (int64, error) {
	return d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		cutoff := now - int64(LoadFetchSettings(ctx, tx).RestoreDays)*86400
		res, err := tx.ExecContext(ctx, `DELETE FROM trimmed_content WHERE id IN (
			SELECT c.id FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id
			WHERE t.trimmed_at < ?1 LIMIT ?2)`, cutoff, limit)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// PurgeLedger deletes up to limit ledger rows (and, by cascade, any stub) whose
// uid was last seen in the feed document more than LedgerDays before now.
func (d *DB) PurgeLedger(ctx context.Context, now int64, limit int) (int64, error) {
	return d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM trimmed_items WHERE id IN (
			SELECT id FROM trimmed_items WHERE last_seen_at < ?1 LIMIT ?2)`, now-LedgerDays*86400, limit)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// PurgeSessions deletes up to limit expired sessions.
func (d *DB) PurgeSessions(ctx context.Context, now int64, limit int) (int64, error) {
	return d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id IN (
			SELECT id FROM sessions WHERE expires_at < ?1 LIMIT ?2)`, now, limit)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// Optimize runs PRAGMA optimize on the writer.
func (d *DB) Optimize(ctx context.Context) error {
	_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		_, err := tx.ExecContext(ctx, "PRAGMA optimize")
		return 0, err
	})
	return err
}

// CheckpointPassive runs PRAGMA wal_checkpoint(PASSIVE) on the writer (design
// §2.6 hourly). It never waits for readers; it returns the frames checkpointed.
func (d *DB) CheckpointPassive(ctx context.Context) (int64, error) {
	release, err := d.AcquireGate(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	// wal_checkpoint cannot run inside a transaction ("database table is
	// locked"), so it takes the writer's only connection directly: that is the
	// same exclusion WithWrite uses, so it still queues behind, and ahead of,
	// other writers.
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	conn, err := d.writer.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: checkpoint: %w", err)
	}
	defer conn.Close()
	var busy, logFrames, frames int64
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &frames); err != nil {
		return 0, fmt.Errorf("store: checkpoint: %w", err)
	}
	return frames, nil
}

// WriteSnapshot writes the nightly snapshot (design §2.6): VACUUM INTO a tmp
// file on the snapshot pool (interrupted by ctx), an optional FTS integrity
// check of the tmp file, fsync, atomic rename to SnapshotName, then record
// sys.last_snapshot_at (or sys.last_snapshot_error). A cancelled ctx removes
// the tmp file and records nothing. It returns the snapshot path.
func (d *DB) WriteSnapshot(ctx context.Context, now int64, integrity bool) (string, error) {
	final := filepath.Join(d.backupDir, SnapshotName)
	// Wait for an export in progress rather than skip the night: it is short.
	release, aerr := d.acquireSnapshot(ctx)
	if aerr != nil {
		return final, aerr
	}
	defer release()
	err := d.writeSnapshot(ctx, final, integrity)
	if ctx.Err() != nil {
		return final, ctx.Err()
	}
	if rerr := d.recordSnapshot(ctx, now, err); rerr != nil {
		err = errors.Join(err, fmt.Errorf("store: record snapshot: %w", rerr))
	}
	return final, err
}

func (d *DB) writeSnapshot(ctx context.Context, final string, integrity bool) (err error) {
	if err := ensureDir(d.backupDir); err != nil {
		return fmt.Errorf("store: backup dir: %w", err)
	}
	tmp := filepath.Join(d.backupDir, snapshotTmp)
	cleanup := func() {
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(tmp + s)
		}
	}
	cleanup() // a leftover from an interrupted run (VACUUM INTO refuses an existing target)
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	snap, err := d.openSnapshot()
	if err != nil {
		return fmt.Errorf("store: open snapshot pool: %w", err)
	}
	_, err = snap.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(filepath.ToSlash(tmp), "'", "''")+"'")
	snap.Close()
	if err != nil {
		return fmt.Errorf("store: vacuum into snapshot: %w", err)
	}

	if integrity {
		if err := snapshotFTSCheck(ctx, tmp); err != nil {
			return err
		}
	}

	f, err := os.OpenFile(tmp, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("store: open snapshot for sync: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("store: fsync snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("store: publish snapshot: %w", err)
	}
	return nil
}

// snapshotFTSCheck runs the FTS5 integrity-check against a snapshot file through a
// throwaway connection, never the live database.
func snapshotFTSCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+escapePath(path)+"?"+url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(DELETE)"}}.Encode())
	if err != nil {
		return fmt.Errorf("store: open snapshot for integrity check: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "INSERT INTO items_fts(items_fts) VALUES('integrity-check')"); err != nil {
		return fmt.Errorf("store: fts integrity-check: %w", err)
	}
	return nil
}

func (d *DB) recordSnapshot(ctx context.Context, now int64, snapErr error) error {
	_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
		const upsert = `INSERT INTO settings(key, value, updated_at) VALUES(?1, ?2, ?3)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
		if snapErr != nil {
			_, err := tx.ExecContext(ctx, upsert, "sys.last_snapshot_error", jsonString(snapErr.Error()), now)
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, upsert, "sys.last_snapshot_at", fmt.Sprint(now), now); err != nil {
			return 0, err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = 'sys.last_snapshot_error'")
		return 0, err
	})
	return err
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TZName is the `tz` setting as written (default America/New_York), without
// resolving it: the caller decides what an unknown name means.
func TZName(ctx context.Context, q Querier) string {
	return settingString(ctx, q, "tz", "America/New_York")
}

// NightlyDate is the local calendar date (YYYY-MM-DD, in the zone that was
// current then) of the last completed nightly run, or "" when there is none
// (sys.last_nightly_date).
func NightlyDate(ctx context.Context, q Querier) string {
	return settingString(ctx, q, "sys.last_nightly_date", "")
}

// RecordNightlyDate stores the local date of a nightly run that has started.
func (d *DB) RecordNightlyDate(ctx context.Context, date string, now int64) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('sys.last_nightly_date', ?1, ?2)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, jsonString(date), now)
		return err
	})
}
