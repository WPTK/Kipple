//go:build unix

package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixOwnershipWhenRunningAsRoot(t *testing.T) {
	dir := t.TempDir()
	var chowned []string
	var uid, gid int
	oldE, oldC := geteuid, chown
	t.Cleanup(func() { geteuid, chown = oldE, oldC })
	chown = func(p string, u, g int) error { chowned = append(chowned, p); uid, gid = u, g; return nil }

	var out bytes.Buffer
	geteuid = func() int { return 1000 }
	fixOwnership(&out, dir, "a")
	require.Empty(t, chowned, "not root: nothing to do")
	require.Empty(t, out.String())

	geteuid = func() int { return 0 }
	if os.Getuid() == 0 {
		t.Skip("the data dir is root-owned here, so there is nobody to hand files to")
	}
	fixOwnership(&out, dir, "a", "b")
	require.Equal(t, []string{"a", "b"}, chowned)
	require.Equal(t, os.Getuid(), uid, "files go to the data directory's owner")
	_ = gid
	require.Contains(t, out.String(), "running as root")
}
