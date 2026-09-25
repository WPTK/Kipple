package auth

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHashAndCheck(t *testing.T) {
	phc, err := HashPassword("hunter2")
	require.NoError(t, err)
	require.Contains(t, phc, "$argon2id$v=19$m=19456,t=2,p=1$")
	require.True(t, CheckPassword("hunter2", phc))
	require.False(t, CheckPassword("hunter3", phc))
	require.False(t, CheckPassword("hunter2", "garbage"))
	require.False(t, CheckPassword("hunter2", "$argon2id$v=19$m=99999999,t=2,p=1$AAAA$AAAA"))
	other, _ := HashPassword("hunter2")
	require.NotEqual(t, phc, other, "salted")
}

func TestVerifierRealArgon2WithMemo(t *testing.T) {
	phc, err := HashPassword("pw")
	require.NoError(t, err)
	v := NewVerifier([]byte("secret"), VerifierOptions{})
	require.False(t, v.Verify(t.Context(), "api", "nope", phc))
	require.True(t, v.Verify(t.Context(), "api", "pw", phc))
	require.True(t, v.Verify(t.Context(), "api", "pw", phc), "memo hit")
	require.False(t, v.Verify(t.Context(), "web", "", phc))
	phc2, _ := HashPassword("pw2")
	require.False(t, v.Verify(t.Context(), "api", "pw", phc2), "memo does not survive a password change")
}

func TestFailureTracker(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	f.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		require.Equal(t, time.Duration(0), f.Fail("a"), "failure %d", i+1)
	}
	require.Equal(t, 2*time.Second, f.Fail("a"))
	require.Equal(t, time.Duration(0), f.Fail("b"), "per IP")
	f.Clear("a")
	require.Equal(t, 0, f.Count("a"))
	require.Equal(t, time.Duration(0), f.Fail("a"))
	for i := 0; i < 10; i++ {
		f.Fail("c")
	}
	now = now.Add(11 * time.Minute)
	require.Equal(t, time.Duration(0), f.Fail("c"), "window expired")
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.20:1234"
	r.Header.Set("CF-Connecting-IP", "203.0.113.9")
	require.Equal(t, "192.0.2.20", ClientIP(r, nil), "untrusted peer: header ignored")
	trusted := []netip.Addr{netip.MustParseAddr("192.0.2.20")}
	require.Equal(t, "203.0.113.9", ClientIP(r, trusted))
	r.Header.Set("CF-Connecting-IP", "junk")
	require.Equal(t, "192.0.2.20", ClientIP(r, trusted))
}

func TestGeneratePassword(t *testing.T) {
	a, err := GeneratePassword(24)
	require.NoError(t, err)
	b, _ := GeneratePassword(24)
	require.Len(t, a, 24)
	require.NotEqual(t, a, b)
	require.Regexp(t, `^[a-km-np-zA-HJ-NP-Z2-9]{24}$`, a)
	_, err = GeneratePassword(0)
	require.Error(t, err)
}
