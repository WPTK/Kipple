package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
		upload := filepath.Join(o.DataDir, "restore-upload.tmp")
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
	fmt.Fprintf(out, "Backup: %s\n  taken: %s\n  schema version %d, %d feeds, %d items, %d starred; passes the integrity checks.\n",
		label, created, info.SchemaVersion, info.Feeds, info.Items, info.Starred)
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
	var pre string
	moved, err := swap(o.DataDir, tmp, live, o.Now(), &pre)
	if err != nil {
		return err
	}
	owned = append(owned, live)
	if moved {
		owned = append(owned, pre)
	}
	prunePreRestore(backupDir)
	if moved {
		fmt.Fprintf(out, "The previous database was moved to %s\n", pre)
	} else {
		fmt.Fprintln(out, "There was no previous database to keep.")
	}
	fmt.Fprintln(out, "Restored. Next: start Kipple (it migrates an older schema after taking its own pre-migration snapshot),")
	fmt.Fprintln(out, "sign in again (all sessions were signed out), and check the feed count on the status page.")
	if moved {
		fmt.Fprintln(out, "To undo, stop Kipple and restore the file in that pre-restore directory (kipple.db, with its -wal if present).")
	}
	return nil
}

// swap moves the live database (and its -wal/-shm) into a new
// backup/pre-restore-<ts>/ directory and renames tmp over kipple.db. Any
// failure puts everything back.
func swap(dataDir, tmp, live string, now time.Time, preOut *string) (moved bool, err error) {
	var pre string
	var done []string // suffixes moved so far
	rollback := func() {
		for _, s := range done {
			_ = os.Rename(filepath.Join(pre, "kipple.db"+s), live+s)
		}
		// Remove the now-empty pre-restore directory: left behind, it would count
		// toward keepPreRestore and prune a real safety copy early. os.Remove
		// refuses a non-empty directory, so a file that failed to move back stays.
		if pre != "" {
			_ = os.Remove(pre)
		}
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(live + s); err != nil {
			continue
		}
		if len(done) == 0 {
			var err error
			if pre, err = newPreRestoreDir(filepath.Join(dataDir, "backup"), now); err != nil {
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

// preRestoreLayout is the timestamp in a pre-restore directory name. New names
// are UTC and end in Z (preRestoreUTC), so a daylight saving change can never
// make a newer name look older; names without the Z were written by older
// versions in the server's local time and are read as such.
const (
	preRestoreLayout = "20060102-150405"
	preRestoreUTC    = "Z"
)

// newPreRestoreDir creates backup/pre-restore-<second>, or <second>-2, -3, ...
// when that name is taken: two restores in one second must never share (and
// overwrite) a directory. prunePreRestore orders them by preRestoreKey.
func newPreRestoreDir(backupDir string, now time.Time) (string, error) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(backupDir, "pre-restore-"+now.UTC().Format(preRestoreLayout)+preRestoreUTC)
	dir := base
	for i := 2; i < 1000; i++ {
		err := os.Mkdir(dir, 0o755)
		if err == nil {
			return dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		dir = fmt.Sprintf("%s-%d", base, i)
	}
	return "", errors.New("too many pre-restore directories with the same timestamp")
}

func prunePreRestore(backupDir string) {
	found, _ := filepath.Glob(filepath.Join(backupDir, "pre-restore-*"))
	// An empty directory (a leftover of an interrupted restore) holds nothing to
	// keep: remove it rather than let it take one of the keepPreRestore places.
	var dirs []string
	for _, d := range found {
		if ents, err := os.ReadDir(d); err == nil && len(ents) == 0 {
			_ = os.Remove(d)
			continue
		}
		dirs = append(dirs, d)
	}
	// Oldest first by the parsed timestamp, then the -N suffix (as a number:
	// -10 is newer than -2). A name that does not parse is not ours: never pruned.
	type entry struct {
		dir string
		at  time.Time
		n   int
	}
	var list []entry
	for _, d := range dirs {
		if at, n, ok := preRestoreKey(filepath.Base(d)); ok {
			list = append(list, entry{d, at, n})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].at.Equal(list[j].at) {
			return list[i].at.Before(list[j].at)
		}
		return list[i].n < list[j].n
	})
	for len(list) > keepPreRestore {
		_ = os.RemoveAll(list[0].dir)
		list = list[1:]
	}
}

// preRestoreKey parses pre-restore-<YYYYMMDD-HHMMSS>[Z][-N]: the time, as UTC
// with the Z and as the server's local time without it (the zone-less names of
// older versions), normalised to UTC so both kinds sort together; and N (1
// without a suffix).
func preRestoreKey(name string) (at time.Time, n int, ok bool) {
	rest, ok := strings.CutPrefix(name, "pre-restore-")
	if !ok || len(rest) < len(preRestoreLayout) {
		return time.Time{}, 0, false
	}
	loc := localZone()
	stamp, suf := rest[:len(preRestoreLayout)], rest[len(preRestoreLayout):]
	if after, utc := strings.CutPrefix(suf, preRestoreUTC); utc {
		loc, suf = time.UTC, after
	}
	at, err := time.ParseInLocation(preRestoreLayout, stamp, loc)
	if err != nil {
		return time.Time{}, 0, false
	}
	at = at.UTC()
	n = 1
	if suf != "" {
		digits, ok := strings.CutPrefix(suf, "-")
		if !ok || digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
			return time.Time{}, 0, false
		}
		if n, err = strconv.Atoi(digits); err != nil {
			return time.Time{}, 0, false
		}
	}
	return at, n, true
}
