//go:build !windows && !linux && !darwin && !freebsd

package store

import "errors"

// diskFree is not available here: the migration space check fails open.
func diskFree(string) (uint64, error) {
	return 0, errors.New("store: free disk space is not available on this platform")
}
