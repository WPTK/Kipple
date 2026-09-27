//go:build linux

package store

import "syscall"

// diskFree is the bytes available to an unprivileged process on the volume that holds dir.
func diskFree(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	unit := uint64(st.Frsize) // the fragment size is the unit of Bavail; older kernels report 0
	if unit == 0 {
		unit = uint64(st.Bsize)
	}
	return uint64(st.Bavail) * unit, nil //nolint:unconvert // field types differ per arch
}
