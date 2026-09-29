package setup

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeNow struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeNow) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeNow) add(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

var bannerRE = regexp.MustCompile(`[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}`)

func newMgr(t *testing.T, out io.Writer, logs io.Writer) (*Manager, string, *fakeNow) {
	t.Helper()
	dir := t.TempDir()
	clk := &fakeNow{t: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	if logs == nil {
		logs = io.Discard
	}
	m := New(Options{DataDir: dir, Out: out, Logger: slog.New(slog.NewTextHandler(logs, nil)), Now: clk.now})
	require.NoError(t, m.Begin())
	return m, dir, clk
}

func TestBeginWritesTheTokenFileAndBanner(t *testing.T) {
	var out bytes.Buffer
	m, dir, _ := newMgr(t, &out, nil)
	require.True(t, m.Pending())
	require.Empty(t, out.String(), "the banner waits for the port")
	tok, ok, err := ReadToken(dir)
	require.NoError(t, err)
	require.True(t, ok)
	st, err := os.Stat(TokenPath(dir))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}
	m.Announce("1919")
	banner := out.String()
	require.Contains(t, banner, tok)
	require.Contains(t, banner, "http://<host>:1919/#setup="+tok)
	require.Contains(t, banner, "kipple setup-token")
	m.Announce("1919")
	require.Equal(t, banner, out.String(), "printed once")
}

func TestClaimSessionsAndFinish(t *testing.T) {
	m, dir, clk := newMgr(t, io.Discard, nil)
	tok, _, _ := ReadToken(dir)

	c, ok, err := m.Claim("wrong")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, c)

	c1, ok, err := m.Claim(strings.ToLower(tok))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, m.SessionOK(c1))
	require.False(t, m.SessionOK(""))
	require.False(t, m.SessionOK(c1+"x"))

	// A new claim replaces the previous setup session.
	c2, ok, _ := m.Claim(tok)
	require.True(t, ok)
	require.False(t, m.SessionOK(c1))
	require.True(t, m.SessionOK(c2))

	// The session lasts an hour.
	clk.add(SessionTTL - time.Second)
	require.True(t, m.SessionOK(c2))
	clk.add(time.Second)
	require.False(t, m.SessionOK(c2))

	c3, _, _ := m.Claim(tok)
	m.Finish()
	require.False(t, m.Pending())
	require.False(t, m.SessionOK(c3), "no session survives the end of setup")
	_, _, err = m.Claim(tok)
	require.ErrorIs(t, err, ErrNotPending)
	_, ok, err = ReadToken(dir)
	require.NoError(t, err)
	require.False(t, ok, "the token file is gone")
	m.Finish() // idempotent
}

func TestRotationAfterRepeatedFailures(t *testing.T) {
	var out, logs bytes.Buffer
	m, dir, _ := newMgr(t, &out, &logs)
	m.Announce("1919")
	old, _, _ := ReadToken(dir)
	for i := 0; i < DefaultRotateAfter-1; i++ {
		_, ok, err := m.Claim("0000-0000-0000-0000-0000-0000")
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.NotContains(t, logs.String(), "rotated")
	_, ok, _ := m.Claim("nope")
	require.False(t, ok)
	require.Contains(t, logs.String(), "setup token rotated after repeated failures")
	require.NotContains(t, logs.String(), old, "the log never carries a token")

	now, _, _ := ReadToken(dir)
	require.NotEqual(t, old, now)
	require.Len(t, bannerRE.FindAllString(out.String(), -1), 4, "two banners, the code twice in each")
	require.Contains(t, out.String(), now)
	_, ok, _ = m.Claim(old)
	require.False(t, ok, "the old token is dead")
	_, ok, _ = m.Claim(now)
	require.True(t, ok)
}

func TestStaleTokenFileIsReplacedAndRemoved(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(TokenPath(dir), []byte("AAAA-AAAA-AAAA-AAAA-AAAA-AAAA\n"), 0o644))
	m := New(Options{DataDir: dir, Logger: quiet})
	require.NoError(t, m.Begin())
	tok, ok, err := ReadToken(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, "AAAA-AAAA-AAAA-AAAA-AAAA-AAAA", tok)

	require.NoError(t, os.WriteFile(TokenPath(dir), []byte("junk"), 0o600))
	_, _, err = ReadToken(dir)
	require.Error(t, err)
	require.NoError(t, RemoveTokenFile(dir))
	require.NoError(t, RemoveTokenFile(dir), "a missing file is fine")
	_, err = os.Stat(filepath.Join(dir, TokenFile))
	require.True(t, os.IsNotExist(err))
}

// A Manager that never began (or a nil one) is not pending.
func TestNotPendingByDefault(t *testing.T) {
	var nilM *Manager
	require.False(t, nilM.Pending())
	m := New(Options{Logger: quiet})
	require.False(t, m.Pending())
	_, _, err := m.Claim("x")
	require.ErrorIs(t, err, ErrNotPending)
	require.False(t, m.SessionOK("x"))
}

// Concurrent claims: every right one gets a session, only the last stays valid.
func TestConcurrentClaims(t *testing.T) {
	m, dir, _ := newMgr(t, io.Discard, nil)
	tok, _, _ := ReadToken(dir)
	var wg sync.WaitGroup
	cookies := make([]string, 32)
	for i := range cookies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := tok
			if i%2 == 1 {
				in = "0000-0000-0000-0000-0000-0000"
			}
			c, _, err := m.Claim(in)
			require.NoError(t, err)
			cookies[i] = c
		}(i)
	}
	wg.Wait()
	valid := 0
	for _, c := range cookies {
		if m.SessionOK(c) {
			valid++
		}
	}
	require.Equal(t, 1, valid, "one setup session at a time")
}
