//go:build !windows

package lock

import (
	"errors"
	"os"
	"syscall"
)

var errWouldBlock = errors.New("would block")

// openNoFollow makes the open fail on a symbolic link, and never wait on a
// FIFO planted under the lock's name.
const openNoFollow = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// hardLinked reports whether another name links to the file.
func hardLinked(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

func tryLock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return errWouldBlock
		default:
			return err
		}
	}
}

func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
