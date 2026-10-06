package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// KeepPreRestore is how many backup/pre-restore-* directories are kept.
const KeepPreRestore = 3

// Swap installs the verified database tmp as <dataDir>/kipple.db: the live
// database (and its -wal and -shm) moves into a new backup/pre-restore-<ts>/
// directory first, then tmp is renamed over kipple.db. Any failure puts
// everything back. pre is the directory the old database went to, "" when there
// was none to keep. It is the one swap of `kipple restore` and of a restore
// confirmed in the setup wizard.
func Swap(dataDir, tmp string, now time.Time) (pre string, err error) {
	live := filepath.Join(dataDir, "kipple.db")
	var done []string // suffixes moved so far
	rollback := func() {
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
		if len(done) == 0 {
			var err error
			if pre, err = newPreRestoreDir(filepath.Join(dataDir, "backup"), now); err != nil {
				return "", fmt.Errorf("pre-restore directory: %w", err)
			}
		}
		if err := os.Rename(live+s, filepath.Join(pre, "kipple.db"+s)); err != nil {
			rollback()
			return "", fmt.Errorf("keep the current database: %w", err)
		}
		done = append(done, s)
	}
	if err := os.Rename(tmp, live); err != nil {
		rollback()
		return "", fmt.Errorf("install the restored database (the current one was put back): %w", err)
	}
	if len(done) == 0 {
		return "", nil
	}
	return pre, nil
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
