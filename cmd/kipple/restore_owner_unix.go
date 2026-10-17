//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// Seams for tests.
var (
	geteuid = os.Geteuid
	chown   = chownPlain
)

// fixOwnership guards against restoring as root. The server runs as the
// container's nonroot user; a database written by root is 0600 root-owned and
// serve cannot open it. When running as root it warns and hands the given paths
// to the owner of the data directory (nothing to do if that is root too).
func fixOwnership(out io.Writer, dataDir string, paths ...string) {
	if geteuid() != 0 {
		return
	}
	st, err := os.Stat(dataDir)
	if err != nil {
		return
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid == 0 {
		return
	}
	fmt.Fprintln(out, "Warning: restore is running as root. Run it as Kipple's own user (in Docker, leave out -u root).")
	for _, p := range paths {
		if err := chown(p, int(sys.Uid), int(sys.Gid)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(out, "Warning: could not give %s to the data directory's owner (%v); serve may be unable to open it. Fix it with chown.\n", p, err)
		}
	}
}

// errNotPlain is a path chownPlain will not hand over.
var errNotPlain = errors.New("not a plain file or directory (a link or special file is left alone)")

// chownPlain changes the owner of the file or directory at p itself, never of
// what a link there points to: the data directory belongs to another user, who
// could have put a symbolic or hard link to a system file under one of the
// names restore hands over. It opens p without following a symbolic link and
// checks and changes the opened file, so the name cannot be swapped in between.
func chownPlain(p string, uid, gid int) error {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			return errNotPlain
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return errNotPlain
	case fi.IsDir():
	case fi.Mode().IsRegular() && st.Nlink == 1:
	default:
		return errNotPlain
	}
	return f.Chown(uid, gid)
}
