package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// A pre-restore copy taken after an unclean stop has its newest transactions in
// the -wal beside it. Undoing a restore from it must bring them back.
func TestRestoreFromABareDatabaseAppliesItsWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
	require.NoError(t, err)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host, title) VALUES ('http://a.test/rss', 'a.test/rss', 'a.test', 'A')`)
		return err
	}))
	// Flush what exists so far into the main file; the items below stay in the WAL.
	_, err = db.Reader().Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	require.NoError(t, err)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < 5; i++ {
			id := int64(1_700_000_000_000_000) + int64(i)*1000
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
				VALUES (?, (SELECT id FROM feeds LIMIT 1), 1, 1, ?, 'c', 't', 'x')`, id, fmt.Sprintf("u%d", i)); err != nil {
				return err
			}
		}
		return nil
	}))

	// Copy the files as an unclean stop leaves them: main file plus a live WAL.
	crash := t.TempDir()
	for _, s := range []string{"", "-wal"} {
		b, err := os.ReadFile(filepath.Join(dir, "kipple.db"+s))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(crash, "kipple.db"+s), b, 0o600))
	}
	st, err := os.Stat(filepath.Join(crash, "kipple.db-wal"))
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(0), "the fixture must have an un-checkpointed WAL")
	require.NoError(t, db.Close())

	target := t.TempDir()
	out, err := doRestore(target, filepath.Join(crash, "kipple.db"), true)
	require.NoError(t, err, out)
	require.Contains(t, out, "-wal was applied")
	require.Equal(t, 5, countItems(t, target), "the transactions that lived only in the WAL are kept")
	require.NoFileExists(t, filepath.Join(target, "restore-tmp.db-wal"))
}

// Two restores within one second must not share a pre-restore directory.
func TestPreRestoreDirectoriesNeverCollide(t *testing.T) {
	dir := newData(t, 7)
	zipPath := export(t, newData(t, 10))
	_, err := doRestore(dir, zipPath, true)
	require.NoError(t, err)
	require.Equal(t, 10, countItems(t, dir))
	// the second restore (same fake second) replaces a database that is now the restored one
	other := newData(t, 3)
	zip2 := export(t, other)
	out, err := doRestore(dir, zip2, true)
	require.NoError(t, err, out)
	require.Equal(t, 3, countItems(t, dir))

	first := filepath.Join(dir, "backup", "pre-restore-20260925-120000Z")
	second := filepath.Join(dir, "backup", "pre-restore-20260925-120000Z-2")
	require.Contains(t, out, second)
	require.Equal(t, 7, scalar(t, first, "SELECT count(*) FROM items"), "the first pre-restore copy was not overwritten")
	require.Equal(t, 10, scalar(t, second, "SELECT count(*) FROM items"), "the second holds what the second restore replaced")
}
