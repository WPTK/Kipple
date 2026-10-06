package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Manager is setup mode for one process: the one-way flag "no account yet". An
// unclaimed instance has no account row and nothing else; there is no secret to
// claim it with. A nil Manager is not pending.
type Manager struct {
	pending atomic.Bool
	once    sync.Once
	then    func()
}

// NewPending returns a Manager in setup mode. then (may be nil) runs once, the
// first time Finish is called: it is how the background work (fetching,
// maintenance) starts only after an account exists.
func NewPending(then func()) *Manager {
	m := &Manager{then: then}
	m.pending.Store(true)
	return m
}

// Pending reports whether setup mode is still open (no account yet).
func (m *Manager) Pending() bool { return m != nil && m.pending.Load() }

// Finish leaves setup mode for good (the account row exists). Safe to call more
// than once; the callback of NewPending runs on the first call only.
func (m *Manager) Finish() {
	if m == nil {
		return
	}
	m.pending.Store(false)
	m.once.Do(func() {
		if m.then != nil {
			m.then()
		}
	})
}

// RemoveStaleTokenFile deletes the `setup-token` file that Kipple versions
// before 0.7 kept in the data directory (a missing one is fine). Those versions
// asked for a setup code; this one needs none, so a leftover file is only a
// stale secret. Run at every start; removed at 1.0.
func RemoveStaleTokenFile(dataDir string) error {
	if err := os.Remove(filepath.Join(dataDir, "setup-token")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// NoEnvAccountFile is the file in the data directory that makes a start ignore
// KIPPLE_USERNAME and KIPPLE_PASSWORD while no account exists. A reset writes it
// when you choose to keep those variables in your compose file or .env; its
// presence is the whole state, and it goes as soon as an account exists.
const NoEnvAccountFile = "no-env-account"

// IgnoreEnvAccount reports whether the start must skip creating the account
// from the environment.
func IgnoreEnvAccount(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, NoEnvAccountFile))
	return err == nil
}

// SetIgnoreEnvAccount writes (on) or removes (off) NoEnvAccountFile.
func SetIgnoreEnvAccount(dataDir string, on bool) error {
	p := filepath.Join(dataDir, NoEnvAccountFile)
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, []byte("Kipple ignores KIPPLE_USERNAME and KIPPLE_PASSWORD until an account exists.\n"), 0o600)
}
