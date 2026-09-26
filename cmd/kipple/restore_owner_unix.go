//go:build unix

package main

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// Seams for tests.
var (
	geteuid = os.Geteuid
	chown   = os.Chown
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
