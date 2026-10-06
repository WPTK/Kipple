package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// KeepPreRestore is how many backup/pre-restore-* directories are kept.
const KeepPreRestore = 3

// Swap installs the verified database tmp as <dataDir>/kipple.db: the live
// database moves into a new backup/pre-restore-<ts>/ directory first (unless it
// is provably empty, see keepLive), then tmp is renamed over kipple.db. Any failure puts
// everything back. pre is the directory the old database went to, "" when there
// was none to keep. It is the one swap of `kipple restore` and of a restore
// confirmed in the setup wizard.
func Swap(dataDir, tmp string, now time.Time) (pre string, err error) {
	pre, undo, err := keepLive(dataDir, now)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(dataDir, "kipple.db")); err != nil {
		undo()
		return "", fmt.Errorf("install the restored database (the current one was put back): %w", err)
	}
	return pre, nil
}

// keepLive moves the live database into a new pre-restore directory. undo puts
// it back. From one clean open of the file (journal mode DELETE, which
// checkpoints it and removes its -wal and -shm, so the database is one file and
// moves with one atomic rename) it first learns whether the database is
// provably empty (liveState). Only then is it deleted instead of kept: what
// setup mode creates holds nothing, and empty databases must never take the
// newest-3 places of real safety copies. On any doubt (a read error, a row
// anywhere, a file that is not a Kipple database, a failed open) it is kept.
//
// A file SQLite takes for garbage is opened with its -wal and -shm beside it,
// and SQLite deletes those, so copies of them wait in the pre-restore directory
// meanwhile and go back when the open found nothing usable.
func keepLive(dataDir string, now time.Time) (pre string, undo func(), err error) {
	live := filepath.Join(dataDir, "kipple.db")
	backupDir := filepath.Join(dataDir, "backup")
	cleanCopies(backupDir)
	if _, err := os.Stat(live); err != nil {
		// No main file: any -wal or -shm is moved as it is, below.
		return keepFiles(dataDir, live, "", now)
	}
	if pre, err = newPreRestoreDir(backupDir, now); err != nil {
		return "", nil, fmt.Errorf("pre-restore directory: %w", err)
	}
	copies := map[string]string{} // suffix -> copy in pre
	for _, s := range []string{"-wal", "-shm"} {
		if c, err := copyAside(live+s, pre); err == nil && c != "" {
			copies[s] = c
		}
	}
	state := liveState(live)
	for s, c := range copies {
		if state == liveUnknown {
			if _, err := os.Stat(live + s); err != nil {
				_ = os.Rename(c, live+s) // the open took it: put it back
				continue
			}
		}
		_ = os.Remove(c)
	}
	if state == liveEmpty {
		for _, s := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(live + s)
		}
		_ = os.Remove(pre)
		return "", func() {}, nil
	}
	return keepFiles(dataDir, live, pre, now)
}

// keepFiles moves kipple.db, -wal and -shm (those that exist) into pre, creating
// it when pre is "".
func keepFiles(dataDir, live, pre string, now time.Time) (string, func(), error) {
	var done []string // suffixes moved so far
	undo := func() {
		for _, s := range done {
			_ = os.Rename(filepath.Join(pre, "kipple.db"+s), live+s)
		}
		// Remove the now-empty pre-restore directory: left behind, it would count
		// toward KeepPreRestore and prune a real safety copy early. os.Remove
		// refuses a non-empty directory, so a file that failed to move back stays.
		if pre != "" {
			_ = os.Remove(pre)
		}
	}
	for _, s := range []string{"", "-wal", "-shm"} {
		if _, err := os.Stat(live + s); err != nil {
			continue
		}
		if pre == "" {
			var err error
			if pre, err = newPreRestoreDir(filepath.Join(dataDir, "backup"), now); err != nil {
				return "", nil, fmt.Errorf("pre-restore directory: %w", err)
			}
		}
		if err := os.Rename(live+s, filepath.Join(pre, "kipple.db"+s)); err != nil {
			undo()
			return "", nil, fmt.Errorf("keep the current database: %w", err)
		}
		done = append(done, s)
	}
	if len(done) == 0 {
		if pre != "" {
			_ = os.Remove(pre)
		}
		return "", func() {}, nil
	}
	return pre, undo, nil
}

// copyPrefix names the copies of a -wal or -shm made while a database is opened
// to be looked at; cleanCopies removes any a crash left.
const copyPrefix = "walcopy-"

// copyAside copies src into dir under a unique name ("" when src is absent).
func copyAside(src, dir string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", nil
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, copyPrefix+"*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// cleanCopies removes copies a crash left in the pre-restore directories.
func cleanCopies(backupDir string) {
	left, _ := filepath.Glob(filepath.Join(backupDir, "pre-restore-*", copyPrefix+"*"))
	for _, f := range left {
		_ = os.Remove(f)
	}
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
// overwrite) a directory. PrunePreRestore orders them by preRestoreKey.
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

// PrunePreRestore keeps the newest KeepPreRestore pre-restore directories in
// backupDir. local is the zone the zone-less names of older versions were
// written in (the server's local time).
func PrunePreRestore(backupDir string, local *time.Location) {
	cleanCopies(backupDir)
	found, _ := filepath.Glob(filepath.Join(backupDir, "pre-restore-*"))
	// An empty directory (a leftover of an interrupted restore) holds nothing to
	// keep: remove it rather than let it take one of the KeepPreRestore places.
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
		if at, n, ok := preRestoreKey(filepath.Base(d), local); ok {
			list = append(list, entry{d, at, n})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].at.Equal(list[j].at) {
			return list[i].at.Before(list[j].at)
		}
		return list[i].n < list[j].n
	})
	for len(list) > KeepPreRestore {
		_ = os.RemoveAll(list[0].dir)
		list = list[1:]
	}
}

// preRestoreKey parses pre-restore-<YYYYMMDD-HHMMSS>[Z][-N]: the time, as UTC
// with the Z and in local without it (the zone-less names of older versions),
// normalised to UTC so both kinds sort together; and N (1 without a suffix).
func preRestoreKey(name string, local *time.Location) (at time.Time, n int, ok bool) {
	rest, ok := strings.CutPrefix(name, "pre-restore-")
	if !ok || len(rest) < len(preRestoreLayout) {
		return time.Time{}, 0, false
	}
	loc := local
	if loc == nil {
		loc = time.Local
	}
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

type liveKind int

const (
	liveUnknown liveKind = iota // absent, unreadable or not provably empty: keep whatever is there
	liveEmpty                   // provably empty: a Kipple database with no account, feed or item
	liveHolds                   // a database that holds something
)

// liveState opens the live database once, checkpointing it into one file, and
// says whether it is provably empty: a Kipple database (application id) whose
// account, feeds and items tables all read as zero rows without an error, and
// that passes a quick integrity check (a damaged b-tree can read as no rows).
// Everything else is liveHolds or liveUnknown, and is kept.
func liveState(live string) liveKind {
	if _, err := os.Stat(live); err != nil {
		return liveUnknown
	}
	db, err := openFile(live)
	if err != nil {
		return liveUnknown
	}
	defer db.Close()
	var appID int
	if err := db.QueryRow("PRAGMA application_id").Scan(&appID); err != nil || appID != store.ApplicationID {
		return liveUnknown
	}
	for _, table := range []string{"account", "feeds", "items"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			return liveUnknown
		}
		if n > 0 {
			return liveHolds
		}
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return liveUnknown
	}
	return liveEmpty
}
