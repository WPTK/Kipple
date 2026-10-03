package auth

import (
	"bytes"
	"context"
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

// fakePacing makes f's pacing wait advance the fake clock *now instead of
// sleeping, and counts the waits.
func fakePacing(f *FailureTracker, now *time.Time, waits *int) {
	var mu sync.Mutex
	f.MaxWait = time.Hour // the fake clock makes every wait instant
	f.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return *now }
	f.After = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		*now = now.Add(d)
		if waits != nil {
			*waits++
		}
		mu.Unlock()
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
}

// fail admits and fails one attempt, reporting whether it was admitted.
func fail(f *FailureTracker, ip string) bool {
	if !f.Acquire(context.Background(), ip) {
		return false
	}
	f.Finish(ip, true)
	return true
}

// Only failures count. The first five are free, then the wait doubles from 2 s to
// the 60 s cap; an attempt waits out its delay instead of being refused; a
// verified success clears the client; the count fades only after a quiet hour.
func TestFailureTrackerEscalatingDelay(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	waits := 0
	f := NewFailureTracker()
	fakePacing(f, &now, &waits)
	for i := 0; i < 5; i++ {
		require.True(t, fail(f, "a"), "attempt %d", i+1)
	}
	require.Zero(t, waits, "inside the budget nothing waits")
	for _, want := range []time.Duration{2, 4, 8, 16, 32, 60, 60, 60} {
		start := now
		require.True(t, fail(f, "a"), "over budget: admitted after the delay, not refused")
		require.Equal(t, want*time.Second, now.Sub(start))
	}
	require.Equal(t, 8, waits)
	require.Equal(t, 13, f.Count("a"))

	require.True(t, fail(f, "b"), "per IP")
	require.Equal(t, 8, waits, "another client does not wait")

	// A busy verifier (Finish false) counts nothing and clears nothing.
	require.True(t, f.Acquire(context.Background(), "a"))
	f.Finish("a", false)
	require.Equal(t, 13, f.Count("a"))
	// A verified success clears.
	f.Forget("a")
	require.Zero(t, f.Count("a"))
	w := waits
	require.True(t, fail(f, "a"))
	require.Equal(t, w, waits, "a cleared client starts with a free budget")

	// The count does not restart every few minutes: only a quiet hour fades it.
	for i := 0; i < 10; i++ {
		fail(f, "d")
	}
	now = now.Add(59 * time.Minute)
	require.Equal(t, 10, f.Count("d"))
	require.True(t, fail(f, "d"), "its delay has long passed")
	require.Equal(t, 11, f.Count("d"), "59 quiet minutes later the count is still there")
	w = waits
	start := now
	require.True(t, fail(f, "d"))
	require.Equal(t, w+1, waits, "so the next attempt waits again")
	require.Equal(t, time.Minute, now.Sub(start))
	now = now.Add(61 * time.Minute)
	w = waits
	require.True(t, fail(f, "d"))
	require.Equal(t, w, waits, "an hour with no failure forgets it")
	require.Equal(t, 1, f.Count("d"))
}

// A wait longer than MaxWait is not held: Acquire says no at once and Wait says
// how long the client's own failures still ask for.
func TestFailureTrackerLongWaitIsAnsweredAtOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	fakePacing(f, &now, nil)
	for i := 0; i < 8; i++ {
		fail(f, "a")
	}
	f.MaxWait = 10 * time.Second
	begin := time.Now()
	require.False(t, f.Acquire(context.Background(), "a"), "16 s is over the 10 s bound")
	require.Less(t, time.Since(begin), time.Second, "and it says so at once")
	require.Equal(t, 16*time.Second, f.Wait("a"))
	now = now.Add(10 * time.Second)
	require.Equal(t, 6*time.Second, f.Wait("a"))
	require.True(t, f.Acquire(context.Background(), "a"), "6 s is within the bound")
	f.Finish("a", false)
	require.Zero(t, f.Wait("nobody"))
	require.Empty(t, f.live, "a refused attempt leaves nothing behind")
}

// A second concurrent attempt from one client waits for the first instead of
// failing; one client never has two hashes in flight; the waiting is bounded.
func TestFailureTrackerConcurrentAttemptsWait(t *testing.T) {
	f := NewFailureTracker()
	ctx := context.Background()
	require.True(t, f.Acquire(ctx, "192.0.2.1"))
	require.True(t, f.Acquire(ctx, "192.0.2.2"), "another client is independent")

	got := make(chan bool, 1)
	go func() { got <- f.Acquire(ctx, "::ffff:192.0.2.1") }() // mapped form: same client
	select {
	case <-got:
		t.Fatal("the second attempt must wait for the first, not fail or run alongside it")
	case <-time.After(50 * time.Millisecond):
	}
	f.Finish("192.0.2.1", true)
	require.True(t, <-got, "admitted once the first finished")
	f.Finish("192.0.2.1", false)
	f.Finish("192.0.2.2", false)
	require.Empty(t, f.live, "idle clients are forgotten")

	// Bounded: MaxWait.
	g := NewFailureTracker()
	g.MaxWait = 30 * time.Millisecond
	require.True(t, g.Acquire(ctx, "198.51.100.1"))
	begin := time.Now()
	require.False(t, g.Acquire(ctx, "198.51.100.1"), "gives up after MaxWait")
	require.GreaterOrEqual(t, time.Since(begin), 25*time.Millisecond)
	// Bounded: a cancelled request stops waiting.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	require.False(t, g.Acquire(cctx, "198.51.100.1"))
	require.Zero(t, g.Count("198.51.100.1"), "an attempt that never started counts nothing")

	// Bounded: MaxWaiters. A burst of 50 from one client has at most one
	// attempt admitted at a time and at most MaxWaiters waiting; the rest are
	// refused at once.
	h := NewFailureTracker()
	h.MaxWait = 5 * time.Second
	var cur, peak, admitted, refused atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !h.Acquire(ctx, "2001:db8::1") {
				refused.Add(1)
				return
			}
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			admitted.Add(1)
			<-release
			cur.Add(-1)
			h.Finish("2001:db8::1", true)
		}()
	}
	require.Eventually(t, func() bool { return refused.Load() == 50-1-int32(h.MaxWaiters) }, 2*time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, peak.Load(), "one attempt per client at a time")
	require.EqualValues(t, 1+h.MaxWaiters, admitted.Load(), "the waiting ones ran in turn")
}

func TestRateKeyGroupsIPv6By64(t *testing.T) {
	require.Equal(t, "192.0.2.7", RateKey("192.0.2.7"))
	require.Equal(t, "192.0.2.7", RateKey("::ffff:192.0.2.7"))
	require.Equal(t, "2001:db8:1:2::/64", RateKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	require.Equal(t, RateKey("2001:db8:1:2::1"), RateKey("2001:db8:1:2:ffff::1"))
	require.NotEqual(t, RateKey("2001:db8:1:2::1"), RateKey("2001:db8:1:3::1"))
	require.Equal(t, "fe80::/64", RateKey("fe80::1%eth0"))
	require.Equal(t, "not-an-ip", RateKey("not-an-ip"))

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

// A panic inside the password check must not keep the only hashing slot.
func TestVerifierPanicReleasesHashingSlot(t *testing.T) {
	boom := true
	v := NewVerifier([]byte("k"), VerifierOptions{Wait: 50 * time.Millisecond, Check: func(pw, phc string) bool {
		if boom {
			panic("check failed")
		}
		return pw == "pw"
	}})
	require.Panics(t, func() { v.VerifyBusy(t.Context(), "api", "pw", "h") })
	boom = false
	ok, busy := v.VerifyBusy(t.Context(), "api", "pw", "h")
	require.False(t, busy, "the slot was released by the panicking check")
	require.True(t, ok)
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

func TestTrackersEvictOldestWhenFull(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
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
	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
	served := 0
	h := WarnUntrustedProxyHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served++ }),
		trusted, func(r *http.Request) bool { return r.Header.Get("X-Test-Expected") != "" }, log, func() time.Time { return now })
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
	send("198.51.100.7:1", map[string]string{"X-Forwarded-For": "100.64.0.9", "X-Test-Expected": "1"})
	require.Empty(t, buf.String(), "an expected proxy (Tailscale Serve) never warns")

	now = now.Add(59 * time.Minute)
	send("198.51.100.7:1", map[string]string{"X-Forwarded-Proto": "https"})
	require.Empty(t, buf.String(), "rate limited")
	now = now.Add(2 * time.Minute)
	send("198.51.100.7:1", map[string]string{"X-Forwarded-Proto": "https"})
	require.Contains(t, buf.String(), "X-Forwarded-Proto")
	require.Equal(t, 6, served, "requests always pass through")
}
