package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
)

// SettingLastVersion is the release version of the binary that last started against this database
// (sys.last_version). It exists for one purpose: naming the newer Kipple in the message of a refused
// downgrade, and showing it on the About screen. It is written at every successful start.
const SettingLastVersion = "sys.last_version"

// RecordVersion stores v as the version that last opened the database. A development build ("dev" or empty)
// never overwrites a real release version, so a `go run` against a copy of real data does not erase what a
// later downgrade message should say.
func (d *DB) RecordVersion(ctx context.Context, v string) error {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return nil
	}
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return setSettingsTx(ctx, tx, map[string]any{SettingLastVersion: v})
	})
}

// LastVersion is the version recorded by RecordVersion, "" when none was.
func (d *DB) LastVersion(ctx context.Context) string {
	return settingString(ctx, d.reader, SettingLastVersion, "")
}

// SQLiteVersion is the version of the SQLite library the driver embeds.
func (d *DB) SQLiteVersion(ctx context.Context) string {
	var v string
	if err := d.reader.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v); err != nil {
		return ""
	}
	return v
}

// downgradeError explains a database that is newer than this binary: what this binary is, the Kipple that last
// started on the database when that is on record (read best-effort: the settings table exists at every schema, and
// any failure just leaves the sentence out), and the two ways out. The recorded version is named as the one to run
// only when it is not this binary: an upgrade that failed partway leaves the schema ahead of the version on record.
// The way back is the snapshot rollbackSnapshot finds, or a description of it when the backup folder has none.
func (d *DB) downgradeError(ctx context.Context, cur, latest int) error {
	this := "this binary"
	v := strings.TrimSpace(d.version)
	if v != "" {
		this = "Kipple " + v
	}
	msg := fmt.Sprintf("store: database schema version %d is newer than this binary (%d); refusing to start.", cur, latest)
	last, err := settingStringErr(ctx, d.writer, SettingLastVersion, "")
	if err != nil {
		last = ""
	}
	if last != "" {
		msg += fmt.Sprintf(" The last Kipple that started on this database is %s.", last)
	}
	snap := rollbackSnapshot(d.backupDir, latest)
	if snap == "" {
		snap = fmt.Sprintf("the newest pre-migration-<from>-<to>-<time>.db whose <from> is at most %d", latest)
	}
	msg += fmt.Sprintf(" To run %s (schema %d), restore %s from the backup folder (docs/deploy.md, Rolling back).", this, latest, snap)
	if last != "" && last != v {
		msg += fmt.Sprintf(" Otherwise run %s or newer.", last)
	} else {
		msg += fmt.Sprintf(" Otherwise run a Kipple whose schema is %d or newer.", cur)
	}
	return fmt.Errorf("%s", msg)
}

// rollbackSnapshot is the name of the pre-migration snapshot in dir that a binary at schema latest can open: the
// highest <from> at most latest whose <to> is above it (taken before an upgrade past latest), the newest of those.
// "" when there is none or the folder cannot be read.
func rollbackSnapshot(dir string, latest int) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "pre-migration-*-*-*.db"))
	best, bestFrom, bestAt := "", -1, int64(-1)
	for _, m := range matches {
		name := filepath.Base(m)
		var from, to int
		var at int64
		if n, err := fmt.Sscanf(name, "pre-migration-%d-%d-%d.db", &from, &to, &at); err != nil || n != 3 {
			continue
		}
		if from > latest || to <= latest {
			continue
		}
		if from > bestFrom || (from == bestFrom && at > bestAt) {
			best, bestFrom, bestAt = name, from, at
		}
	}
	return best
}
