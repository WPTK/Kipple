package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/WPTK/kipple/internal/store"
)

// DBInfo is what Inspect learns about a database file.
type DBInfo struct {
	SchemaVersion int
	Feeds         int64 // subscriptions, archive holder excluded
	Items         int64
	Starred       int64
}

// openFile opens a database file for inspection on one connection. The journal
// mode is DELETE so that neither a -wal nor a -shm is left behind next to it
// and a read-only directory copy still opens.
func openFile(path string) (*sql.DB, error) {
	return openDSN(path, url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(DELETE)"}})
}

// openFileUntouched opens a database file read-only and immutable: nothing is
// written, no -wal or -shm is created or recovered, and only the main file is
// read (a live database's WAL, if any, is not seen). For a glance at the live
// database before a restore replaces it.
func openFileUntouched(path string) (*sql.DB, error) {
	return openDSN(path, url.Values{"mode": {"ro"}, "immutable": {"1"}})
}

func openDSN(path string, q url.Values) (*sql.DB, error) {
	u := url.URL{Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", "file:"+u.EscapedPath()+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Inspect verifies that path is a healthy Kipple database and reads its
// counts. full runs PRAGMA integrity_check, foreign_key_check and the FTS
// integrity-check (restore); otherwise PRAGMA quick_check (export). It refuses
// a file that is not a Kipple database and one whose schema is newer than this
// binary. The file is opened read-write only so the journal mode can be DELETE;
// nothing in it is changed.
func Inspect(ctx context.Context, path string, full bool) (DBInfo, error) {
	db, err := openFile(path)
	if err != nil {
		return DBInfo{}, err
	}
	defer db.Close()
	return inspect(ctx, db, full)
}

func inspect(ctx context.Context, db *sql.DB, full bool) (DBInfo, error) {
	var info DBInfo
	var appID int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID); err != nil {
		return info, fmt.Errorf("not a readable SQLite database: %w", err)
	}
	if appID != store.ApplicationID {
		return info, errors.New("not a Kipple database (application_id mismatch)")
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&info.SchemaVersion); err != nil {
		return info, err
	}
	if latest := store.LatestVersion(); info.SchemaVersion > latest {
		return info, fmt.Errorf("the database schema version %d is newer than this Kipple binary (%d): upgrade Kipple first", info.SchemaVersion, latest)
	}
	if info.SchemaVersion < 1 {
		return info, errors.New("the database has no schema (user_version 0)")
	}

	check := "quick_check"
	if full {
		check = "integrity_check"
	}
	rows, err := db.QueryContext(ctx, "PRAGMA "+check)
	if err != nil {
		return info, fmt.Errorf("%s: %w", check, err)
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return info, err
		}
		if line != "ok" && len(problems) < 5 {
			problems = append(problems, line)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return info, fmt.Errorf("%s: %w", check, err)
	}
	if len(problems) > 0 {
		return info, fmt.Errorf("database is damaged (%s): %s", check, strings.Join(problems, "; "))
	}

	if full {
		fk, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return info, fmt.Errorf("foreign_key_check: %w", err)
		}
		bad := fk.Next()
		fk.Close()
		if bad {
			return info, errors.New("database is damaged (foreign_key_check reported violations)")
		}
		var hasFTS int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name = 'items_fts'").Scan(&hasFTS); err == nil && hasFTS > 0 {
			if _, err := db.ExecContext(ctx, "INSERT INTO items_fts(items_fts) VALUES('integrity-check')"); err != nil {
				return info, fmt.Errorf("search index is damaged (fts integrity-check): %w", err)
			}
		}
	}

	for _, c := range []struct {
		q string
		p *int64
	}{
		{"SELECT count(*) FROM feeds WHERE disabled_reason IS NOT 'archive'", &info.Feeds},
		{"SELECT count(*) FROM items", &info.Items},
		{"SELECT count(*) FROM items WHERE starred = 1", &info.Starred},
	} {
		if err := db.QueryRowContext(ctx, c.q).Scan(c.p); err != nil {
			return info, fmt.Errorf("read counts: %w", err)
		}
	}
	return info, nil
}
