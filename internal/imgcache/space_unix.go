//go:build !windows

package imgcache

import "syscall"

// diskSpace is the bytes available to an unprivileged process on the volume that
// holds dir, and the volume's total size.
func diskSpace(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)                                     //nolint:unconvert,gosec // field types differ per OS; Bsize is never negative
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil //nolint:unconvert // field types differ per OS
}
