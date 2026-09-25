package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/lock"
)

// keepPreRestore is how many pre-restore-* directories are kept.
const keepPreRestore = 3

type restoreOptions struct {
	DataDir string
	Src     string
	Yes     bool
	Out     io.Writer
	Now     func() time.Time
}

const restoreUsage = "usage: kipple restore <backup.zip|kipple.db> [--yes]"

// runRestore implements `kipple restore <backup.zip|.db> [--yes]`.
func runRestore(args []string) error {
	var src string
	yes := false
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-yes":
			yes = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown option %q (%s)", a, restoreUsage)
		case src == "":
			src = a
		default:
			return errors.New(restoreUsage)
		}
	}
	if src == "" {
		return errors.New(restoreUsage)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return restore(context.Background(), restoreOptions{DataDir: cfg.DataDir, Src: src, Yes: yes, Out: os.Stdout, Now: time.Now})
}

var errNotConfirmed = errors.New("nothing was changed: run the same command again with --yes to restore")

func isZip(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	switch {
	case n >= 4 && string(head[:2]) == "PK":
		return true, nil
	case n >= 15 && string(head[:15]) == "SQLite format 3":
		return false, nil
	}
	return false, errors.New("the file is neither a Kipple backup zip nor a SQLite database")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}

// restore is design §2.6's restore: refuse under a running server, verify a
// temporary copy completely, keep the current database, swap, say what next.
func restore(ctx context.Context, o restoreOptions) error {
	out := o.Out
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	lk, err := lock.Acquire(filepath.Join(o.DataDir, "kipple.lock"))
	if errors.Is(err, lock.ErrLocked) {
		return fmt.Errorf("kipple is running on %s (it holds kipple.lock): stop it first, then restore (see docs/deploy.md)", o.DataDir)
	}
	if err != nil {
		return err
	}
	defer lk.Release()

	live := filepath.Join(o.DataDir, "kipple.db")
	tmp := filepath.Join(o.DataDir, "restore-tmp.db")
	removeTmp := func() {
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(tmp + s)
		}
	}
	removeTmp() // a leftover of an interrupted restore
	defer removeTmp()

	if a, err1 := filepath.Abs(o.Src); err1 == nil {
		if b, err2 := filepath.Abs(live); err2 == nil && a == b {
			return errors.New("that is the live database itself; restore takes a backup zip or a snapshot copy")
		}
	}
	zipped, err := isZip(o.Src)
	if err != nil {
		return fmt.Errorf("%s: %w", o.Src, err)
	}
	var created string
	if zipped {
		mf, err := backup.ExtractDB(o.Src, tmp)
		if err != nil {
			return fmt.Errorf("%s: %w", o.Src, err)
		}
		created = mf.CreatedAt + " by Kipple " + mf.KippleVersion + "; checksums verified"
	} else {
		if err := copyFile(o.Src, tmp); err != nil {
			return fmt.Errorf("copy %s: %w", o.Src, err)
		}
		created = "a bare database file (no checksums to verify, only its own integrity checks)"
	}
	info, err := backup.Inspect(ctx, tmp, true)
	if err != nil {
		return fmt.Errorf("%s: %w", o.Src, err)
	}
	fmt.Fprintf(out, "Backup: %s\n  taken: %s\n  schema version %d, %d feeds, %d items, %d starred; passes the integrity checks.\n",
		o.Src, created, info.SchemaVersion, info.Feeds, info.Items, info.Starred)
	if !o.Yes {
		return errNotConfirmed
	}

	if n, err := backup.RevokeSessions(ctx, tmp); err != nil {
		return fmt.Errorf("sign out sessions in the restored copy: %w", err)
	} else if n > 0 {
		fmt.Fprintf(out, "  %d web session(s) in the backup were signed out.\n", n)
	}

	var pre string
	moved, err := swap(o.DataDir, tmp, live, o.Now(), &pre)
	if err != nil {
		return err
	}
	prunePreRestore(filepath.Join(o.DataDir, "backup"))
	if moved {
		fmt.Fprintf(out, "The previous database was moved to %s\n", pre)
	} else {
		fmt.Fprintln(out, "There was no previous database to keep.")
	}
	fmt.Fprintln(out, "Restored. Next: start Kipple (it migrates an older schema after taking its own pre-migration snapshot),")
	fmt.Fprintln(out, "sign in again (all sessions were signed out), and check the feed count on the status page.")
	fmt.Fprintln(out, "To undo, stop Kipple and restore the file in that pre-restore directory (kipple.db, with its -wal if present).")
	return nil
}

// swap moves the live database (and its -wal/-shm) into a new
// backup/pre-restore-<ts>/ directory and renames tmp over kipple.db. Any
// failure puts everything back.
func swap(dataDir, tmp, live string, now time.Time, preOut *string) (moved bool, err error) {
	pre := filepath.Join(dataDir, "backup", "pre-restore-"+now.Format("20060102-150405"))
	var done []string // suffixes moved so far
	rollback := func() {
		for _, s := range done {
			_ = os.Rename(filepath.Join(pre, "kipple.db"+s), live+s)
		}
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(live + s); err != nil {
			continue
		}
		if len(done) == 0 {
			if err := os.MkdirAll(pre, 0o755); err != nil {
				return false, fmt.Errorf("pre-restore directory: %w", err)
			}
		}
		if err := os.Rename(live+s, filepath.Join(pre, "kipple.db"+s)); err != nil {
			rollback()
			return false, fmt.Errorf("keep the current database: %w", err)
		}
		done = append(done, s)
	}
	if err := os.Rename(tmp, live); err != nil {
		rollback()
		return false, fmt.Errorf("install the restored database (the current one was put back): %w", err)
	}
	*preOut = pre
	return len(done) > 0, nil
}

func prunePreRestore(backupDir string) {
	dirs, _ := filepath.Glob(filepath.Join(backupDir, "pre-restore-*"))
	sort.Strings(dirs) // the timestamp sorts as text
	for len(dirs) > keepPreRestore {
		_ = os.RemoveAll(dirs[0])
		dirs = dirs[1:]
	}
}
