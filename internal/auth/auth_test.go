package auth

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
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

// fail reserves and fails one attempt, reporting whether it was granted.
func fail(f *FailureTracker, ip string) bool {
	if !f.Reserve(ip) {
		return false
	}
	f.Done(ip)
	return true
}

func TestFailureTrackerBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	f.Now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		require.True(t, fail(f, "a"), "attempt %d", i+1)
	}
	require.False(t, fail(f, "a"), "over budget inside the delay")
	require.Equal(t, 5, f.Count("a"), "a refused attempt is not counted")
	require.True(t, fail(f, "b"), "per IP")
	now = now.Add(2 * time.Second)
	require.True(t, fail(f, "a"), "one attempt per delay")
	require.False(t, fail(f, "a"))
	f.Clear("a")
	require.Equal(t, 0, f.Count("a"))
	require.True(t, fail(f, "a"))
	for i := 0; i < 10; i++ {
		fail(f, "c")
		now = now.Add(2 * time.Second)
	}
	now = now.Add(11 * time.Minute)
	require.True(t, fail(f, "c"), "window expired")
	require.Equal(t, 1, f.Count("c"))
}

func TestFailureTrackerInFlightAndRelease(t *testing.T) {
	f := NewFailureTracker()
	require.True(t, f.Reserve("192.0.2.1"))
	require.False(t, f.Reserve("192.0.2.1"), "one attempt in flight per client")
	require.False(t, f.Reserve("::ffff:192.0.2.1"), "mapped form is the same client")
	require.True(t, f.Reserve("192.0.2.2"))
	f.Release("192.0.2.1")
	require.Equal(t, 0, f.Count("192.0.2.1"), "released attempt is given back")
	require.True(t, f.Reserve("192.0.2.1"))
	f.Clear("192.0.2.1")
	require.True(t, f.Reserve("192.0.2.1"), "Clear ends the attempt")

	// Concurrent burst from one client: exactly one is granted.
	g := NewFailureTracker()
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.Reserve("2001:db8::1") {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, granted.Load())
}

func TestRateKeyGroupsIPv6By64(t *testing.T) {
	require.Equal(t, "192.0.2.7", RateKey("192.0.2.7"))
	require.Equal(t, "192.0.2.7", RateKey("::ffff:192.0.2.7"))
	require.Equal(t, "2001:db8:1:2::/64", RateKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	require.Equal(t, RateKey("2001:db8:1:2::1"), RateKey("2001:db8:1:2:ffff::1"))
	require.NotEqual(t, RateKey("2001:db8:1:2::1"), RateKey("2001:db8:1:3::1"))
	require.Equal(t, "fe80::/64", RateKey("fe80::1%eth0"))
	require.Equal(t, "not-an-ip", RateKey("not-an-ip"))

	// The web login lockout keys the same way.
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })
	for i := 0; i < 10; i++ {
		ok, _ := l.Reserve(fmt.Sprintf("2001:db8:5:6::%x", i+1))
		require.True(t, ok)
	}
	ok, _ := l.Reserve("2001:db8:5:6::ffff")
	require.False(t, ok, "rotating inside one /64 hits the same lockout")
	locked, _ := l.Locked("2001:db8:5:6::1234")
	require.True(t, locked)
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

func TestVerifierSetSecretDropsMemo(t *testing.T) {
	var checks int
	v := NewVerifier(nil, VerifierOptions{Check: func(pw, phc string) bool { checks++; return pw == "pw" }})
	v.SetSecret([]byte("one"))
	require.True(t, v.Verify(t.Context(), "web", "pw", "h"))
	require.True(t, v.Verify(t.Context(), "web", "pw", "h"))
	require.Equal(t, 1, checks, "memo hit")
	v.SetSecret([]byte("one"))
	require.True(t, v.Verify(t.Context(), "web", "pw", "h"))
	require.Equal(t, 1, checks, "same secret keeps the memo")
	v.SetSecret([]byte("two"))
	require.True(t, v.Verify(t.Context(), "web", "pw", "h"))
	require.Equal(t, 2, checks, "a rotated secret forgets remembered logins")
}

func TestVerifierRemembered(t *testing.T) {
	var checks int
	v := NewVerifier([]byte("k"), VerifierOptions{Check: func(pw, phc string) bool { checks++; return pw == "pw" }})
	require.False(t, v.Remembered("api", "pw", "h"), "nothing remembered yet")
	require.True(t, v.Verify(t.Context(), "api", "pw", "h"))
	require.True(t, v.Remembered("api", "pw", "h"))
	require.False(t, v.Remembered("api", "other", "h"))
	require.False(t, v.Remembered("web", "pw", "h"), "per kind")
	require.False(t, v.Remembered("api", "pw", "h2"), "per hash")
	require.Equal(t, 1, checks, "Remembered never hashes")
}

func TestLockoutReserveIsAtomicAndReleasable(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := l.Reserve("1.2.3.4"); ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 10, granted.Load(), "a parallel burst cannot exceed Max reservations")
	ok, left := l.Reserve("1.2.3.4")
	require.False(t, ok)
	require.Positive(t, left)

	l.Release("5.6.7.8") // releasing an unknown IP is harmless
	ok, _ = l.Reserve("5.6.7.8")
	require.True(t, ok)
	l.Release("5.6.7.8")
	for i := 0; i < 10; i++ {
		ok, _ = l.Reserve("5.6.7.8")
		require.True(t, ok, "released reservation did not count, attempt %d", i)
	}
	l.Clear("1.2.3.4")
	ok, _ = l.Reserve("1.2.3.4")
	require.True(t, ok)
}

func TestTrackersEvictOldestWhenFull(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	l := NewLockout(func() time.Time { return now })
	for i := 0; i < maxTracked; i++ {
		now = now.Add(time.Millisecond)
		ok, _ := l.Reserve(fmt.Sprintf("ip-%d", i))
		require.True(t, ok)
	}
	now = now.Add(time.Millisecond)
	for i := 0; i < 10; i++ {
		ok, _ := l.Reserve("newcomer")
		require.True(t, ok)
	}
	ok, _ := l.Reserve("newcomer")
	require.False(t, ok, "a new IP is still tracked when the map is full")
	require.Len(t, l.m, maxTracked)
	_, oldestStillThere := l.m["ip-0"]
	require.False(t, oldestStillThere, "the oldest entry was evicted")

	f := NewFailureTracker()
	f.Now = func() time.Time { return now }
	for i := 0; i < maxTracked; i++ {
		now = now.Add(time.Millisecond)
		fail(f, fmt.Sprintf("ip-%d", i))
	}
	for i := 0; i < 6; i++ {
		now = now.Add(2 * time.Second)
		fail(f, "newcomer")
	}
	require.Equal(t, 6, f.Count("newcomer"))
	require.Len(t, f.m, maxTracked)
}

func TestWarnUntrustedProxyHeaders(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	trusted := []netip.Addr{netip.MustParseAddr("192.0.2.10")}
	served := 0
	h := WarnUntrustedProxyHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }),
		trusted, log, func() time.Time { return now })
	send := func(peer string, hdr map[string]string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	send("198.51.100.7:1", nil)
	send("192.0.2.10:1", map[string]string{"CF-Connecting-IP": "203.0.113.5", "X-Forwarded-Proto": "https"})
	require.Empty(t, buf.String(), "no headers, or a trusted peer, is silent")

	send("198.51.100.7:1", map[string]string{"CF-Connecting-IP": "203.0.113.5"})
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "KIPPLE_TRUSTED_PROXY_IPS")
	require.Contains(t, buf.String(), "198.51.100.7:1")
	require.NotContains(t, buf.String(), "203.0.113.5", "the spoofable value is not logged")

	buf.Reset()
	now = now.Add(59 * time.Minute)
	send("198.51.100.7:1", map[string]string{"X-Forwarded-Proto": "https"})
	require.Empty(t, buf.String(), "rate limited")
	now = now.Add(2 * time.Minute)
	send("198.51.100.7:1", map[string]string{"X-Forwarded-Proto": "https"})
	require.Contains(t, buf.String(), "X-Forwarded-Proto")
	require.Equal(t, 5, served, "requests always pass through")
}
