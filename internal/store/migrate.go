package store

import (
	"context"
	"database/sql"
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

// Markers a migration declares on its leading comment lines: foreignKeysOffMarker runs it with
// foreign keys off; rebuildsTableMarker says it copies a whole table (CREATE new, copy, DROP, RENAME),
// which the free-space check sizes for.
const (
	foreignKeysOffMarker = "-- kipple:foreign-keys-off"
	rebuildsTableMarker  = "-- kipple:rebuilds-table"
)

// marked reports whether the migration's leading comment lines include marker.
func (m migration) marked(marker string) bool {
	for _, line := range strings.Split(m.sql, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "--") {
			return false
		}
		if line == marker {
			return true
		}
	}
	return false
}

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
		return d.downgradeError(ctx, cur, latest)
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
		if err := d.preMigrationSnapshot(ctx, cur, latest, ms[cur:]); err != nil {
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
// with 10% slack, and the migrations themselves need room for their WAL and file growth: 1x the
// database plus a fixed migrateHeadroom (an index build; 0009 adds about a quarter of the database),
// or migrateRewriteFactor times the database plus the headroom when a pending migration declares
// rebuildsTableMarker (its WAL holds the new copy of the table and its indexes until the checkpoint,
// and the checkpoint then grows the file by the new table before the old pages are reused). When the
// backup directory is on the same volume as the database the two requirements add up. The check
// refuses before anything is written, so a full volume ends in a clear error and an untouched
// database; it fails open when free space cannot be read.
const (
	snapshotFreeFactor   = 1.1
	migrateRewriteFactor = 2
	migrateHeadroom      = 64 << 20
)

// migrateFactor is the multiple of the database size the pending migrations need for their WAL and
// growth: the largest any of them declares.
func migrateFactor(pending []migration) uint64 {
	for _, m := range pending {
		if m.marked(rebuildsTableMarker) {
			return migrateRewriteFactor
		}
	}
	return 1
}

// checkMigrationSpace refuses to migrate when the volumes are too full for the snapshot and the
// pending migrations.
func (d *DB) checkMigrationSpace(from, to int, pending []migration) error {
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
	dbNeed := migrateFactor(pending)*uint64(size) + migrateHeadroom
	snapNeed := uint64(float64(size) * snapshotFreeFactor)
	same := dbFree == snapFree
	if rel, err := filepath.Rel(dbDir, d.backupDir); err == nil && !strings.HasPrefix(rel, "..") {
		same = true
	}
	fail := func(free, need uint64, what string) error {
		return fmt.Errorf("store: not enough free disk space to migrate the database from schema %d to %d: %s, %d MB free of the %d MB needed, so free at least %d MB more and start again (nothing was changed)",
			from, to, what, free>>20, mbCeil(need), mbCeil(need-free))
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

// mbCeil is n bytes in whole megabytes, rounded up, so a shortfall is never shown as 0 MB.
func mbCeil(n uint64) uint64 { return (n + 1<<20 - 1) >> 20 }

func (d *DB) preMigrationSnapshot(ctx context.Context, from, to int, pending []migration) error {
	if err := d.checkMigrationSpace(from, to, pending); err != nil {
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
	// The log is where an operator finds the file to restore if the upgrade has to be rolled back.
	d.log.Info("store: wrote pre-migration snapshot", "file", filepath.Base(target), "from", from, "to", to)
	// One snapshot per from and to: a start that fails the same migration again (a restart loop after an upgrade
	// that stopped partway) replaces its own copy instead of stacking new ones, which would push the snapshot of the
	// schema the upgrade started from (the one a rollback needs) out of the newest 3. Of two copies of the same pair
	// the newer is the better rollback: usually the database did not change between them, and when it did (a restore
	// and some use of the older version in between, then the same upgrade again) the newer copy holds that use.
	same, _ := filepath.Glob(filepath.Join(d.backupDir, fmt.Sprintf("pre-migration-%d-%d-*.db", from, to)))
	for _, m := range same {
		if m != target {
			_ = os.Remove(m)
		}
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

	fkOff := m.marked(foreignKeysOffMarker)
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
	notices, err := migrationNotices(ctx, conn)
	if err != nil {
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
	for _, n := range notices {
		d.log.Warn("store: "+n, "migration", m.name)
	}
	return nil
}

// migrationNotices reads and drops temp.migration_notice, the table a migration creates (on the
// migration's own connection, so it never reaches the database file) when it has something to tell
// the operator, such as data it had to drop. The runner logs each message as a warning once the
// migration has committed. A migration without notices creates no table.
func migrationNotices(ctx context.Context, conn *sql.Conn) ([]string, error) {
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_temp_master WHERE type = 'table' AND name = 'migration_notice'").Scan(&n); err != nil {
		return nil, fmt.Errorf("migration notices: %w", err)
	}
	if n == 0 {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, "SELECT message FROM temp.migration_notice ORDER BY rowid")
	if err != nil {
		return nil, fmt.Errorf("migration notices: %w", err)
	}
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("migration notices: %w", err)
		}
		out = append(out, s)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("migration notices: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE temp.migration_notice"); err != nil {
		return nil, fmt.Errorf("migration notices: %w", err)
	}
	return out, nil
}

// Version returns PRAGMA user_version (used by tests and diagnostics).
func (d *DB) Version(ctx context.Context) (int, error) {
	return pragmaInt(ctx, d.reader, "user_version")
}
