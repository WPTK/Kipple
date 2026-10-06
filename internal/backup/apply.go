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
// fresh one does not have is refused, and so is one it has that db lacks, and a
// trigger or view whose definition differs. Tables and indexes are matched by
// name: their text changes with how a migration was worded, while code that
// runs by itself lives only in triggers and views. SQLite's own bookkeeping
// (sqliteInternal) is the only exception; a trigger or view is never one.
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
	for _, o := range have {
		seen[key(o)] = true
		w, ok := ref[key(o)]
		switch {
		case !ok && sqliteInternal(o):
		case !ok:
			return fmt.Errorf("kipple.db holds the %s %q, which Kipple never creates", o.Type, o.Name)
		case !strings.EqualFold(w.Table, o.Table):
			return fmt.Errorf("kipple.db has the %s %q on the wrong table", o.Type, o.Name)
		case (o.Type == "trigger" || o.Type == "view") && squash(o.SQL) != squash(w.SQL):
			return fmt.Errorf("kipple.db has a changed %s %q", o.Type, o.Name)
		}
	}
	for _, w := range want {
		if !seen[key(w)] && !sqliteInternal(w) {
			return fmt.Errorf("kipple.db lacks the %s %q", w.Type, w.Name)
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

// squash collapses runs of white space, so a definition compares by its tokens.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

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

// HostSettings are the settings a restore clears: they describe the server a
// backup was made on, not the library (the trusted proxies also decide who may
// claim to be which client, so a stale list must not carry over).
var HostSettings = []string{store.SettingPublicURL, store.SettingAllowedHosts, store.SettingTrustedProxies}

func prepareStaged(ctx context.Context, path, passwordHash string) error {
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
	for _, k := range HostSettings {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", k); err != nil {
			return fmt.Errorf("restore: clear %s: %w", k, err)
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

// marker is MarkerFile: what was confirmed, for the log of the start that
// applies it. Its existence is the confirmation.
type marker struct {
	// Kind is KindRestoreMarker (or empty) for a confirmed restore and
	// KindResetMarker for a reset, which has no staged database.
	Kind          string `json:"kind,omitempty"`
	KippleVersion string `json:"kipple_version"`
	CreatedAt     string `json:"created_at"`
	Username      string `json:"username"`
}

// Marker kinds: what the next start does with the live database.
const (
	KindRestoreMarker = "restore" // swap in the staged database
	KindResetMarker   = "reset"   // swap in nothing: a fresh database, in setup mode
)

// MarkerPending reports whether a confirmed restore or reset waits for the next
// start.
func MarkerPending(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, MarkerFile))
	return err == nil
}

// WriteResetMarker confirms a reset: the next start moves the live database to
// backup/pre-restore-<ts>/ and starts empty. It refuses (ErrRestorePending)
// when a marker is already there, so a pending restore is never replaced.
func WriteResetMarker(dataDir, kippleVersion, username string, now time.Time) error {
	if MarkerPending(dataDir) {
		return ErrRestorePending
	}
	return writeMarker(filepath.Join(dataDir, MarkerFile), marker{
		Kind: KindResetMarker, KippleVersion: kippleVersion, Username: username,
		CreatedAt: now.UTC().Format(time.RFC3339),
	})
}

// writeMarker writes path atomically (a temporary file, synced, renamed) so a
// crash leaves either no marker or a whole one.
func writeMarker(path string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
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

// Applied is what ApplyStaged did.
type Applied struct {
	// Restored: a confirmed restore was installed now.
	Restored bool
	// Reset: a confirmed reset moved the database away now.
	Reset bool
	// Pre is the directory the replaced database went to ("" when there was none).
	Pre string
	// KippleVersion, CreatedAt and Username describe the backup, from the marker.
	KippleVersion, CreatedAt, Username string
}

// ApplyStaged finishes a restore confirmed in the setup wizard, or a reset
// confirmed in Settings. Run it at start
// under the data lock, before the database is opened. It is idempotent, so a
// crash at any point is finished or cleaned up by the next start:
//
//   - marker and staged database: the staged one replaces kipple.db (the old one
//     goes to backup/pre-restore-*, as with `kipple restore`), then the marker is
//     removed;
//   - a reset marker: the live database (if any) goes to backup/pre-restore-*
//     and nothing replaces it; without a live database it was already done and
//     only the marker is removed;
//   - marker without a staged database: the swap was done and only the marker
//     was left: it is removed;
//   - staged database (or a spooled upload) without a marker: an upload that was
//     never confirmed, or expired with the process: deleted.
//
// A failed swap puts the old database back and keeps the marker, so the next
// start tries again. local is the server's zone (PrunePreRestore).
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
	staged := filepath.Join(dataDir, StagedFile)
	if m.Kind == KindResetMarker {
		pre, err := Retire(dataDir, now)
		if err != nil {
			return Applied{}, fmt.Errorf("reset: %w (it is tried again at the next start)", err)
		}
		syncDir(dataDir)
		out.Reset, out.Pre = pre != "", pre
		PrunePreRestore(filepath.Join(dataDir, "backup"), local)
	} else if _, err := os.Stat(staged); err == nil {
		pre, err := Swap(dataDir, staged, now)
		if err != nil {
			return Applied{}, fmt.Errorf("restore: %w (it is tried again at the next start)", err)
		}
		syncDir(dataDir)
		out.Restored, out.Pre = true, pre
		PrunePreRestore(filepath.Join(dataDir, "backup"), local)
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
// after its own swap: that restore is the newer decision.
func DiscardStaged(dataDir string) bool {
	err := os.Remove(filepath.Join(dataDir, MarkerFile))
	removeStaged(dataDir)
	return err == nil
}
