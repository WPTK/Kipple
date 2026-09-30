package backup

import "context"

// RevokeSessions deletes every web session in the database file at path
// (restore does this to the copy it is about to install), returning how many.
func RevokeSessions(ctx context.Context, path string) (int64, error) {
	db, err := openFile(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	res, err := db.ExecContext(ctx, "DELETE FROM sessions")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// legacySchema is the migration that introduced sys.legacy_port: an older
// database with an account gets the flag when it migrates.
const legacySchema = 10

// PortSetting reads what the database at path says about the listen port:
// installed is whether it holds an account (a database that was never set up
// is no installation to follow), legacy whether an unset KIPPLE_ADDR then
// listens on the pre-0.5 port 7080 (sys.legacy_port is set, or it is an older
// database with an account, which migration 0010 will stamp). Restore asks this
// of the live database before replacing it, and of the copy otherwise. The
// file is opened read-only.
func PortSetting(ctx context.Context, path string) (installed, legacy bool, err error) {
	db, err := openFileReadOnly(path)
	if err != nil {
		return false, false, err
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, false, err
	}
	if version == 0 {
		return false, false, nil // never initialised
	}
	var accounts int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account").Scan(&accounts); err != nil {
		return false, false, err
	}
	if accounts == 0 {
		return false, false, nil
	}
	if version < legacySchema {
		return true, true, nil
	}
	var n int
	err = db.QueryRowContext(ctx, "SELECT count(*) FROM settings WHERE key = 'sys.legacy_port' AND value = 'true'").Scan(&n)
	return true, n > 0, err
}

// Initialized reports whether the database at path was ever initialised by
// Kipple (its schema version is set): a server has run on it. Read-only.
func Initialized(ctx context.Context, path string) (bool, error) {
	db, err := openFileReadOnly(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, err
	}
	return version > 0, nil
}

// SetLegacyPort writes sys.legacy_port into the database at path (the copy
// restore is about to install): the listen port belongs to the installation,
// not to the backup, so restoring an old backup into a new 1919 install keeps
// 1919, and restoring any backup into a 7080 install keeps 7080. An explicit
// false also stops migration 0010 from stamping an older copy.
func SetLegacyPort(ctx context.Context, path string, on bool) error {
	db, err := openFile(path)
	if err != nil {
		return err
	}
	defer db.Close()
	v := "false"
	if on {
		v = "true"
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('sys.legacy_port', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v)
	return err
}
