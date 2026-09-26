package main

import (
	"errors"
	"io/fs"
	"os"
)

// ensureDataDir creates the data directory 0700 when it does not exist: it
// holds the live database, its backups and the image cache, none of which is
// anyone else's business. An existing directory is used as it is, whatever its
// mode (the image creates /data itself, and an operator may have chosen
// otherwise). When the directory is new its mode is set again, best effort, in
// case the umask narrowed it oddly.
func ensureDataDir(dir string) error {
	_, err := os.Stat(dir)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !existed {
		_ = os.Chmod(dir, 0o700)
	}
	return nil
}
