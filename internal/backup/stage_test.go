package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func newRestorer(t *testing.T, tune ...func(*RestorerOptions)) (*Restorer, string) {
	t.Helper()
	dir := t.TempDir()
	o := RestorerOptions{DataDir: dir, Logger: quiet, FreeBytes: func(string) (uint64, error) { return 1 << 40, nil }}
	for _, f := range tune {
		f(&o)
	}
	r := NewRestorer(o)
	t.Cleanup(r.Close)
	return r, dir
}

func upload(r *Restorer, b []byte) (Upload, error) {
	return r.Upload(context.Background(), bytes.NewReader(b), int64(len(b)))
}

// hostBackup is a real backup whose database holds the address settings and a
// trusted proxy, and a web session.
func hostBackup(t *testing.T) []byte {
	t.Helper()
	db := openDB(t)
	seed(t, db, 7)
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES
			('server.public_url', '"https://old.example.com"', 1),
			('security.allowed_hosts', '["old.example.com"]', 1),
			('security.trusted_proxies', '["10.0.0.1/32"]', 1)`)
		return err
	}))
	m := newManager(t, db)
	exp, err := m.Create(context.Background())
	require.NoError(t, err)
	d, err := m.Take(exp.Token)
	require.NoError(t, err)
	defer d.Close()
	return readAll(t, d)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func rawQuery(t *testing.T, path, q string) string {
	t.Helper()
	db, err := openFile(path)
	require.NoError(t, err)
	defer db.Close()
	var v sql.NullString
	err = db.QueryRow(q).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "<none>"
	}
	require.NoError(t, err)
	return v.String
}

// The whole wizard path: upload stages a verified copy and deletes the zip,
// confirm edits only the staged copy and writes the marker last, the next start
// swaps it in and keeps the old database.
func TestUploadConfirmApply(t *testing.T) {
	r, dir := newRestorer(t)
	up, err := upload(r, hostBackup(t))
	require.NoError(t, err)
	require.Equal(t, KindBackup, up.Kind)
	require.Equal(t, "test-1", up.Manifest.KippleVersion)
	require.EqualValues(t, 1, up.Feeds)
	require.EqualValues(t, 7, up.Info.Items)
	require.EqualValues(t, 1, up.Info.Starred)
	require.Equal(t, BackupAccount{Username: "owner", HasPassword: true}, up.Account)
	require.Equal(t, 30, up.EstimateSeconds, "a small backup gets the minimum")
	require.Equal(t, RestoreUploaded, r.State())
	require.False(t, exists(filepath.Join(dir, UploadFile)), "the zip is deleted at once")
	require.True(t, exists(filepath.Join(dir, StagedFile)))
	require.False(t, exists(filepath.Join(dir, MarkerFile)), "nothing is confirmed yet")

	_, ticket, ok := r.Uploaded()
	require.True(t, ok)
	require.NoError(t, r.Confirm(context.Background(), ticket, "new-hash"))
	require.Equal(t, RestoreConfirmed, r.State())
	staged := filepath.Join(dir, StagedFile)
	require.True(t, exists(filepath.Join(dir, MarkerFile)))
	require.Equal(t, "0", rawQuery(t, staged, "SELECT count(*) FROM sessions"), "every session is signed out")
	require.Equal(t, "<none>", rawQuery(t, staged, "SELECT value FROM settings WHERE key = 'server.public_url'"))
	require.Equal(t, "<none>", rawQuery(t, staged, "SELECT value FROM settings WHERE key = 'security.allowed_hosts'"))
	require.Equal(t, "<none>", rawQuery(t, staged, "SELECT value FROM settings WHERE key = 'security.trusted_proxies'"))
	require.Equal(t, "45", rawQuery(t, staged, "SELECT value FROM settings WHERE key = 'refresh.interval_minutes'"), "other settings stay")
	require.Equal(t, "new-hash", rawQuery(t, staged, "SELECT password_hash FROM account"))
	require.Equal(t, "standard", rawQuery(t, staged, "SELECT auth_mode FROM account"))

	// Once confirmed, nothing else is accepted.
	_, err = upload(r, hostBackup(t))
	require.ErrorIs(t, err, ErrRestorePending)
	require.ErrorIs(t, r.Cancel(), ErrRestorePending)
	_, err = r.Feeds()
	require.ErrorIs(t, err, ErrRestorePending)
	require.ErrorIs(t, r.Confirm(context.Background(), ticket, ""), ErrRestorePending)

	// The next start: the empty database of setup mode is kept, the backup installed.
	live := filepath.Join(dir, "kipple.db")
	require.NoError(t, os.WriteFile(live, []byte("empty"), 0o600))
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	done, err := ApplyStaged(dir, now, time.UTC)
	require.NoError(t, err)
	require.True(t, done.Restored)
	require.Equal(t, "owner", done.Username)
	require.Equal(t, filepath.Join(dir, "backup", "pre-restore-20261006-120000Z"), done.Pre)
	require.FileExists(t, filepath.Join(done.Pre, "kipple.db"))
	require.Equal(t, "7", rawQuery(t, live, "SELECT count(*) FROM items"))
	for _, f := range []string{MarkerFile, StagedFile, UploadFile} {
		require.NoFileExists(t, filepath.Join(dir, f))
	}
	// Run again (a second start): nothing to do.
	done, err = ApplyStaged(dir, now, time.UTC)
	require.NoError(t, err)
	require.False(t, done.Restored)
	require.Equal(t, "7", rawQuery(t, live, "SELECT count(*) FROM items"))
}

func TestConfirmWithoutNewPasswordKeepsTheAccount(t *testing.T) {
	r, dir := newRestorer(t)
	_, err := upload(r, hostBackup(t))
	require.NoError(t, err)
	_, ticket, _ := r.Uploaded()
	require.NoError(t, r.Confirm(context.Background(), ticket, ""))
	require.Equal(t, "h", rawQuery(t, filepath.Join(dir, StagedFile), "SELECT password_hash FROM account"))
}

func TestUploadKinds(t *testing.T) {
	r, dir := newRestorer(t)

	// A bare OPML file: counted, nothing kept, no upload state.
	up, err := upload(r, []byte(`<?xml version="1.0"?>
<!-- exported -->
<opml version="2.0"><body><outline text="A" xmlUrl="http://a.test/rss"/><outline text="F"><outline xmlUrl="http://b.test/rss"/></outline></body></opml>`))
	require.NoError(t, err)
	require.Equal(t, Upload{Kind: KindOPML, Feeds: 2}, up)
	require.Equal(t, RestoreNone, r.State())

	// A bare database file is not accepted here, nor any other file.
	db := openDB(t)
	seed(t, db, 1)
	snap := filepath.Join(t.TempDir(), "s.db")
	rel, err := db.TrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SnapshotTo(context.Background(), snap))
	rel()
	raw, err := os.ReadFile(snap)
	require.NoError(t, err)
	for name, b := range map[string][]byte{
		"sqlite": raw,
		"text":   []byte("hello, this is not a backup"),
		"html":   []byte("<html><body>no</body></html>"),
		"empty":  {},
	} {
		_, err := upload(r, b)
		require.ErrorIs(t, err, ErrNotBackup, name)
		require.Equal(t, "This is not a Kipple backup zip or an OPML file.", err.Error())
	}

	// An OPML file too large for an import.
	big := append([]byte("<opml><body>"), bytes.Repeat([]byte(" "), maxFeedsOPML)...)
	_, err = upload(r, big)
	require.ErrorIs(t, err, ErrOPMLTooLarge)

	// A file over the cap is refused before it is read.
	r2, dir2 := newRestorer(t, func(o *RestorerOptions) { o.MaxBytes = 100 })
	_, err = r2.Upload(context.Background(), strings.NewReader(strings.Repeat("x", 101)), 101)
	require.ErrorIs(t, err, ErrUploadTooLarge)
	require.NoFileExists(t, filepath.Join(dir2, UploadFile))

	// A body shorter than announced.
	_, err = r.Upload(context.Background(), strings.NewReader("PK\x03\x04short"), 1000)
	require.ErrorIs(t, err, ErrUploadCut)

	for _, f := range []string{UploadFile, StagedFile, MarkerFile} {
		require.NoFileExists(t, filepath.Join(dir, f))
	}
	require.Equal(t, RestoreNone, r.State())
}

// Free space is checked against about four times the file before a byte is
// stored.
func TestUploadSpaceCheck(t *testing.T) {
	b := hostBackup(t)
	var asked string
	r, dir := newRestorer(t, func(o *RestorerOptions) {
		o.FreeBytes = func(d string) (uint64, error) { asked = d; return uint64(len(b)) * 3, nil }
	})
	_, err := upload(r, b)
	var space *UploadSpaceError
	require.ErrorAs(t, err, &space)
	require.Equal(t, int64(len(b))*4+16<<20, space.Need)
	require.Contains(t, err.Error(), "Not enough free disk space to restore this backup")
	require.Equal(t, dir, asked)
	require.NoFileExists(t, filepath.Join(dir, UploadFile), "nothing was written")

	// Unknown free space does not block.
	r, _ = newRestorer(t, func(o *RestorerOptions) {
		o.FreeBytes = func(string) (uint64, error) { return 0, errors.New("statfs") }
	})
	_, err = upload(r, b)
	require.NoError(t, err)
}

// zipEntries reads every entry of a zip.
func zipEntries(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		body, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		out[f.Name] = body
	}
	return out
}

// rebuilt takes a real backup, changes its database with sql, and zips it again
// with a manifest that matches (what someone crafting a file would do).
func rebuilt(t *testing.T, b []byte, change string) []byte {
	t.Helper()
	ents := zipEntries(t, b)
	p := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(p, ents[DBFile], 0o600))
	db, err := openFile(p)
	require.NoError(t, err)
	_, err = db.Exec(change)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	nb, err := os.ReadFile(p)
	require.NoError(t, err)
	var mf Manifest
	require.NoError(t, json.Unmarshal(ents[ManifestFile], &mf))
	return goodZip(nb, map[string][]byte{OPMLFile: ents[OPMLFile]})
}

func TestUploadRefusesAnExtraOrChangedSchemaObject(t *testing.T) {
	b := hostBackup(t)
	for name, change := range map[string]string{
		"trigger":         "CREATE TRIGGER evil AFTER INSERT ON sessions BEGIN DELETE FROM items; END",
		"view":            "CREATE VIEW evil AS SELECT 1",
		"table":           "CREATE TABLE evil (a)",
		"index":           "CREATE INDEX evil ON items(title)",
		"changed trigger": "DROP TRIGGER folders_keep_default; CREATE TRIGGER folders_keep_default BEFORE DELETE ON folders BEGIN DELETE FROM items; END",
	} {
		r, dir := newRestorer(t)
		_, err := upload(r, rebuilt(t, b, change))
		var bad *BadUploadError
		require.ErrorAs(t, err, &bad, name)
		want := "which Kipple never creates"
		if name == "changed trigger" {
			want = `a changed trigger "folders_keep_default"`
		}
		require.Contains(t, err.Error(), want, name)
		require.NoFileExists(t, filepath.Join(dir, StagedFile), name)
		require.Equal(t, RestoreNone, r.State(), name)
	}
	// The same rebuild without a change passes: the check is not the zip's doing.
	r, _ := newRestorer(t)
	_, err := upload(r, rebuilt(t, b, "SELECT 1"))
	require.NoError(t, err)
}

func TestCheckSchemaMatchesAFreshDatabase(t *testing.T) {
	db := openDB(t)
	snap := filepath.Join(t.TempDir(), "s.db")
	rel, err := db.TrySnapshot()
	require.NoError(t, err)
	require.NoError(t, db.SnapshotTo(context.Background(), snap))
	rel()
	raw, err := openFile(snap)
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, CheckSchema(context.Background(), raw, store.LatestVersion()))
	// At an older version the newer objects are extras.
	err = CheckSchema(context.Background(), raw, 1)
	require.ErrorContains(t, err, "which Kipple never creates")
}

func TestUploadRefusesTooManyEntries(t *testing.T) {
	extra := map[string][]byte{}
	for i := 0; i < MaxEntries; i++ {
		extra[fmt.Sprintf("f%d.txt", i)] = []byte("x")
	}
	r, dir := newRestorer(t)
	_, err := upload(r, goodZip([]byte("not read"), extra))
	var bad *BadUploadError
	require.ErrorAs(t, err, &bad)
	require.Contains(t, err.Error(), "holds 12 files")
	require.NoFileExists(t, filepath.Join(dir, StagedFile))
}

func TestUploadRefusesANewerKipple(t *testing.T) {
	b := hostBackup(t)
	// The manifest says so: refused before anything is extracted.
	ents := zipEntries(t, b)
	var mf Manifest
	require.NoError(t, json.Unmarshal(ents[ManifestFile], &mf))
	mf.SchemaVersion = store.LatestVersion() + 1
	mf.KippleVersion = "9.1.0"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range ents {
		if name == ManifestFile {
			body, _ = json.Marshal(mf)
		}
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	r, _ := newRestorer(t)
	_, err := upload(r, buf.Bytes())
	var newer *NewerError
	require.ErrorAs(t, err, &newer)
	require.Equal(t, "This backup was made by a newer Kipple (9.1.0). Update Kipple first.", err.Error())

	// The database says so although the manifest does not.
	_, err = upload(r, rebuilt(t, b, fmt.Sprintf("PRAGMA user_version = %d", store.LatestVersion()+1)))
	require.ErrorAs(t, err, &newer)
	require.NotContains(t, strings.ToLower(err.Error()), "schema")
}

// One intent at a time: a second upload while one arrives or waits is busy; a
// cancel stops the one arriving and frees the slot.
func TestUploadRaces(t *testing.T) {
	b := hostBackup(t)
	r, dir := newRestorer(t)
	pr, pw := io.Pipe()
	first := make(chan error, 1)
	go func() {
		_, err := r.Upload(context.Background(), pr, int64(len(b)))
		first <- err
	}()
	_, err := pw.Write(b[:100]) // the first upload is now reading
	require.NoError(t, err)
	_, err = upload(r, b)
	require.ErrorIs(t, err, ErrRestoreBusy)
	require.Contains(t, err.Error(), "Cancel it first")
	require.Equal(t, RestoreNone, r.State(), "an upload still arriving is not an upload yet")

	cancelled := make(chan error, 1)
	go func() { cancelled <- r.Cancel() }()
	go func() { _, _ = pw.Write(b[100:]); _ = pw.Close() }() // the client keeps sending
	require.ErrorIs(t, <-first, context.Canceled)
	require.NoError(t, <-cancelled)
	require.NoFileExists(t, filepath.Join(dir, UploadFile))
	require.NoFileExists(t, filepath.Join(dir, StagedFile))

	_, err = upload(r, b)
	require.NoError(t, err)
	_, err = upload(r, b)
	require.ErrorIs(t, err, ErrRestoreBusy, "an upload waiting for a confirm is busy too")

	// A confirm holding a ticket of an upload that was replaced does nothing.
	_, ticket, _ := r.Uploaded()
	require.NoError(t, r.Cancel())
	require.NoError(t, r.Cancel(), "idempotent")
	_, err = upload(r, b)
	require.NoError(t, err)
	require.ErrorIs(t, r.Confirm(context.Background(), ticket, ""), ErrNoUpload)
	require.NoFileExists(t, filepath.Join(dir, MarkerFile))

	// Drop (an account was created) clears it without waiting.
	r.Drop()
	require.Equal(t, RestoreNone, r.State())
	require.NoFileExists(t, filepath.Join(dir, StagedFile))
	require.ErrorIs(t, r.Confirm(context.Background(), ticket, ""), ErrNoUpload)
}

func TestUnconfirmedUploadExpires(t *testing.T) {
	r, dir := newRestorer(t, func(o *RestorerOptions) { o.TTL = 50 * time.Millisecond })
	_, err := upload(r, hostBackup(t))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return r.State() == RestoreNone }, 5*time.Second, 10*time.Millisecond)
	require.NoFileExists(t, filepath.Join(dir, StagedFile))
}

func TestFeedsOnlyTakesTheZipsOPML(t *testing.T) {
	b := hostBackup(t)
	r, dir := newRestorer(t)
	_, err := r.Feeds()
	require.ErrorIs(t, err, ErrNoUpload)
	_, err = upload(r, b)
	require.NoError(t, err)
	got, err := r.Feeds()
	require.NoError(t, err)
	require.Equal(t, zipEntries(t, b)[OPMLFile], got)
	require.Contains(t, string(got), "http://a.test/rss")
	require.Equal(t, RestoreNone, r.State(), "the upload is discarded")
	require.NoFileExists(t, filepath.Join(dir, StagedFile))
}

// Every state a crash can leave is finished or cleaned at the next start.
func TestApplyStagedIsIdempotent(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	write := func(p, body string) { require.NoError(t, os.WriteFile(p, []byte(body), 0o600)) }
	read := func(p string) string {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("marker without staged file: already done", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "restored")
		write(filepath.Join(dir, MarkerFile), `{"username":"owner"}`)
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.False(t, done.Restored)
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.Equal(t, "restored", read(filepath.Join(dir, "kipple.db")))
		require.NoDirExists(t, filepath.Join(dir, "backup"))
	})

	t.Run("staged file without marker: never confirmed", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "live")
		write(filepath.Join(dir, StagedFile), "staged")
		write(filepath.Join(dir, StagedFile+"-journal"), "j")
		write(filepath.Join(dir, UploadFile), "zip")
		write(filepath.Join(dir, MarkerFile+".tmp"), "half a marker")
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.False(t, done.Restored)
		require.Equal(t, "live", read(filepath.Join(dir, "kipple.db")))
		for _, f := range []string{StagedFile, StagedFile + "-journal", UploadFile, MarkerFile + ".tmp"} {
			require.NoFileExists(t, filepath.Join(dir, f))
		}
	})

	t.Run("crash between the swap and the marker removal", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "kipple.db"), "empty")
		write(filepath.Join(dir, "kipple.db-wal"), "wal")
		write(filepath.Join(dir, StagedFile), "staged")
		write(filepath.Join(dir, MarkerFile), `{}`)
		pre, err := Swap(dir, filepath.Join(dir, StagedFile), now) // the swap ran, then the power went
		require.NoError(t, err)
		done, err := ApplyStaged(dir, now.Add(time.Minute), time.UTC)
		require.NoError(t, err)
		require.False(t, done.Restored, "nothing left to swap")
		require.Equal(t, "staged", read(filepath.Join(dir, "kipple.db")))
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.Equal(t, "empty", read(filepath.Join(pre, "kipple.db")), "the old database is still kept")
		dirs, _ := filepath.Glob(filepath.Join(dir, "backup", "pre-restore-*"))
		require.Len(t, dirs, 1, "no second safety copy of the restored database")
	})

	t.Run("crash in the middle of the swap", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, StagedFile), "staged")
		write(filepath.Join(dir, MarkerFile), `{}`)
		// The live database was moved away and the staged one not yet renamed.
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.True(t, done.Restored)
		require.Empty(t, done.Pre)
		require.Equal(t, "staged", read(filepath.Join(dir, "kipple.db")))
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
	})

	t.Run("nothing at all", func(t *testing.T) {
		dir := t.TempDir()
		done, err := ApplyStaged(dir, now, time.UTC)
		require.NoError(t, err)
		require.Equal(t, Applied{}, done)
	})

	t.Run("DiscardStaged drops a confirmed restore", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, StagedFile), "staged")
		write(filepath.Join(dir, MarkerFile), `{}`)
		require.True(t, DiscardStaged(dir))
		require.NoFileExists(t, filepath.Join(dir, StagedFile))
		require.NoFileExists(t, filepath.Join(dir, MarkerFile))
		require.False(t, DiscardStaged(dir))
	})
}

func TestEstimateSeconds(t *testing.T) {
	require.Equal(t, 30, EstimateSeconds(0, true))
	require.Equal(t, 32, EstimateSeconds(2<<30, false))
	require.Equal(t, 47, EstimateSeconds(2<<30, true))
}

// The texts a person restoring reads never mention schema numbers.
func TestRestoreTextsSayNoSchemaVersion(t *testing.T) {
	for _, e := range []error{ErrRestoreBusy, ErrRestorePending, ErrNoUpload, ErrNotBackup, ErrUploadTooLarge, ErrOPMLTooLarge,
		ErrUploadCut, &NewerError{}, &NewerError{KippleVersion: "1.2"}, &UploadSpaceError{Need: 1, Free: 0}} {
		require.NotContains(t, strings.ToLower(e.Error()), "schema", e.Error())
	}
	require.NotContains(t, strings.ToLower(restoreText), "schema")
}
