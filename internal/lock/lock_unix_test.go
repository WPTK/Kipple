//go:build unix

package lock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// A lock name that points elsewhere is refused before anything is written:
// a root `kipple restore` must not truncate the file it leads to.
func TestAcquireRefusesWhatIsNotAPlainFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("keep me\n"), 0o600))

	link := filepath.Join(dir, "symlink.lock")
	require.NoError(t, os.Symlink(target, link))
	_, err := Acquire(link)
	require.ErrorContains(t, err, "symbolic link")

	dangling := filepath.Join(dir, "dangling.lock")
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing"), dangling))
	_, err = Acquire(dangling)
	require.ErrorContains(t, err, "symbolic link")
	require.NoFileExists(t, filepath.Join(dir, "missing"), "a dangling link is not created through")

	// A hard link is refused when running as root (the threat), and accepted
	// otherwise (a hard-linked snapshot of the data directory still serves).
	hard := filepath.Join(dir, "hard.lock")
	require.NoError(t, os.Link(target, hard))
	oldE := geteuid
	t.Cleanup(func() { geteuid = oldE })
	geteuid = func() int { return 0 }
	_, err = Acquire(hard)
	require.ErrorIs(t, err, errNotPlain)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep me\n", string(b), "refused before anything is written")
	geteuid = func() int { return 1000 }
	l, err := Acquire(hard)
	require.NoError(t, err, "unprivileged, a hard-linked lock file is fine")
	require.NoError(t, l.Release())
	require.NoError(t, os.WriteFile(target, []byte("keep me\n"), 0o600)) // Acquire wrote its pid through the link
	geteuid = oldE

	fifo := filepath.Join(dir, "fifo.lock")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	_, err = Acquire(fifo)
	require.ErrorIs(t, err, errNotPlain)

	sub := filepath.Join(dir, "dir.lock")
	require.NoError(t, os.Mkdir(sub, 0o700))
	_, err = Acquire(sub)
	require.Error(t, err)

	b, err = os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep me\n", string(b), "the target is untouched")
}
