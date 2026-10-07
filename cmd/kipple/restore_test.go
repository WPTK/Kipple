package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/lock"
	"github.com/WPTK/kipple/internal/store"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newData makes a data directory holding a live database with an account, a
// session and n items, and closes it (no server running).
func newData(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	ctx := context.Background()
	_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "old-hash", Secret: testSecret})
	require.NoError(t, err)
	require.NoError(t, db.CreateSession(ctx, "s1", 1, 4_000_000_000, "ua", "127.0.0.1"))
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host, title) VALUES ('http://a.test/rss', 'a.test/rss', 'a.test', 'A')`); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			id := int64(1_700_000_000_000_000) + int64(i)*1000
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
				VALUES (?, (SELECT id FROM feeds LIMIT 1), 1, 1, ?, 'c', 't', ?)`, id, fmt.Sprintf("u%d", i), fmt.Sprintf("Item %d", i)); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, db.Close())
	return dir
}

func scalar(t *testing.T, dir, query string) int {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog, NoMigrate: true, NoCheckpoint: true})
	require.NoError(t, err)
	defer db.Close()
	var n int
	require.NoError(t, db.Reader().QueryRow(query).Scan(&n))
	return n
}

func countItems(t *testing.T, dir string) int { return scalar(t, dir, "SELECT count(*) FROM items") }
func countSessions(t *testing.T, dir string) int {
	return scalar(t, dir, "SELECT count(*) FROM sessions")
}

// export runs the real export path against dir's database and returns the zip.
func export(t *testing.T, dir string) string {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	defer db.Close()
	m := backup.New(backup.Options{DB: db, Logger: quietLog, Version: "t", FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }})
	defer m.Close()
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	defer d.Close()
	p := filepath.Join(t.TempDir(), exp.Filename)
	b, err := io.ReadAll(d.File)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}

func doRestore(dir, src string, yes bool) (string, error) {
	var out bytes.Buffer
	err := restore(context.Background(), restoreOptions{DataDir: dir, Src: src, Yes: yes, Out: &out,
		Now: func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }})
	return out.String(), err
}

func TestRestoreRoundTripKeepsThePreRestoreCopy(t *testing.T) {
	dir := newData(t, 40)
	zipPath := export(t, dir)

	// The data changes after the backup: 40 items become 6 (an accident).
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM items WHERE id > 1700000000005000")
		return err
	}))
	require.NoError(t, db.Close())
	require.Equal(t, 6, countItems(t, dir))

	// Without --yes it verifies and changes nothing.
	out, err := doRestore(dir, zipPath, false)
	require.ErrorIs(t, err, errNotConfirmed)
	require.Contains(t, out, "40 items")
	require.Equal(t, 6, countItems(t, dir))
	require.NoFileExists(t, filepath.Join(dir, "restore-tmp.db"))

	out, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)
	require.Contains(t, out, "checksums verified")
	require.Contains(t, out, "start Kipple")
	require.Contains(t, out, "To undo")
	require.Equal(t, 40, countItems(t, dir), "the backup's data is back")
	require.Equal(t, 0, countSessions(t, dir), "restore signs every session out")

	pre := filepath.Join(dir, "backup", "pre-restore-20260925-120000Z")
	require.FileExists(t, filepath.Join(pre, "kipple.db"))
	require.Contains(t, out, pre)
	require.NoFileExists(t, filepath.Join(dir, "restore-tmp.db"))
	require.NoFileExists(t, filepath.Join(dir, "kipple.db-wal"), "no stale WAL of the old database next to the new one")

	// The pre-restore copy is itself restorable (the undo).
	out, err = doRestore(dir, filepath.Join(pre, "kipple.db"), true)
	require.NoError(t, err)
	require.Contains(t, out, "bare database file")
	require.Equal(t, 6, countItems(t, dir))

	// The restored database serves: it opens with the normal (migrating) open.
	db, err = store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	acc, ok, err := db.Account(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "owner", acc.Username)
	require.NoError(t, db.Close())
}

func TestRestoreIntoAnEmptyDataDir(t *testing.T) {
	src := newData(t, 12)
	zipPath := export(t, src)
	empty := t.TempDir()
	out, err := doRestore(empty, zipPath, true)
	require.NoError(t, err)
	require.Contains(t, out, "no previous library")
	require.NotContains(t, out, "To undo", "there is no pre-restore directory to undo from")
	require.Equal(t, 12, countItems(t, empty))
}

func TestRestoreRefusesWhileTheServerHoldsTheLock(t *testing.T) {
	dir := newData(t, 5)
	zipPath := export(t, dir)
	held, err := lock.Acquire(filepath.Join(dir, "kipple.lock")) // what serve does
	require.NoError(t, err)
	_, err = doRestore(dir, zipPath, true)
	require.ErrorContains(t, err, "is running")
	require.ErrorContains(t, err, "kipple.lock")
	require.Equal(t, 5, countItems(t, dir), "nothing changed")
	require.NoDirExists(t, filepath.Join(dir, "backup", "pre-restore-20260925-120000Z"))

	require.NoError(t, held.Release())
	_, err = doRestore(dir, zipPath, true)
	require.NoError(t, err)
}

func TestServeRefusesWhenTheLockIsHeld(t *testing.T) {
	// runServe applies TZ to a fake time.Local, never the real one (#165).
	z := fakeLocalZone(t, time.UTC)
	dir := t.TempDir()
	t.Setenv("KIPPLE_DATA", dir)
	t.Setenv("TZ", "UTC")
	held, err := lock.Acquire(filepath.Join(dir, "kipple.lock"))
	require.NoError(t, err)
	defer held.Release()
	err = runServe()
	require.ErrorContains(t, err, "already running")
	_, statErr := os.Stat(filepath.Join(dir, "kipple.db"))
	require.True(t, os.IsNotExist(statErr), "a refused serve never touched the database")
	require.Zero(t, z.writes, "runServe left the unchanged zone alone")
}

// corruptDBEntry copies the zip with one byte of kipple.db's uncompressed content flipped and
// everything else (manifest, sizes, checksums) untouched.
func corruptDBEntry(t *testing.T, zipPath string) string {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer zr.Close()
	out := filepath.Join(t.TempDir(), "bad.zip")
	f, err := os.Create(out)
	require.NoError(t, err)
	defer f.Close()
	zw := zip.NewWriter(f)
	flipped := false
	for _, e := range zr.File {
		rc, err := e.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		if e.Name == backup.DBFile {
			body[len(body)/2] ^= 0xFF
			flipped = true
		}
		w, err := zw.Create(e.Name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.True(t, flipped, "the backup has a kipple.db entry")
	require.NoError(t, zw.Close())
	return out
}

func TestRestoreRefusesBadInput(t *testing.T) {
	dir := newData(t, 5)
	zipPath := export(t, dir)

	// A newer schema than this binary: a bare snapshot with user_version bumped.
	newer := filepath.Join(t.TempDir(), "newer.db")
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(dir, "kipple.db"), Logger: quietLog})
	require.NoError(t, err)
	rel, err := db.TrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SnapshotTo(context.Background(), newer))
	rel()
	require.NoError(t, db.Close())
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(newer))
	require.NoError(t, err)
	_, err = raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", store.LatestVersion()+1))
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	_, err = doRestore(dir, newer, true)
	require.ErrorContains(t, err, "made by a newer Kipple")

	// A damaged backup: kipple.db rewritten with one byte of its decompressed content changed and
	// the manifest's checksum left stale. (Flipping a raw byte of the zip is not deterministic: it
	// can land in a compressed stream without changing what it decompresses to.)
	bad := corruptDBEntry(t, zipPath)
	_, err = doRestore(dir, bad, true)
	require.ErrorContains(t, err, "checksum")
	// A truncated zip is refused too.
	b, err := os.ReadFile(zipPath)
	require.NoError(t, err)
	trunc := filepath.Join(t.TempDir(), "trunc.zip")
	require.NoError(t, os.WriteFile(trunc, b[:len(b)/2], 0o600))
	_, err = doRestore(dir, trunc, true)
	require.Error(t, err)

	// Not a backup at all, and the live database itself.
	junk := filepath.Join(t.TempDir(), "junk.txt")
	require.NoError(t, os.WriteFile(junk, []byte(strings.Repeat("x", 100)), 0o600))
	_, err = doRestore(dir, junk, true)
	require.ErrorContains(t, err, "neither")
	_, err = doRestore(dir, filepath.Join(dir, "kipple.db"), true)
	require.ErrorContains(t, err, "live database itself")

	require.Equal(t, 5, countItems(t, dir), "every refusal left the live database alone")
	require.NoFileExists(t, filepath.Join(dir, "restore-tmp.db"))
	require.NoDirExists(t, filepath.Join(dir, "backup", "pre-restore-20260925-120000Z"))
}

// Restores a month apart: only the newest KeepPreRestore copies stay (copies from
// the last KeepPreRestoreFor are kept whatever their number, internal/backup).
func TestPreRestoreDirsArePruned(t *testing.T) {
	dir := newData(t, 3)
	zipPath := export(t, dir)
	for i := 0; i < 5; i++ {
		var out bytes.Buffer
		require.NoError(t, restore(context.Background(), restoreOptions{DataDir: dir, Src: zipPath, Yes: true, Out: &out,
			Now: func() time.Time {
				return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC).Add(time.Duration(i) * (backup.KeepPreRestoreFor + time.Hour))
			}}))
	}
	dirs, err := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
	require.NoError(t, err)
	require.Len(t, dirs, backup.KeepPreRestore)
}

func TestRunRestoreArgs(t *testing.T) {
	require.ErrorContains(t, runRestore(nil), "usage")
	require.ErrorContains(t, runRestore([]string{"a.zip", "b.zip"}), "usage")
	require.ErrorContains(t, runRestore([]string{"--force", "a.zip"}), "unknown option")
	require.ErrorContains(t, runRestore([]string{"-", "-"}), "usage")

	// The documented stdin form: a lone "-" is the source, not an option.
	for _, args := range [][]string{{"-"}, {"-", "--yes"}, {"--yes", "-"}} {
		src, yes, err := parseRestoreArgs(args)
		require.NoError(t, err, args)
		require.Equal(t, "-", src, args)
		require.Equal(t, len(args) == 2, yes, args)
	}
}

func TestRestoreFromStandardInput(t *testing.T) {
	src := newData(t, 9)
	zipPath := export(t, src)
	f, err := os.Open(zipPath)
	require.NoError(t, err)
	defer f.Close()

	dir := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, restore(context.Background(), restoreOptions{DataDir: dir, Src: "-", Yes: true, In: f, Out: &out, Now: time.Now}))
	require.Contains(t, out.String(), "standard input")
	require.Equal(t, 9, countItems(t, dir))
	require.NoFileExists(t, filepath.Join(dir, "restore-upload.tmp"), "the spool is removed")
}
