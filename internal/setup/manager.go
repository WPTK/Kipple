package setup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TokenFile is the name of the file in the data directory that holds the
// current setup token while setup is pending, for `kipple setup-token`.
const TokenFile = "setup-token"

// Defaults of Options.
const (
	// DefaultRotateAfter is how many wrong tokens one process accepts before the
	// token is replaced (design 5.1): at 120 bits this is about noise and log
	// volume, not feasibility.
	DefaultRotateAfter = 100
	// SessionTTL is the life of the setup session a claim starts (the
	// kipple_setup cookie's Max-Age).
	SessionTTL = time.Hour
	// RotateEvery caps rotations: a flood of wrong tokens from many addresses
	// replaces the owner's code at most once an hour (and prints one banner).
	RotateEvery = time.Hour
)

// Options configures a Manager.
type Options struct {
	// DataDir holds TokenFile. Empty keeps the token in memory only (tests).
	DataDir string
	// Out receives the banner (stderr in production), outside slog on purpose:
	// KIPPLE_LOG_LEVEL=error cannot hide it and log shippers get a plain line
	// rather than a searchable field. Nil discards it.
	Out    io.Writer
	Logger *slog.Logger
	Now    func() time.Time
	// RotateAfter defaults to DefaultRotateAfter.
	RotateAfter int
}

// Manager is setup mode for one process: the token (kept only as a hash), the
// global failure count, the one setup session a claim starts, and the one-way
// pending flag. A Manager that never called Begin is not pending.
type Manager struct {
	o       Options
	pending atomic.Bool

	mu         sync.Mutex
	hash       [32]byte
	failures   int
	port       string
	issuedAt   time.Time
	session    [32]byte // sha256 of the kipple_setup cookie value
	sessionExp time.Time
	rotatedAt  time.Time // the last rotation after failures (at most one per RotateEvery)
	// unprinted is a token made before the port was known, held in plain text
	// only until Announce prints it.
	unprinted string
}

// New builds a Manager; call Begin to enter setup mode.
func New(o Options) *Manager {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.RotateAfter <= 0 {
		o.RotateAfter = DefaultRotateAfter
	}
	return &Manager{o: o}
}

// Begin enters setup mode: a fresh token, kept as a hash in memory and written
// to <data>/setup-token (0600). A token file that cannot be written is a
// warning, not a failure: the banner still shows the token.
func (m *Manager) Begin() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.newTokenLocked(); err != nil {
		return err
	}
	m.pending.Store(true)
	m.o.Logger.Info("setup pending: no account yet; the setup code is printed on standard error (or run `kipple setup-token`)")
	return nil
}

// newTokenLocked replaces the token and its file and returns the token.
func (m *Manager) newTokenLocked() (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	norm, _ := NormalizeToken(tok)
	m.hash = tokenHash(norm)
	m.issuedAt = m.o.Now()
	m.failures = 0
	if m.o.DataDir != "" {
		if err := writeTokenFile(m.o.DataDir, tok); err != nil {
			m.o.Logger.Warn("setup: cannot write the setup token file; `kipple setup-token` will not show it", "err", err)
		}
	}
	m.printBannerLocked(tok)
	return tok, nil
}

// Announce records the port the server listens on (for the banner's URL) and
// prints the banner of a token made before the port was known. Call it once
// the listener is up; tokens made later (a rotation) are printed at once.
func (m *Manager) Announce(port string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.port = port
	tok := m.unprinted
	m.unprinted = ""
	if tok != "" && m.pending.Load() {
		m.printBannerLocked(tok)
	}
}

func (m *Manager) printBannerLocked(tok string) {
	if m.port == "" {
		m.unprinted = tok // Announce prints it once the port is known
		return
	}
	var b strings.Builder
	b.WriteString("\n  Kipple is not set up yet. To claim it, open Kipple in a browser and enter this setup code:\n\n")
	fmt.Fprintf(&b, "      %s\n\n", tok)
	fmt.Fprintf(&b, "  or open http://<host>:%s/#setup=%s\n", m.port, tok)
	b.WriteString("  (<host> is this machine's address, e.g. 127.0.0.1). To show the code again, run\n")
	b.WriteString("  `kipple setup-token` (Docker: `docker exec <container> /kipple setup-token`).\n\n")
	_, _ = io.WriteString(m.o.Out, b.String())
}

// Pending reports whether setup mode is still open (no account yet).
func (m *Manager) Pending() bool { return m != nil && m.pending.Load() }

// IssuedAt is when the current token was made (the hint in GET /api/setup/state).
func (m *Manager) IssuedAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.issuedAt
}

// ErrNotPending is Claim's answer once setup has finished.
var ErrNotPending = errors.New("setup: not pending")

// Claim checks a hand-typed token. On a match it starts a new setup session,
// replacing any earlier one (the token holder is the authority), and returns
// the cookie value. A mismatch counts towards the global rotation: after
// RotateAfter wrong tokens the token is replaced and the new one printed.
func (m *Manager) Claim(in string) (cookie string, ok bool, err error) {
	return m.claim(in, true)
}

// ClaimUncounted is Claim for an address the caller has already locked out: a
// match still starts the session, a mismatch does not count towards the
// rotation (so one noisy client cannot keep replacing the owner's code).
func (m *Manager) ClaimUncounted(in string) (cookie string, ok bool, err error) {
	return m.claim(in, false)
}

func (m *Manager) claim(in string, count bool) (cookie string, ok bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.pending.Load() {
		return "", false, ErrNotPending
	}
	if !tokenMatches(in, m.hash) {
		if !count {
			return "", false, nil
		}
		m.failures++
		now := m.o.Now()
		if m.failures >= m.o.RotateAfter && (m.rotatedAt.IsZero() || now.Sub(m.rotatedAt) >= RotateEvery) {
			if _, err := m.newTokenLocked(); err != nil {
				return "", false, err
			}
			m.rotatedAt = now
			m.o.Logger.Warn("setup token rotated after repeated failures; the new one is printed on standard error (or run `kipple setup-token`)", "failures", m.o.RotateAfter)
		}
		return "", false, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", false, err
	}
	cookie = base64.RawURLEncoding.EncodeToString(b)
	m.session = sha256.Sum256([]byte(cookie))
	m.sessionExp = m.o.Now().Add(SessionTTL)
	return cookie, true, nil
}

// SessionOK reports whether cookie is the current, unexpired setup session.
func (m *Manager) SessionOK(cookie string) bool {
	if cookie == "" || !m.Pending() {
		return false
	}
	sum := sha256.Sum256([]byte(cookie))
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionExp.IsZero() || !m.o.Now().Before(m.sessionExp) {
		return false
	}
	return subtle.ConstantTimeCompare(sum[:], m.session[:]) == 1
}

// Finish leaves setup mode for good (the account row exists): the flag flips
// once, the token and session are wiped and the token file is removed. Safe to
// call more than once.
func (m *Manager) Finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending.Store(false)
	m.hash = [32]byte{}
	m.session = [32]byte{}
	m.sessionExp = time.Time{}
	m.unprinted = ""
	if m.o.DataDir != "" {
		if err := RemoveTokenFile(m.o.DataDir); err != nil {
			m.o.Logger.Warn("setup: cannot remove the setup token file", "err", err)
		}
	}
}

// TokenPath is the setup token file in dataDir.
func TokenPath(dataDir string) string { return filepath.Join(dataDir, TokenFile) }

// writeTokenFile replaces the token file: a stale one is removed first and the
// new one is created exclusively, owner-only (mode 0600; on Windows, where the
// mode means nothing, a protected DACL for the current user alone, set before
// the token is written: restrictToOwner).
func writeTokenFile(dataDir, tok string) error {
	p := TokenPath(dataDir)
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a fixed name in the data directory
	if err != nil {
		return err
	}
	if err := restrictToOwner(p); err != nil {
		_ = f.Close()
		_ = os.Remove(p)
		return err
	}
	_, werr := f.WriteString(tok + "\n")
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(p)
		return err
	}
	return nil
}

// RemoveTokenFile removes a setup token file (a missing one is fine). A server
// that starts with an account calls it, so a token left by a crash never lingers.
func RemoveTokenFile(dataDir string) error {
	if err := os.Remove(TokenPath(dataDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReadToken reads the token file: ok is false when there is none (no setup
// pending, or it could not be written).
func ReadToken(dataDir string) (tok string, ok bool, err error) {
	b, err := os.ReadFile(TokenPath(dataDir)) // #nosec G304 -- a fixed name in the data directory
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	norm, valid := NormalizeToken(strings.TrimSpace(string(b)))
	if !valid {
		return "", false, fmt.Errorf("setup: %s does not hold a setup token", TokenPath(dataDir))
	}
	return FormatToken(norm), true, nil
}
