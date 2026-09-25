package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrSnapshotBusy means a snapshot (the nightly one or a backup export) is
// already running: one at a time, so two big VACUUM INTOs never compete for disk.
var ErrSnapshotBusy = errors.New("store: another snapshot is running")

// acquireSnapshot waits for the snapshot slot (nightly path).
func (d *DB) acquireSnapshot(ctx context.Context) (release func(), err error) {
	select {
	case d.snap <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-d.snap }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TrySnapshot takes the snapshot slot without waiting; ErrSnapshotBusy when it
// is held. The backup export holds it for its whole build, so the nightly
// snapshot and an export never overlap. Call release exactly once.
func (d *DB) TrySnapshot() (release func(), err error) {
	select {
	case d.snap <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-d.snap }) }, nil
	default:
		return nil, ErrSnapshotBusy
	}
}

// SnapshotTo writes a consistent copy of the database to path with VACUUM INTO
// on the snapshot pool. That holds only a read snapshot, so the writer and the
// commit gate are never blocked (fetch commits carry on). The caller must hold
// the slot from TrySnapshot; path must not exist.
func (d *DB) SnapshotTo(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("store: snapshot target %s already exists", path)
	}
	snap, err := d.openSnapshot()
	if err != nil {
		return fmt.Errorf("store: open snapshot pool: %w", err)
	}
	defer snap.Close()
	if _, err := snap.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(filepath.ToSlash(path), "'", "''")+"'"); err != nil {
		return fmt.Errorf("store: vacuum into %s: %w", path, err)
	}
	return nil
}

// DiskSize is the size in bytes of the database file plus its WAL, which is
// what a snapshot could at most need.
func (d *DB) DiskSize() int64 {
	var n int64
	for _, s := range []string{"", "-wal"} {
		if st, err := os.Stat(d.path + s); err == nil {
			n += st.Size()
		}
	}
	return n
}

// ResetPassword is the recovery path of `kipple password`: it replaces the web
// password hash, rotates the account secret (which revokes every Reader API
// token, since the token is an HMAC keyed by it) and deletes every session,
// all in one transaction. The Reader API password is left as it is.
func (d *DB) ResetPassword(ctx context.Context, hash, newSecret string) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE account SET password_hash = ?, secret = ?, updated_at = unixepoch() WHERE id = 1", hash, newSecret)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("store: no account row")
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM sessions")
		return err
	})
}
