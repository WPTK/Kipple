package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedLedger inserts a ledger row (and optionally its restore stub) directly.
func seedLedger(t *testing.T, db *DB, feed, id, trimmedAt, lastSeen int64, stub bool) {
	t.Helper()
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			VALUES (?1, ?2, 'u' || ?1, 0, ?3, ?4)`, id, feed, trimmedAt, lastSeen); err != nil {
			return err
		}
		if !stub {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash,
			url, title, author, content_html, content_text) VALUES (?1, 1, 1, 1, 'c', 't', 'https://a/x', 'T', '', '<p>x</p>', 'x')`, id)
		return err
	}))
}

func seedFeed(t *testing.T, db *DB) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES ('https://a/f','a/f','a') RETURNING id`).Scan(&id)
	}))
	return id
}

const day = int64(86400)

func TestRestoreHonoursRestoreDaysAndReportsOnlyInserted(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	feed := seedFeed(t, db)
	now := int64(1_800_000_000)
	seedLedger(t, db, feed, 1001, now-10*day, now, true)  // inside 90 days
	seedLedger(t, db, feed, 1002, now-100*day, now, true) // stub not purged yet, but outside the window
	seedLedger(t, db, feed, 1003, now-10*day, now, true)  // will collide with a live items row

	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, uid, published_at, sort_at, content_hash, text_hash, url, title, author)
			VALUES (1003, ?1, 'other', 1, 1, 'c', 't', 'https://a/y', 'T', '')`, feed)
		return err
	}))

	var res StateResult
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		res, err = SetStarred(ctx, tx, []int64{1001, 1002, 1003}, true, now)
		return err
	}))
	require.Equal(t, []int64{1001}, res.Restored, "only the in-window id that was really inserted")
	require.NotContains(t, res.Changed, int64(1002))
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE id = 1001 AND starred = 1"))
	require.Equal(t, 0, scalar[int](t, db.Reader(), "SELECT count(*) FROM items WHERE id = 1002"))
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM trimmed_items WHERE id = 1002"), "the old ledger row is left alone")

	// Unread restores obey the same window.
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		res, err = SetRead(ctx, tx, []int64{1002}, false, now)
		return err
	}))
	require.Empty(t, res.Restored)
}

func TestPurgeBatchesRespectCutoffs(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	feed := seedFeed(t, db)
	now := int64(1_800_000_000)
	seedLedger(t, db, feed, 1, now-91*day, now, true)           // stub expired (ledger row stays)
	seedLedger(t, db, feed, 2, now-89*day, now, true)           // stub kept
	seedLedger(t, db, feed, 3, now-200*day, now-181*day, true)  // stub and then ledger both expire
	seedLedger(t, db, feed, 4, now-200*day, now-179*day, false) // ledger kept

	n, err := db.PurgeStubs(ctx, now, 100)
	require.NoError(t, err)
	require.EqualValues(t, 2, n) // ids 1 and 3
	require.Equal(t, 4, scalar[int](t, db.Reader(), "SELECT count(*) FROM trimmed_items"))
	n, err = db.PurgeLedger(ctx, now, 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, []int64{1, 2, 4}, func() []int64 {
		var out []int64
		rows, err := db.Reader().QueryContext(ctx, "SELECT id FROM trimmed_items ORDER BY id")
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		return out
	}())
	require.Equal(t, 1, scalar[int](t, db.Reader(), "SELECT count(*) FROM trimmed_content"))
}

func TestCheckpointPassiveAndOptimize(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	seedFeed(t, db)
	_, err := db.CheckpointPassive(ctx)
	require.NoError(t, err)
	require.NoError(t, db.Optimize(ctx))
}

func TestWriteSnapshotRecordsAndSurvivesLeftoverTmp(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	seedFeed(t, db)
	// A leftover tmp from an interrupted run must not stop the next snapshot.
	require.NoError(t, os.MkdirAll(db.BackupDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(db.BackupDir(), snapshotTmp), []byte("junk"), 0o644))

	path, err := db.WriteSnapshot(ctx, 1_800_000_000, true)
	require.NoError(t, err)
	require.FileExists(t, path)
	require.NoFileExists(t, filepath.Join(db.BackupDir(), snapshotTmp))
	require.Equal(t, "1800000000", scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'sys.last_snapshot_at'"))

	// Cancelled: nothing recorded, no tmp left, the good snapshot untouched.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = db.WriteSnapshot(cctx, 1_800_100_000, false)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(db.BackupDir(), snapshotTmp))
	require.FileExists(t, path)
	require.Equal(t, "1800000000", scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = 'sys.last_snapshot_at'"))
}
