package store

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Stored user agents are cut on a rune boundary, never mid-character.
func TestTruncateOnRuneBoundary(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", truncate("abc", 5))
	require.Equal(t, "abc", truncate("abcdef", 3))
	// "é" is 2 bytes, "€" 3, "😀" 4: every cut inside one backs off to its start.
	for _, r := range []string{"é", "€", "😀"} {
		s := strings.Repeat("a", 299) + r + "tail"
		got := truncate(s, 300)
		require.True(t, utf8.ValidString(got), "%q", r)
		require.Equal(t, strings.Repeat("a", 299), got)
	}
	require.Equal(t, strings.Repeat("a", 298)+"é", truncate(strings.Repeat("a", 298)+"é€", 300))
	// Invalid bytes are still cut at n.
	junk := strings.Repeat("\x80", 400)
	require.Len(t, truncate(junk, 300), 300)
}

// The session and device user agents go through it.
func TestSessionAndDeviceUserAgentsStayValidUTF8(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ua := strings.Repeat("a", 299) + "😀"
	require.NoError(t, e.db.CreateSession(e.ctx, "sid", 100, 1_000_000, ua, "127.0.0.1"))
	got := scalar[string](t, e.db.Reader(), "SELECT user_agent FROM sessions WHERE id = 'sid'")
	require.True(t, utf8.ValidString(got))
	require.Equal(t, strings.Repeat("a", 299), got)
}
