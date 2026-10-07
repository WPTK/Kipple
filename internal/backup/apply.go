package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// openUntrusted opens a database that came from outside: no -wal or -shm
// beside it, and trusted_schema off, so a trigger or view in it cannot call a
// function with side effects.
func openUntrusted(path string) (*sql.DB, error) {
	return openDSN(path, url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(DELETE)", "trusted_schema(0)"}})
}

// checkStaged verifies an extracted database: a Kipple database of a version
// this binary knows, holding nothing a fresh database of that version does not
// (CheckSchema), passing the integrity checks, with an account row.
func checkStaged(ctx context.Context, path, kippleVersion string) (DBInfo, BackupAccount, error) {
	db, err := openUntrusted(path)
	if err != nil {
		return DBInfo{}, BackupAccount{}, err
	}
	defer db.Close()
	bad := func(err error) (DBInfo, BackupAccount, error) {
		var newer *NewerError
		if errors.As(err, &newer) {
			newer.KippleVersion = kippleVersion
			return DBInfo{}, BackupAccount{}, newer
		}
		return DBInfo{}, BackupAccount{}, &BadUploadError{err}
	}
	var appID, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return bad(fmt.Errorf("kipple.db is not a readable database: %w", err))
	}
	if appID != store.ApplicationID {
		return bad(errors.New("kipple.db is not a Kipple database"))
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return bad(err)
	}
	switch {
	case version > store.LatestVersion():
		return bad(&NewerError{})
	case version < 1:
		return bad(errors.New("kipple.db is empty"))
	}
	// Before anything else runs on it: the integrity checks below write to the
	// search index.
	if err := CheckSchema(ctx, db, version); err != nil {
		return bad(err)
	}
	info, err := inspect(ctx, db, true)
	if err != nil {
		return bad(err)
	}
	acct, err := readAccount(ctx, db)
	if err != nil {
		return bad(err)
	}
	return info, acct, nil
}

// CheckSchema compares the schema of db (at version) with a fresh database of
// the same version, in both directions: any table, index, trigger or view the
// fresh one does not have is refused, and so is one it has that db lacks, one
// whose definition differs (its text with comments and spacing ignored,
// store.NormalizeSQL, which is where constraints, defaults and index
// conditions are written), and a table or index whose shape differs
// (store.SchemaObject.Shape: its columns and kind as SQLite reads them). A
// table redefined with other columns, as a virtual table or with generated
// columns would pass a check by name and then stop Kipple at every start.
// SQLite's own bookkeeping (sqliteInternal) is the only exception; a trigger or
// view is never one.
//
// Everything that reads only sqlite_master (names, tables, definitions) is
// checked first, and the whole upload refused on any difference, before a shape
// is read: reading one runs SQLite's own code on the object, which must then be
// one Kipple made.
func CheckSchema(ctx context.Context, db *sql.DB, version int) error {
	want, err := store.SchemaAt(ctx, version)
	if err != nil {
		return err
	}
	have, err := store.ReadSchema(ctx, db)
	if err != nil {
		return err
	}
	key := func(o store.SchemaObject) string { return o.Type + "\x00" + o.Name }
	ref := map[string]store.SchemaObject{}
	for _, o := range want {
		ref[key(o)] = o
	}
	seen := map[string]bool{}
	var shaped []store.SchemaObject // matched by name and text: their shapes are compared next
	for _, o := range have {
		seen[key(o)] = true
		w, ok := ref[key(o)]
		switch {
		case !ok && sqliteInternal(o):
		case !ok:
			return fmt.Errorf("kipple.db holds the %s %q, which Kipple never creates", o.Type, o.Name)
		case !strings.EqualFold(w.Table, o.Table):
			return fmt.Errorf("kipple.db has the %s %q on the wrong table", o.Type, o.Name)
		case !sqliteInternal(o) && store.NormalizeSQL(o.SQL) != store.NormalizeSQL(w.SQL):
			return fmt.Errorf("kipple.db has a changed %s %q", o.Type, o.Name)
		case !sqliteInternal(o) && (o.Type == "table" || o.Type == "index"):
			shaped = append(shaped, o)
		}
	}
	for _, w := range want {
		if !seen[key(w)] && !sqliteInternal(w) {
			return fmt.Errorf("kipple.db lacks the %s %q", w.Type, w.Name)
		}
	}
	for _, o := range shaped {
		got, err := store.Shape(ctx, db, o)
		if err != nil {
			return fmt.Errorf("kipple.db: read the %s %q: %w", o.Type, o.Name, err)
		}
		if got != ref[key(o)].Shape {
			return fmt.Errorf("kipple.db has a changed %s %q", o.Type, o.Name)
		}
	}
	return nil
}

// sqliteInternal reports SQLite's own bookkeeping, which comes and goes with
// ANALYZE, PRAGMA optimize and AUTOINCREMENT: the sqlite_sequence and
// sqlite_stat tables, and the indexes SQLite makes for UNIQUE and PRIMARY KEY
// (no SQL). Nothing else named sqlite_* is, whatever its name says.
func sqliteInternal(o store.SchemaObject) bool {
	switch o.Type {
	case "table":
		switch o.Name {
		case "sqlite_sequence", "sqlite_stat1", "sqlite_stat2", "sqlite_stat3", "sqlite_stat4":
			return true
		}
	case "index":
		return strings.HasPrefix(o.Name, "sqlite_autoindex_") && o.SQL == ""
	}
	return false
}

func readAccount(ctx context.Context, db *sql.DB) (BackupAccount, error) {
	var modes int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('account') WHERE name = 'auth_mode'").Scan(&modes); err != nil {
		return BackupAccount{}, err
	}
	mode := "'" + store.AuthStandard + "'" // before account modes existed every account was standard
	if modes > 0 {
		mode = "auth_mode"
	}
	var a BackupAccount
	var hash, m string
	err := db.QueryRowContext(ctx, "SELECT username, password_hash, "+mode+" FROM account WHERE id = 1").Scan(&a.Username, &hash, &m)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupAccount{}, errors.New("it holds no account")
	}
	if err != nil {
		return BackupAccount{}, err
	}
	a.HasPassword = hash != ""
	a.Open = m == store.AuthOpen
	return a, nil
}

// EstimateSeconds is how long the restart that applies a restore of dbBytes
// should take, from the measured times in docs/performance.md: about 16 s per
// GB to restore and, for a backup of an older Kipple, about 7.5 s per GB for
// the safety copy and the upgrade. Rounded up, at least 30 s.
func EstimateSeconds(dbBytes int64, older bool) int {
	perGB := 16.0
	if older {
		perGB += 7.5
	}
	s := int(math.Ceil(float64(dbBytes) / (1 << 30) * perGB))
	return max(s, 30)
}

// prepareStaged edits the staged copy before it is installed: every web session
// is signed out, the backup's server settings (ServerSettingPrefixes: they
// describe the server it was made on, and the trusted proxies also decide who
// may claim to be which client) are replaced by the live instance's own (live may
// be nil: then they are only cleared), and passwordHash, when not empty, becomes
// the web password. A restore must not drop the address this instance answers at.
func prepareStaged(ctx context.Context, path, passwordHash string, live *sql.DB) error {
	db, err := openUntrusted(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return fmt.Errorf("restore: sign out sessions: %w", err)
	}
	if live == nil {
		if err := clearServerSettings(ctx, tx); err != nil {
			return fmt.Errorf("restore: clear the server settings: %w", err)
		}
	} else {
		rows, err := readServerSettings(ctx, live)
		if err != nil {
			return fmt.Errorf("restore: read this server's settings: %w", err)
		}
		if err := writeServerSettings(ctx, tx, rows); err != nil {
			return fmt.Errorf("restore: keep this server's settings: %w", err)
		}
	}
	if passwordHash != "" {
		var modes int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('account') WHERE name = 'auth_mode'").Scan(&modes); err != nil {
			return err
		}
		q := "UPDATE account SET password_hash = ?, updated_at = unixepoch() WHERE id = 1"
		args := []any{passwordHash}
		if modes > 0 {
			q = "UPDATE account SET password_hash = ?, auth_mode = ?, updated_at = unixepoch() WHERE id = 1"
			args = append(args, store.AuthStandard)
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("restore: set the new password: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("restore: the backup holds no account")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.Close()
}

// RecordRestoreGap marks the days up to today as a statistics gap in the installed database (one
// path for the setup restore and `kipple restore`): the reading since the backup was made is in the
// database that was replaced, and a comparison must not read those days as quiet. It also pulls a
// marker the backup carried from the future back to today. Statistics only: a failure is for the
// caller to log, never a reason to stop a restore or a start.
func RecordRestoreGap(ctx context.Context, dataDir string, now time.Time) error {
	db, err := openUntrusted(filepath.Join(dataDir, "kipple.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.RecordStatsGapToday(ctx, tx, now); err != nil {
		return fmt.Errorf("record the statistics gap since the backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.Close()
}

// marker is MarkerFile: what was confirmed, for the log of the start that
// applies it. Its existence is the confirmation.
type marker struct {
	KippleVersion string `json:"kipple_version"`
	CreatedAt     string `json:"created_at"`
	Username      string `json:"username"`
}

// MarkerPending reports whether a confirmed restore or reset waits for the next
// start.
func MarkerPending(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, MarkerFile))
	return err == nil
}

// writeMarker writes path atomically (a temporary file, synced, renamed) so a
// crash leaves either no marker or a whole one. With exclusive the marker is
// created in place with O_EXCL instead, which fails (ErrRestorePending) when one
// exists, so two writers can never both win.
func writeMarker(path string, m marker, exclusive bool) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if exclusive {
		// Created in place with O_EXCL: of two writers one wins, and the file's
		// content is only for the log, so a crash while it is written leaves a
		// marker that still means "apply the staged database".
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			return ErrRestorePending
		}
		if err != nil {
			return fmt.Errorf("restore: marker: %w", err)
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(path)
			return fmt.Errorf("restore: marker: %w", err)
		}
		syncDir(filepath.Dir(path))
		return nil
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("restore: marker: %w", err)
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("restore: marker: %w", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes renames in dir durable where the OS allows it (not on Windows).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// removeStaged deletes the files of an upload: the spool, the staged database
// and anything SQLite left beside it.
func removeStaged(dataDir string) {
	staged := filepath.Join(dataDir, StagedFile)
	for _, p := range []string{filepath.Join(dataDir, UploadFile), staged, staged + "-journal", staged + "-wal", staged + "-shm", filepath.Join(dataDir, MarkerFile+".tmp")} {
		_ = os.Remove(p)
	}
}

// MaxMarkerAge is how old a confirmed restore or reset may be and still be
// applied at start.
const MaxMarkerAge = 7 * 24 * time.Hour

// Applied is what ApplyStaged did.
type Applied struct {
	// Restored: a confirmed restore was installed now.
	Restored bool
	// Stale: a confirmed marker older than MaxMarkerAge was not applied. MarkerTime is
	// when it was written (its file time) and StaleErr why removing it failed (nil:
	// it and the staged database were removed).
	Stale      bool
	MarkerTime time.Time
	StaleErr   error
	// Pre is the directory the replaced database went to ("" when there was none).
	Pre string
	// KippleVersion, CreatedAt and Username describe the backup, from the marker.
	KippleVersion, CreatedAt, Username string
}

// ApplyStaged finishes a restore confirmed in the setup wizard, or a reset
// confirmed in Settings (a reset stages a fresh database that keeps the server
// settings, so it is a restore like any other). Run it at start
// under the data lock, before the database is opened. It is idempotent, so a
// crash at any point is finished or cleaned up by the next start:
//
//   - marker and staged database: the staged one replaces kipple.db (the old one
//     goes to backup/pre-restore-*, as with `kipple restore`), then the marker is
//     removed;
//   - marker without a staged database: the swap was done and only the marker
//     was left: it is removed;
//   - staged database (or a spooled upload) without a marker: an upload that was
//     never confirmed, or expired with the process: deleted.
//
// A failed swap puts the old database back and keeps the marker, so the next
// start tries again. A marker older than MaxMarkerAge (by its file time) is
// discarded with its staged database instead (Applied.Stale). local is the
// server's zone (PrunePreRestore).
func ApplyStaged(dataDir string, now time.Time, local *time.Location) (Applied, error) {
	mpath := filepath.Join(dataDir, MarkerFile)
	b, err := os.ReadFile(mpath)
	if errors.Is(err, fs.ErrNotExist) {
		removeStaged(dataDir)
		return Applied{}, nil
	}
	if err != nil {
		return Applied{}, fmt.Errorf("restore: read %s: %w", MarkerFile, err)
	}
	var m marker
	_ = json.Unmarshal(b, &m) // only for the log; the file's existence is the confirmation
	out := Applied{KippleVersion: m.KippleVersion, CreatedAt: m.CreatedAt, Username: m.Username}
	// A marker that outlived days of use (a rollback to a Kipple that ignores it,
	// then an upgrade again) is a decision nobody still means: drop it.
	if fi, err := os.Stat(mpath); err == nil && now.Sub(fi.ModTime()) > MaxMarkerAge {
		out.Stale, out.MarkerTime = true, fi.ModTime()
		if err := os.Remove(mpath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			out.StaleErr = err // it is not applied, and removed at a later start
			return out, nil
		}
		removeStaged(dataDir)
		return out, nil
	}
	staged := filepath.Join(dataDir, StagedFile)
	if _, err := os.Stat(staged); err == nil {
		pre, err := Swap(dataDir, staged, now)
		if err != nil {
			return Applied{}, fmt.Errorf("restore: %w (it is tried again at the next start)", err)
		}
		syncDir(dataDir)
		out.Restored, out.Pre = true, pre
		PrunePreRestore(filepath.Join(dataDir, "backup"), now, local)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Applied{}, fmt.Errorf("restore: %w", err)
	}
	if err := os.Remove(mpath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return out, fmt.Errorf("restore: remove %s: %w", MarkerFile, err)
	}
	removeStaged(dataDir)
	return out, nil
}

// DiscardStaged deletes a restore staged in the setup wizard, confirmed or not,
// and reports whether a confirmed one was dropped. `kipple restore` runs it
// before its own swap: that restore is the newer decision, and dropping the
// other first means a crash in between can never leave both in place.
func DiscardStaged(dataDir string) bool {
	err := os.Remove(filepath.Join(dataDir, MarkerFile))
	removeStaged(dataDir)
	return err == nil
}
