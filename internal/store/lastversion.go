package store

import (
	"context"
	"database/sql"
	"fmt"
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
// any failure just leaves the sentence out), and the two ways out. The recorded version is not assumed to be newer
// than this binary: an upgrade that failed partway leaves the schema between two versions while the version on
// record is still the old one, so the way back names the snapshot by this binary's schema, which is always right.
func (d *DB) downgradeError(ctx context.Context, cur, latest int) error {
	this := "this binary"
	if v := strings.TrimSpace(d.version); v != "" {
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
	msg += fmt.Sprintf(" To run %s (schema %d), restore the snapshot pre-migration-%d-<to>-<time>.db from the backup folder"+
		" (docs/deploy.md, Rolling back); otherwise run a Kipple whose schema is %d or newer.", this, latest, latest, cur)
	return fmt.Errorf("%s", msg)
}
