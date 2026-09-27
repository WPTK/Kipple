package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ApplicationID is 'KIPL' = 0x4B49504C, stamped by 0001_init.sql.
const ApplicationID = 1263095884

const foreignKeysOffMarker = "-- kipple:foreign-keys-off"

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("store: bad migration name %q", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("store: bad migration name %q: %w", name, err)
		}
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations not dense: expected %04d, found %s", i+1, m.name)
		}
	}
	return ms, nil
}

// LatestVersion is the highest embedded migration number.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

func pragmaInt(ctx context.Context, q Querier, name string) (int, error) {
	var v int
	if err := q.QueryRowContext(ctx, "PRAGMA "+name).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: PRAGMA %s: %w", name, err)
	}
	return v, nil
}

// migrate implements design.md 2.5.
func (d *DB) migrate(ctx context.Context) error {
	ms := d.migrations
	if ms == nil {
		var err error
		if ms, err = loadMigrations(); err != nil {
			return err
		}
	}
	latest := len(ms)

	appID, err := pragmaInt(ctx, d.writer, "application_id")
	if err != nil {
		return err
	}
	var objects int
	if err := d.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&objects); err != nil {
		return fmt.Errorf("store: read schema: %w", err)
	}
	fresh := appID == 0 && objects == 0
	if !fresh && appID != ApplicationID {
		return errors.New("store: not a Kipple database")
	}

	cur, err := pragmaInt(ctx, d.writer, "user_version")
	if err != nil {
		return err
	}
	if cur > latest {
		return fmt.Errorf("store: database schema version %d is newer than this binary (%d); refusing to start", cur, latest)
	}

	if d.noMigrate {
		switch {
		case fresh:
			return errors.New("store: database is not initialised; start `kipple serve` once first")
		case cur < latest:
			return fmt.Errorf("store: database schema version %d is older than this binary (%d); start `kipple serve` to migrate it first", cur, latest)
		}
		return nil
	}

	if cur < latest && !fresh {
		if err := d.preMigrationSnapshot(ctx, cur, latest); err != nil {
			return err
		}
	}
	for _, m := range ms[cur:] {
		if err := d.applyMigration(ctx, m); err != nil {
			return err
		}
		d.log.Info("store: applied migration", "name", m.name)
	}
	if _, err := d.writer.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return fmt.Errorf("store: optimize after migrate: %w", err)
	}
	return nil
}

// migrationFreeBytes reports the free bytes on the volume holding a directory (a variable so a test
// can fake a full disk).
var migrationFreeBytes = diskFree

// Extra free space (not peak total usage) a migration needs on top of what the database already
// occupies: the pre-migration snapshot (VACUUM INTO) is a copy of about 1x the database file, taken
// with 10% slack, and the migration itself needs headroom for its WAL and file growth (an index
// build or table rewrite; 0009 adds about a quarter of the database), covered by a fixed
// migrateHeadroom. When the backup directory is on the same volume as the database the two
// requirements add up. The check refuses before anything is written, so a full volume ends in a
// clear error and an untouched database; it fails open when free space cannot be read.
const (
	snapshotFreeFactor = 1.1
	migrateHeadroom    = 64 << 20
)

// checkMigrationSpace refuses to migrate when the volumes are too full for the snapshot and the migration.
func (d *DB) checkMigrationSpace(from, to int) error {
	var size int64 // the database file alone, without the WAL
	if st, err := os.Stat(d.path); err == nil {
		size = st.Size()
	}
	if err := ensureDir(d.backupDir); err != nil {
		return fmt.Errorf("store: backup dir: %w", err)
	}
	dbDir := filepath.Dir(d.path)
	dbFree, err1 := migrationFreeBytes(dbDir)
	snapFree, err2 := migrationFreeBytes(d.backupDir)
	if err1 != nil || err2 != nil {
		return nil // cannot tell: do not block the upgrade on the check itself
	}
	dbNeed := uint64(size) + migrateHeadroom
	snapNeed := uint64(float64(size) * snapshotFreeFactor)
	same := dbFree == snapFree
	if rel, err := filepath.Rel(dbDir, d.backupDir); err == nil && !strings.HasPrefix(rel, "..") {
		same = true
	}
	fail := func(free, need uint64, what string) error {
		return fmt.Errorf("store: not enough free disk space to migrate the database from schema %d to %d: %d bytes free %s, at least %d more needed; free some space and start again (nothing was changed)",
			from, to, free, what, need)
	}
	if same {
		if need := dbNeed + snapNeed; dbFree < need {
			return fail(dbFree, need, "on the volume holding the database and its pre-migration snapshot")
		}
		return nil
	}
	if dbFree < dbNeed {
		return fail(dbFree, dbNeed, "on the volume holding the database")
	}
	if snapFree < snapNeed {
		return fail(snapFree, snapNeed, "where the pre-migration snapshot goes")
	}
	return nil
}

func (d *DB) preMigrationSnapshot(ctx context.Context, from, to int) error {
	if err := d.checkMigrationSpace(from, to); err != nil {
		return err
	}
	snap, err := d.openSnapshot()
	if err != nil {
		return fmt.Errorf("store: open snapshot pool: %w", err)
	}
	defer snap.Close()
	target := filepath.Join(d.backupDir, fmt.Sprintf("pre-migration-%d-%d-%d.db", from, to, time.Now().UnixNano()))
	if err := createPrivate(target); err != nil {
		return fmt.Errorf("store: pre-migration snapshot: %w", err)
	}
	if _, err := snap.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(filepath.ToSlash(target), "'", "''")+"'"); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("store: pre-migration snapshot: %w", err)
	}
	// Keep the newest 3 by mtime.
	matches, _ := filepath.Glob(filepath.Join(d.backupDir, "pre-migration-*.db"))
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil {
			files = append(files, f{m, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, old := range files[min(3, len(files)):] {
		_ = os.Remove(old.path)
	}
	return nil
}

func (d *DB) applyMigration(ctx context.Context, m migration) (err error) {
	conn, err := d.writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: writer conn: %w", err)
	}
	defer conn.Close()

	fkOff := strings.HasPrefix(m.sql, foreignKeysOffMarker)
	// Restore and verify foreign keys on every path, including panic.
	defer func() {
		p := recover()
		if _, e := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys=ON"); e != nil {
			err = errors.Join(err, fmt.Errorf("store: restore foreign_keys: %w", e))
		}
		var fk int
		if e := conn.QueryRowContext(context.WithoutCancel(ctx), "PRAGMA foreign_keys").Scan(&fk); e != nil || fk != 1 {
			err = errors.Join(err, errors.New("store: foreign_keys not restored after migration"))
		}
		if p != nil {
			panic(p)
		}
	}()

	if fkOff {
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return fmt.Errorf("store: %s: foreign_keys off: %w", m.name, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("store: %s: begin: %w", m.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("store: %s: %w", m.name, err)
	}
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("store: %s: foreign_key_check: %w", m.name, err)
	}
	violations := rows.Next()
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: %s: foreign_key_check: %w", m.name, err)
	}
	if violations {
		return fmt.Errorf("store: %s: foreign_key_check reported violations", m.name)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("store: %s: set user_version: %w", m.name, err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("store: %s: commit: %w", m.name, err)
	}
	committed = true
	return nil
}

// Version returns PRAGMA user_version (used by tests and diagnostics).
func (d *DB) Version(ctx context.Context) (int, error) {
	return pragmaInt(ctx, d.reader, "user_version")
}
