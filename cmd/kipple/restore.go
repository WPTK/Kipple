package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/backup"
	"github.com/WPTK/kipple/internal/config"
	"github.com/WPTK/kipple/internal/lock"
)

type restoreOptions struct {
	DataDir string
	Src     string
	Yes     bool
	In      io.Reader // read when Src is "-"
	Out     io.Writer
	Now     func() time.Time
}

const restoreUsage = "usage: kipple restore <backup.zip|kipple.db|-> [--yes]  (- reads the file from standard input)"

// runRestore implements `kipple restore <backup.zip|.db> [--yes]`.
func runRestore(args []string) error {
	src, yes, err := parseRestoreArgs(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return restore(context.Background(), restoreOptions{DataDir: cfg.DataDir, Src: src, Yes: yes, In: os.Stdin, Out: os.Stdout, Now: time.Now})
}

// parseRestoreArgs reads the source and --yes. A lone "-" is the source
// (standard input), not an option, so it is matched before the option check.
func parseRestoreArgs(args []string) (src string, yes bool, err error) {
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-yes":
			yes = true
		case a == "-" && src == "":
			src = a
		case a != "-" && strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown option %q (%s)", a, restoreUsage)
		case src == "":
			src = a
		default:
			return "", false, errors.New(restoreUsage)
		}
	}
	if src == "" {
		return "", false, errors.New(restoreUsage)
	}
	return src, yes, nil
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
	label := o.Src
	if label == "-" {
		label = "standard input"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if err := ensureDataDir(o.DataDir); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	lockPath := filepath.Join(o.DataDir, "kipple.lock")
	backupDir := filepath.Join(o.DataDir, "backup")
	_, statErr := os.Stat(backupDir)
	backupExisted := statErr == nil
	lk, err := lock.Acquire(lockPath)
	if errors.Is(err, lock.ErrLocked) {
		return fmt.Errorf("kipple is running on %s (it holds kipple.lock): stop it first, then restore (see docs/deploy.md)", o.DataDir)
	}
	if err != nil {
		return err
	}
	defer lk.Release()
	// Run as root, restore hands everything it may have created to the data
	// directory's owner on the way out, whatever the outcome: kipple.lock (serve
	// opens it read-write), the backup/ directory if it was created here, the
	// pre-restore directory and the new database. A root-owned lock or backup/
	// would stop the nonroot server from starting or taking snapshots.
	owned := []string{lockPath}
	defer func() { fixOwnership(out, o.DataDir, owned...) }()

	live := filepath.Join(o.DataDir, "kipple.db")
	tmp := filepath.Join(o.DataDir, "restore-tmp.db")
	removeTmp := func() {
		for _, s := range []string{"", "-wal", "-shm", "-journal"} {
			_ = os.Remove(tmp + s)
		}
	}
	removeTmp() // a leftover of an interrupted restore
	defer removeTmp()

	// "-" spools standard input into the data directory first (the distroless
	// container's user cannot read a bind-mounted /import, and a zip needs random
	// access), and removes the spool afterwards.
	if o.Src == "-" {
		upload := filepath.Join(o.DataDir, backup.UploadFile)
		_ = os.Remove(upload)
		defer os.Remove(upload)
		if o.In == nil {
			return errors.New("no input to read")
		}
		f, err := os.OpenFile(upload, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, o.In)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("read the backup from standard input: %w", err)
		}
		o.Src = upload
	}
	if a, err1 := filepath.Abs(o.Src); err1 == nil {
		if b, err2 := filepath.Abs(live); err2 == nil && a == b {
			return errors.New("that is the live database itself; restore takes a backup zip or a snapshot copy")
		}
	}
	zipped, err := isZip(o.Src)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	var created string
	if zipped {
		mf, err := backup.ExtractDB(o.Src, tmp)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		created = mf.CreatedAt + " by Kipple " + mf.KippleVersion + "; checksums verified"
	} else {
		if err := copyFile(o.Src, tmp); err != nil {
			return fmt.Errorf("copy %s: %w", o.Src, err)
		}
		created = "a bare database file (no checksums to verify, only its own integrity checks)"
		// A database that was not closed cleanly has its newest transactions in a
		// -wal beside it (a pre-restore copy after an unclean stop). Bring the WAL
		// along so opening the copy replays and checkpoints it; the .db alone would
		// silently drop those transactions.
		if st, err := os.Stat(o.Src + "-wal"); err == nil && st.Size() > 0 {
			if err := copyFile(o.Src+"-wal", tmp+"-wal"); err != nil {
				return fmt.Errorf("copy %s-wal: %w", o.Src, err)
			}
			created += "; its -wal was applied"
		}
	}
	info, err := backup.Inspect(ctx, tmp, true)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	fmt.Fprintf(out, "Backup: %s\n  taken: %s\n  %d feeds, %d items, %d starred; passes the integrity checks.\n",
		label, created, info.Feeds, info.Items, info.Starred)
	if !o.Yes {
		return errNotConfirmed
	}

	if n, err := backup.RevokeSessions(ctx, tmp); err != nil {
		return fmt.Errorf("sign out sessions in the restored copy: %w", err)
	} else if n > 0 {
		fmt.Fprintf(out, "  %d web session(s) in the backup were signed out.\n", n)
	}

	if !backupExisted {
		owned = append(owned, backupDir) // a swap creates it; chown ignores a missing path
	}
	// A restore confirmed in the setup wizard and not applied yet (Kipple was
	// stopped before it started again) would replace this one at the next start:
	// this restore is the newer decision, so that one is dropped first. Done
	// before the swap, so a crash in between can never leave both in place.
	if backup.DiscardStaged(o.DataDir) {
		fmt.Fprintln(out, "A restore that was waiting to be applied at the next start was dropped; this one replaces it.")
	}
	pre, err := backup.Swap(o.DataDir, tmp, o.Now())
	if err != nil {
		return err
	}
	moved := pre != ""
	owned = append(owned, live)
	if moved {
		owned = append(owned, pre)
	}
	backup.PrunePreRestore(backupDir, localZone())
	if moved {
		fmt.Fprintf(out, "The previous database was moved to %s\n", pre)
	} else {
		fmt.Fprintln(out, "There was no previous database to keep.")
	}
	fmt.Fprintln(out, "Restored. Next: start Kipple (a backup from an older Kipple is upgraded on that start, after a safety copy),")
	fmt.Fprintln(out, "sign in again (all sessions were signed out), and check the feed count on the status page.")
	if moved {
		fmt.Fprintln(out, "To undo, stop Kipple and restore the file in that pre-restore directory (kipple.db, with its -wal if present).")
	}
	return nil
}
