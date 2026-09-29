package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/opml"
	"github.com/WPTK/kipple/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.CreateAccount(context.Background(), store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
	require.NoError(t, err)
	return db
}

// seed adds one feed and n items (every tenth starred).
func seed(t *testing.T, db *store.DB, n int) {
	t.Helper()
	ctx := context.Background()
	doc, err := opml.Parse(strings.NewReader(`<opml><body><outline text="Tech"><outline text="A" xmlUrl="http://a.test/rss"/></outline></body></opml>`))
	require.NoError(t, err)
	_, err = opml.Import(ctx, db, doc, opml.ImportOptions{})
	require.NoError(t, err)
	var feed int64
	require.NoError(t, db.Reader().QueryRow("SELECT id FROM feeds WHERE url = 'http://a.test/rss'").Scan(&feed))
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			id := int64(1_700_000_000_000_000) + int64(i)*1000
			star := 0
			if i%10 == 0 {
				star = 1
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, starred, published_at, sort_at, uid, content_hash, text_hash, url, title)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, id, feed, star, id/1_000_000, id/1_000_000, fmt.Sprintf("u%d", i), "c", "t",
				fmt.Sprintf("http://a.test/%d", i), fmt.Sprintf("Item %d", i)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (?,?,?)`,
				id, "<p>body</p>", strings.Repeat("body ", 50)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES('refresh.interval_minutes','45',1),('sys.last_nightly_date','"2026-09-24"',1)`)
		return err
	}))
	require.NoError(t, db.CreateSession(ctx, "sess1", 1, 4_000_000_000, "ua", "127.0.0.1"))
}

func newManager(t *testing.T, db *store.DB, tune ...func(*Options)) *Manager {
	t.Helper()
	o := Options{DB: db, Logger: quiet, Version: "test-1", FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }}
	for _, f := range tune {
		f(&o)
	}
	m := New(o)
	t.Cleanup(m.Close)
	return m
}

func exportFiles(t *testing.T, m *Manager) []string {
	t.Helper()
	ents, _ := os.ReadDir(m.o.Dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func readAll(t *testing.T, d *Download) []byte {
	t.Helper()
	b, err := io.ReadAll(d.File)
	require.NoError(t, err)
	return b
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestExportZipContentsAndIntegrity(t *testing.T) {
	db := openDB(t)
	seed(t, db, 200)
	m := newManager(t, db)

	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	require.Regexp(t, `^kipple-backup-\d{8}-\d{6}\.zip$`, exp.Filename)
	require.Len(t, exp.Token, 32)

	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	body := readAll(t, d)
	require.Equal(t, exp.Bytes, int64(len(body)))
	require.NoError(t, d.Close())
	require.Empty(t, exportFiles(t, m), "the file is deleted after the download")

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		files[f.Name] = b
	}
	require.ElementsMatch(t, []string{DBFile, OPMLFile, SettingsFile, ReadmeFile, ManifestFile}, keys(files))

	var mf Manifest
	require.NoError(t, json.Unmarshal(files[ManifestFile], &mf))
	require.Equal(t, "test-1", mf.KippleVersion)
	require.Equal(t, store.LatestVersion(), mf.SchemaVersion)
	require.EqualValues(t, store.ApplicationID, mf.ApplicationID)
	require.EqualValues(t, 1, mf.Feeds)
	require.EqualValues(t, 200, mf.Items)
	require.EqualValues(t, 20, mf.Starred)
	require.Equal(t, sum(files[DBFile]), mf.DBSHA256)
	require.EqualValues(t, len(files[DBFile]), mf.DBBytes)
	for _, e := range mf.Files {
		require.Equal(t, sum(files[e.Name]), e.SHA256, e.Name)
		require.EqualValues(t, len(files[e.Name]), e.Bytes, e.Name)
	}
	require.Contains(t, string(files[OPMLFile]), `xmlUrl="http://a.test/rss"`)
	var settings settingsDoc
	require.NoError(t, json.Unmarshal(files[SettingsFile], &settings))
	require.JSONEq(t, "45", string(settings.Settings["refresh.interval_minutes"]))
	require.NotContains(t, settings.Settings, "sys.last_nightly_date", "internal bookkeeping stays out of the readable copy")

	// The zip restores: extract, verify, inspect.
	zipPath := filepath.Join(t.TempDir(), "b.zip")
	require.NoError(t, os.WriteFile(zipPath, body, 0o600))
	out := filepath.Join(t.TempDir(), "out.db")
	mf2, err := ExtractDB(zipPath, out)
	require.NoError(t, err)
	require.Equal(t, mf.DBSHA256, mf2.DBSHA256)
	info, err := Inspect(context.Background(), out, true)
	require.NoError(t, err)
	require.EqualValues(t, 200, info.Items)
	require.EqualValues(t, 20, info.Starred)
}

// The export carries items.state_changed_at and its index, and a restored file opens as is.
func TestExportKeepsStateChangedAt(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	ctx := context.Background()
	const first = int64(1_700_000_000_000_000)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := store.SetRead(ctx, tx, []int64{first}, true, 1111); err != nil {
			return err
		}
		_, err := store.SetStarred(ctx, tx, []int64{first}, false, 2222) // item 0 is seeded starred
		return err
	}))
	m := newManager(t, db)
	exp, err := m.Create(ctx)
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	body := readAll(t, d)
	require.NoError(t, d.Close())
	zipPath := filepath.Join(t.TempDir(), "b.zip")
	require.NoError(t, os.WriteFile(zipPath, body, 0o600))
	out := filepath.Join(t.TempDir(), "out.db")
	_, err = ExtractDB(zipPath, out)
	require.NoError(t, err)
	_, err = Inspect(ctx, out, true)
	require.NoError(t, err)

	restored, err := store.Open(ctx, store.Options{Path: out, Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })
	v, err := restored.Version(ctx)
	require.NoError(t, err)
	require.Equal(t, store.LatestVersion(), v)
	var at int64
	require.NoError(t, restored.Reader().QueryRow("SELECT state_changed_at FROM items WHERE id = ?", first).Scan(&at))
	require.EqualValues(t, 2222, at)
	var n int
	require.NoError(t, restored.Reader().QueryRow("SELECT count(*) FROM items WHERE state_changed_at IS NOT NULL").Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, restored.Reader().QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'idx_items_state_changed'").Scan(&n))
	require.Equal(t, 1, n)
}

func keys(m map[string][]byte) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

func TestExtractRefusesDamage(t *testing.T) {
	db := openDB(t)
	seed(t, db, 20)
	m := newManager(t, db)
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	good := readAll(t, d)
	require.NoError(t, d.Close())

	write := func(name string, b []byte) string {
		p := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(p, b, 0o600))
		return p
	}

	// A rebuilt zip whose manifest lies about the database hash.
	zr, err := zip.NewReader(bytes.NewReader(good), int64(len(good)))
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == ManifestFile {
			var mf Manifest
			require.NoError(t, json.Unmarshal(b, &mf))
			mf.DBSHA256 = strings.Repeat("0", 64)
			for i := range mf.Files {
				if mf.Files[i].Name == DBFile {
					mf.Files[i].SHA256 = mf.DBSHA256
				}
			}
			b, _ = json.Marshal(mf)
		}
		w, _ := zw.Create(f.Name)
		_, _ = w.Write(b)
	}
	require.NoError(t, zw.Close())
	out := filepath.Join(t.TempDir(), "o.db")
	_, err = ExtractDB(write("bad-hash.zip", buf.Bytes()), out)
	require.ErrorContains(t, err, "checksum mismatch")
	_, statErr := os.Stat(out)
	require.True(t, os.IsNotExist(statErr), "a failed extract leaves no database behind")

	// Truncation and garbage.
	_, err = ExtractDB(write("trunc.zip", good[:len(good)/2]), filepath.Join(t.TempDir(), "o2.db"))
	require.Error(t, err)
	_, err = ExtractDB(write("junk.zip", []byte("not a zip at all")), filepath.Join(t.TempDir(), "o3.db"))
	require.Error(t, err)

	// A valid zip that is not a Kipple backup.
	var buf2 bytes.Buffer
	zw2 := zip.NewWriter(&buf2)
	w, _ := zw2.Create("hello.txt")
	_, _ = w.Write([]byte("hi"))
	require.NoError(t, zw2.Close())
	_, err = ExtractDB(write("other.zip", buf2.Bytes()), filepath.Join(t.TempDir(), "o4.db"))
	require.ErrorContains(t, err, "no manifest.json")

	// The good backup plus one entry with a path, listed in the manifest with a
	// correct checksum or not listed at all: refused either way (a Kipple backup
	// is flat), and nothing is extracted.
	for _, name := range []string{"../../evil.txt", "/abs.txt", `..\evil.txt`, "sub/kipple.db", "C:evil.txt"} {
		for _, listed := range []bool{true, false} {
			var buf3 bytes.Buffer
			zw3 := zip.NewWriter(&buf3)
			for _, f := range zr.File {
				rc, _ := f.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				if f.Name == ManifestFile && listed {
					var mf Manifest
					require.NoError(t, json.Unmarshal(b, &mf))
					sum := sha256.Sum256([]byte("owned"))
					mf.Files = append(mf.Files, FileEntry{Name: name, Bytes: 5, SHA256: hex.EncodeToString(sum[:])})
					b, _ = json.Marshal(mf)
				}
				w, _ := zw3.Create(f.Name)
				_, _ = w.Write(b)
			}
			w, _ := zw3.Create(name)
			_, _ = w.Write([]byte("owned"))
			require.NoError(t, zw3.Close())
			out := filepath.Join(t.TempDir(), "o5.db")
			_, err = ExtractDB(write("path.zip", buf3.Bytes()), out)
			require.ErrorContains(t, err, "not a plain file name", "%s listed=%v", name, listed)
			_, statErr := os.Stat(out)
			require.True(t, os.IsNotExist(statErr))
		}
	}
}

func TestExportConsistentUnderConcurrentWrites(t *testing.T) {
	db := openDB(t)
	seed(t, db, 500)
	m := newManager(t, db)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var written, worst atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Each write is one transaction inserting an item and its content, like a
		// fetch commit; a consistent snapshot never sees half of one.
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			began := time.Now()
			id := int64(1_800_000_000_000_000) + int64(i)*1000
			err := db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
					VALUES (?, (SELECT id FROM feeds LIMIT 1), 1, 1, ?, 'c', 't', 'w')`, id, fmt.Sprintf("w%d", i)); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (?, '<p>x</p>', 'x')`, id)
				return err
			})
			if err != nil {
				t.Errorf("write during export: %v", err)
				return
			}
			written.Add(1)
			if d := time.Since(began).Milliseconds(); d > worst.Load() {
				worst.Store(d)
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	exp, err := m.Create(context.Background())
	close(stop)
	wg.Wait()
	require.NoError(t, err)
	require.Positive(t, written.Load())
	// A writer was never stalled anywhere near the 10 s write deadline (generous under -race).
	require.Less(t, worst.Load(), int64(5000), "no write waited for the export")

	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "b.zip")
	require.NoError(t, os.WriteFile(p, readAll(t, d), 0o600))
	require.NoError(t, d.Close())
	out := filepath.Join(t.TempDir(), "o.db")
	_, err = ExtractDB(p, out)
	require.NoError(t, err)
	info, err := Inspect(context.Background(), out, true)
	require.NoError(t, err, "integrity_check, foreign_key_check and the FTS check pass")
	require.GreaterOrEqual(t, info.Items, int64(500))

	// Every item has its content row: no transaction was cut in half.
	chk, err := openFile(out)
	require.NoError(t, err)
	defer chk.Close()
	var orphans int
	require.NoError(t, chk.QueryRow("SELECT count(*) FROM items i WHERE NOT EXISTS (SELECT 1 FROM item_content c WHERE c.item_id = i.id)").Scan(&orphans))
	require.Zero(t, orphans)
}

func TestFreeSpaceRefusal(t *testing.T) {
	db := openDB(t)
	seed(t, db, 50)
	var asked string
	m := newManager(t, db, func(o *Options) {
		o.FreeBytes = func(dir string) (uint64, error) { asked = dir; return 1 << 20, nil }
	})
	_, err := m.Create(context.Background())
	var ns *NoSpaceError
	require.ErrorAs(t, err, &ns)
	require.Contains(t, err.Error(), "not enough free disk space")
	require.Greater(t, ns.Need, ns.Free)
	require.Equal(t, m.o.Dir, asked)
	require.Empty(t, exportFiles(t, m), "a refused export leaves nothing behind")

	// Need is 2.2x the database (plus slack): with exactly that free, it proceeds.
	need := db.DiskSize()*22/10 + spaceSlack
	m2 := newManager(t, db, func(o *Options) {
		o.FreeBytes = func(string) (uint64, error) { return uint64(need), nil }
	})
	_, err = m2.Create(context.Background())
	require.NoError(t, err)
}

func TestTooLargeIsRefused(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	m := newManager(t, db, func(o *Options) { o.MaxDBBytes = 1024 })
	_, err := m.Create(context.Background())
	require.ErrorIs(t, err, ErrTooLarge)
}

func TestTokenSingleUseAndExpiry(t *testing.T) {
	db := openDB(t)
	seed(t, db, 20)
	now := time.Now()
	m := newManager(t, db, func(o *Options) { o.Now = func() time.Time { return now }; o.TTL = time.Hour })

	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	require.Equal(t, now.Add(time.Hour).Unix(), exp.ExpiresAt.Unix())

	_, err = m.Take(strings.Repeat("0", 32))
	require.ErrorIs(t, err, ErrBadToken)
	_, err = m.Take("")
	require.ErrorIs(t, err, ErrBadToken)

	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	_, err = m.Take(exp.Token)
	require.ErrorIs(t, err, ErrBadToken, "single use, even while the first transfer is still open")
	require.NoError(t, d.Close())

	// Expiry: a token used after its TTL is refused and the file is dropped.
	exp2, err := m.Create(context.Background())
	require.NoError(t, err)
	now = now.Add(time.Hour + time.Second)
	_, err = m.Take(exp2.Token)
	require.ErrorIs(t, err, ErrBadToken)
}

func TestExpiryTimerDeletesTheFile(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	m := newManager(t, db, func(o *Options) { o.TTL = 100 * time.Millisecond })
	_, err := m.Create(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, exportFiles(t, m))
	require.Eventually(t, func() bool { return len(exportFiles(t, m)) == 0 }, 10*time.Second, 20*time.Millisecond)
}

func TestNewExportReplacesTheOldOne(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	m := newManager(t, db)
	a, err := m.Create(context.Background())
	require.NoError(t, err)
	b, err := m.Create(context.Background())
	require.NoError(t, err)
	require.Len(t, exportFiles(t, m), 1)
	_, err = m.Take(a.Token)
	require.ErrorIs(t, err, ErrBadToken)
	d, err := m.Take(b.Token)
	require.NoError(t, err)
	require.NoError(t, d.Close())
}

func TestOneExportAtATime(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	m := newManager(t, db)

	// The slot is held (as the nightly snapshot or another export would): busy, not queued.
	release, err := db.TrySnapshot()
	require.NoError(t, err)
	_, err = m.Create(context.Background())
	require.ErrorIs(t, err, ErrBusy)
	release()
	_, err = m.Create(context.Background())
	require.NoError(t, err)

	// Concurrent requests: every one either succeeds or is told busy; at least one succeeds.
	var wg sync.WaitGroup
	var ok, busy atomic.Int32
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Create(context.Background())
			switch {
			case err == nil:
				ok.Add(1)
			case err == ErrBusy:
				busy.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	require.Positive(t, ok.Load())
	require.EqualValues(t, 6, ok.Load()+busy.Load())
	require.Len(t, exportFiles(t, m), 1, "only the newest export is kept")
}

func TestNightlySnapshotWaitsForAnExport(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	release, err := db.TrySnapshot()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := db.WriteSnapshot(context.Background(), time.Now().Unix(), false)
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("the nightly snapshot ran while an export held the slot")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the nightly snapshot never ran")
	}
}

func TestStartupAndFailureCleanup(t *testing.T) {
	db := openDB(t)
	seed(t, db, 10)
	dir := filepath.Join(db.BackupDir(), "export")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, n := range []string{"export-1.db", "export-1.zip", "export-1.zip.partial"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("stale"), 0o600))
	}
	m := newManager(t, db)
	require.Empty(t, exportFiles(t, m), "New removes what a crash left")

	// A cancelled build leaves nothing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Create(ctx)
	require.Error(t, err)
	require.Empty(t, exportFiles(t, m))
	// and the slot was released
	_, err = m.Create(context.Background())
	require.NoError(t, err)
}

func TestInspectRefusesNewerSchemaAndForeignFiles(t *testing.T) {
	db := openDB(t)
	seed(t, db, 5)
	snap := filepath.Join(t.TempDir(), "s.db")
	release, err := db.TrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SnapshotTo(context.Background(), snap))
	release()

	f, err := openFile(snap)
	require.NoError(t, err)
	_, err = f.Exec(fmt.Sprintf("PRAGMA user_version = %d", store.LatestVersion()+1))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = Inspect(context.Background(), snap, true)
	require.ErrorContains(t, err, "newer than this Kipple binary")

	other := filepath.Join(t.TempDir(), "o.db")
	g, err := openFile(other)
	require.NoError(t, err)
	_, err = g.Exec("CREATE TABLE x (a)")
	require.NoError(t, err)
	require.NoError(t, g.Close())
	_, err = Inspect(context.Background(), other, true)
	require.ErrorContains(t, err, "not a Kipple database")
}

// The download name reads in the effective zone (TZ, else the tz setting, else
// UTC), whatever zone the clock's time carries.
func TestBackupFilenameFollowsTheEffectiveZone(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	at := time.Date(2026, 1, 15, 3, 30, 0, 0, time.UTC).In(time.FixedZone("elsewhere", 3*3600))
	m := newManager(t, db, func(o *Options) { o.Now = func() time.Time { return at } })
	name := func() string {
		t.Helper()
		exp, err := m.Create(ctx)
		require.NoError(t, err)
		return exp.Filename
	}
	require.Equal(t, "kipple-backup-20260115-033000.zip", name(), "UTC by default")
	require.NoError(t, db.SetSettings(ctx, map[string]any{"tz": "America/New_York"}))
	require.Equal(t, "kipple-backup-20260114-223000.zip", name())
	t.Cleanup(func() { _ = store.SetEnvZone("") })
	require.NoError(t, store.SetEnvZone("Asia/Tokyo"))
	require.Equal(t, "kipple-backup-20260115-123000.zip", name(), "TZ wins")
}

func TestPortSetting(t *testing.T) {
	ctx := context.Background()
	_, _, err := PortSetting(ctx, filepath.Join(t.TempDir(), "missing.db"))
	require.Error(t, err, "a missing file cannot be read")

	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := store.Open(ctx, store.Options{Path: path, Logger: quiet})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	installed, legacy, err := PortSetting(ctx, path)
	require.NoError(t, err)
	require.False(t, installed, "never set up: no installation to follow")
	require.False(t, legacy)

	db, err = store.Open(ctx, store.Options{Path: path, Logger: quiet})
	require.NoError(t, err)
	_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
	require.NoError(t, err)
	require.NoError(t, db.Close())
	installed, legacy, err = PortSetting(ctx, path)
	require.NoError(t, err)
	require.True(t, installed)
	require.False(t, legacy, "a 0.5 account without the flag")
	require.NoError(t, SetLegacyPort(ctx, path, true))
	_, legacy, _ = PortSetting(ctx, path)
	require.True(t, legacy)
	require.NoError(t, SetLegacyPort(ctx, path, false))
	_, legacy, _ = PortSetting(ctx, path)
	require.False(t, legacy)

	// An older database with an account is legacy: 0010 will stamp it.
	raw, err := openFile(path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, "PRAGMA user_version = 9")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	installed, legacy, err = PortSetting(ctx, path)
	require.NoError(t, err)
	require.True(t, installed)
	require.True(t, legacy)
}

// The live database is read with its committed WAL, and nothing is created
// beside it.
func TestPortSettingReadsTheWALReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := store.Open(ctx, store.Options{Path: path, Logger: quiet})
	require.NoError(t, err)
	_, err = db.CreateAccount(ctx, store.Account{Username: "owner", PasswordHash: "h", Secret: testSecret})
	require.NoError(t, err)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('sys.legacy_port', 'true')`)
		return err
	}))
	// While the store is still open the flag may live only in the WAL.
	installed, legacy, err := PortSetting(ctx, path)
	require.NoError(t, err)
	require.True(t, installed)
	require.True(t, legacy, "the committed WAL is read")
	require.NoError(t, db.Close())
}
