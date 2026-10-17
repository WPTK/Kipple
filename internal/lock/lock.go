// Package lock is the data-directory lock (design §2.6): `kipple serve` holds
// an exclusive OS lock on <data>/kipple.lock for its whole life, and
// `kipple restore` takes the same lock, so a restore can never run under a live
// server and two servers can never share one data directory.
//
// The lock is an advisory OS file lock (flock on Unix, LockFileEx on Windows),
// so it disappears with the process, even after a crash or kill -9: there is
// no stale lock file to clean up. Two containers that share the data volume
// exclude each other too, because they share the kernel.
package lock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("lock: held by another process")

// Lock is a held lock. Release it (or exit) to drop it.
type Lock struct {
	f *os.File
}

// Acquire takes the exclusive lock on path without waiting, creating the file
// if needed. It returns ErrLocked when another process holds it. The holder's
// pid is written into the file as a courtesy for humans; it is not the lock.
//
// path must be a plain file of its own: a symbolic link is not followed (on
// Unix) and a hard-linked or special file is refused before anything is
// written. `kipple restore` may run as root in a directory another user owns,
// and must not truncate a file that name points somewhere else.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("lock: %s is a symbolic link, which Kipple does not follow: remove it and try again", path)
		}
		return nil, fmt.Errorf("lock: open %s: %w", path, err)
	}
	if err := plainFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock: %s: %w (remove it and try again)", path, err)
	}
	if err := tryLock(f); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock: %s: %w", path, err)
	}
	// Best effort: the pid is informational.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

// errNotPlain is a lock path that is not a regular file with a single link.
var errNotPlain = errors.New("not a plain file")

// plainFile checks the opened file itself (not the name, which could change):
// a regular file that no other name links to.
func plainFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || hardLinked(fi) {
		return errNotPlain
	}
	return nil
}

// Release drops the lock. It is safe to call more than once. The file stays in
// place (removing it would race with another process's Acquire).
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	unlockErr := unlock(f)
	return errors.Join(unlockErr, f.Close())
}
