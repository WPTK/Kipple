package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
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
	f.SetMaxWait(time.Hour) // the fake clock makes every wait instant
	fakeClock(f, now, waits)
}

// fakeClock is fakePacing without lifting the wait bound.
func fakeClock(f *FailureTracker, now *time.Time, waits *int) {
	var mu sync.Mutex
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

// A wait longer than the wait bound is not held: Acquire says no at once and Wait says
// how long the client's own failures still ask for.
func TestFailureTrackerLongWaitIsAnsweredAtOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	fakePacing(f, &now, nil)
	for i := 0; i < 8; i++ {
		fail(f, "a")
	}
	f.SetMaxWait(10 * time.Second)
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

	// Bounded: the wait bound.
	g := NewFailureTracker()
	g.SetMaxWait(30 * time.Millisecond)
	require.True(t, g.Acquire(ctx, "198.51.100.1"))
	begin := time.Now()
	require.False(t, g.Acquire(ctx, "198.51.100.1"), "gives up after the wait bound")
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
	h.SetMaxWait(5 * time.Second)
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
	require.Eventually(t, func() bool { return refused.Load() == 50-1-4 }, 2*time.Second, 5*time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, peak.Load(), "one attempt per client at a time")
	require.EqualValues(t, 1+4, admitted.Load(), "the waiting ones ran in turn")
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
		func() []netip.Prefix { return trusted }, func(r *http.Request) bool { return r.Header.Get("X-Test-Expected") != "" }, log, func() time.Time { return now })
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
	require.Contains(t, buf.String(), "trusted proxies (Settings")
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

func TestProxyTooWide(t *testing.T) {
	for p, want := range map[string]bool{
		"0.0.0.0/0": true, "10.0.0.0/7": true, "10.0.0.0/8": false, "8.0.0.0/8": false, "172.16.0.0/12": false, "192.0.2.10/32": false,
		"::/0": true, "2000::/3": true, "2001::/16": true, "2001::/19": true, "2001::/20": false, "2001:db8::/32": false, "fc00::/7": false, "fd00::/8": false, "fe80::/10": false, "fe00::/7": true,
	} {
		require.Equal(t, want, ProxyTooWide(netip.MustParsePrefix(p)), p)
	}
}

// Cloudflare's published ranges (https://www.cloudflare.com/ips-v4 and ips-v6)
// are what a Cloudflare Tunnel or proxy user lists: every one must be accepted.
func TestProxyTooWideAcceptsCloudflareRanges(t *testing.T) {
	v4 := []string{"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22"}
	v6 := []string{"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32"}
	for _, p := range append(v4, v6...) {
		ps, err := ParseProxies(p)
		require.NoError(t, err, p)
		require.False(t, ProxyTooWide(ps[0]), p)
	}
	// All of them in one list too: the rule is per entry, not a total.
	ps, err := ParseProxies(strings.Join(append(v4, v6...), ","))
	require.NoError(t, err)
	require.Len(t, ps, len(v4)+len(v6))
}

// A stored hash is read back from the database, so a hand-edited or restored row must be refused, never panic
// (argon2.IDKey panics on zero rounds) and never be read loosely (trailing text after a number). The salt and tag
// are real ones, so only the field under test can be the reason for the refusal.
func TestCheckPasswordRefusesBadHash(t *testing.T) {
	good, err := HashPassword("pw")
	require.NoError(t, err)
	require.True(t, CheckPassword("pw", good))
	parts := strings.Split(good, "$") // "", argon2id, v=19, cost, salt, tag
	rebuild := func(version, cost, salt, tag string) string {
		return "$argon2id$" + version + "$" + cost + "$" + salt + "$" + tag
	}
	for name, phc := range map[string]string{
		"zero rounds":         rebuild(parts[2], "m=8,t=0,p=1", parts[4], parts[5]),
		"zero threads":        rebuild(parts[2], "m=8,t=1,p=0", parts[4], parts[5]),
		"too many rounds":     rebuild(parts[2], "m=8,t=9,p=1", parts[4], parts[5]),
		"too many threads":    rebuild(parts[2], "m=8,t=1,p=5", parts[4], parts[5]),
		"threads overflow":    rebuild(parts[2], "m=8,t=1,p=256", parts[4], parts[5]),
		"too much memory":     rebuild(parts[2], "m=262145,t=1,p=1", parts[4], parts[5]),
		"memory overflow":     rebuild(parts[2], "m=4294967296,t=1,p=1", parts[4], parts[5]),
		"memory under 8p":     rebuild(parts[2], "m=7,t=1,p=1", parts[4], parts[5]),
		"zero memory":         rebuild(parts[2], "m=0,t=1,p=1", parts[4], parts[5]),
		"trailing text":       rebuild(parts[2], "m=19456,t=2,p=1A5", parts[4], parts[5]),
		"trailing in t":       rebuild(parts[2], "m=19456,t=2x,p=1", parts[4], parts[5]),
		"plus sign":           rebuild(parts[2], "m=+19456,t=2,p=1", parts[4], parts[5]),
		"minus sign":          rebuild(parts[2], "m=-8,t=2,p=1", parts[4], parts[5]),
		"extra field":         rebuild(parts[2], "m=19456,t=2,p=1,x=1", parts[4], parts[5]),
		"missing field":       rebuild(parts[2], "m=19456,t=2", parts[4], parts[5]),
		"fields out of order": rebuild(parts[2], "t=2,m=19456,p=1", parts[4], parts[5]),
		"space":               rebuild(parts[2], "m=19456, t=2,p=1", parts[4], parts[5]),
		"empty number":        rebuild(parts[2], "m=,t=2,p=1", parts[4], parts[5]),
		"empty cost":          rebuild(parts[2], "", parts[4], parts[5]),
		"version trailing":    rebuild("v=19junk", parts[3], parts[4], parts[5]),
		"version padded":      rebuild("v=019", parts[3], parts[4], parts[5]),
		"version old":         rebuild("v=16", parts[3], parts[4], parts[5]),
		"short salt":          rebuild(parts[2], parts[3], "AAAAAA", parts[5]),
		"short tag":           rebuild(parts[2], parts[3], parts[4], "AA"),
		"long tag":            rebuild(parts[2], parts[3], parts[4], strings.Repeat("A", 4000)),
		"text before first $": "0" + good,
	} {
		require.NotPanics(t, func() { require.False(t, CheckPassword("pw", phc), name) }, name)
	}
}

// Each limit on a stored hash, one step inside (accepted) and one step outside (refused), so a limit that moves by
// one is caught. parseHash is read directly: a hash at the memory and round limits takes seconds to verify.
func TestParseHashLimits(t *testing.T) {
	b64 := func(n int) string { return strings.Repeat("A", n*4/3) } // n bytes, n a multiple of 3
	phc := func(cost string, salt, tag int) string {
		return "$argon2id$v=19$" + cost + "$" + b64(salt) + "$" + b64(tag)
	}
	for _, ok := range []string{
		phc("m=8,t=1,p=1", 9, 18),       // the least memory for one thread, one round
		phc("m=32,t=8,p=4", 9, 18),      // the most rounds and threads, the least memory for four
		phc("m=262144,t=1,p=1", 63, 63), // the most memory, long salt and tag
		"$argon2id$v=19$m=8,t=1,p=1$" + strings.Repeat("A", 11) + "$" + strings.Repeat("A", 22), // salt 8 and tag 16 bytes
		"$argon2id$v=19$m=8,t=1,p=1$" + strings.Repeat("A", 86) + "$" + strings.Repeat("A", 86), // salt and tag 64 bytes
	} {
		_, good := parseHash(ok)
		require.True(t, good, ok)
	}
	for name, bad := range map[string]string{
		"rounds over":        phc("m=32,t=9,p=4", 9, 18),
		"threads over":       phc("m=40,t=1,p=5", 9, 18),
		"memory under 8p":    phc("m=31,t=1,p=4", 9, 18),
		"memory over":        phc("m=262145,t=1,p=1", 9, 18),
		"salt under 8 bytes": "$argon2id$v=19$m=8,t=1,p=1$" + strings.Repeat("A", 10) + "$" + b64(18),
		"tag under 16 bytes": "$argon2id$v=19$m=8,t=1,p=1$" + b64(9) + "$" + strings.Repeat("A", 20),
		"salt over 64 bytes": "$argon2id$v=19$m=8,t=1,p=1$" + strings.Repeat("A", 87) + "$" + b64(18),
		"tag over 64 bytes":  "$argon2id$v=19$m=8,t=1,p=1$" + b64(9) + "$" + strings.Repeat("A", 87),
		"zero":               phc("m=8,t=0,p=1", 9, 18),
		"leading zero":       phc("m=08,t=1,p=1", 9, 18),
	} {
		_, good := parseHash(bad)
		require.False(t, good, name)
	}
}

// The default wait bound is 10 s, and a turn that comes exactly at the bound is waited for, not refused.
func TestFailureTrackerWaitsUpToTheBound(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	fakeClock(f, &now, nil) // the default bound stays; the fake wait fires at once
	for i := 0; i < 8; i++ {
		fail(f, "a")
	}
	// The next turn is 16 s away: past the bound, so it is refused, not held.
	require.False(t, f.Acquire(context.Background(), "a"), "16 s away is refused")
	now = now.Add(5 * time.Second)
	require.Equal(t, 11*time.Second, f.Wait("a"))
	require.False(t, f.Acquire(context.Background(), "a"), "just past the bound is refused")
	now = now.Add(1 * time.Second)
	require.Equal(t, 10*time.Second, f.Wait("a"))
	require.True(t, f.Acquire(context.Background(), "a"), "a wait of exactly the bound is held")
	f.Finish("a", false)
}

// An attempt that gives up waiting frees its place in the queue: afterwards a full queue of new attempts can still
// wait, and each of them gets its turn.
func TestFailureTrackerGiveUpReleasesItsPlace(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFailureTracker()
	var mu sync.Mutex
	f.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	for i := 0; i < 5; i++ {
		fail(f, "a")
	}
	// Every attempt now waits 2 s for its turn. The fake wait reports that it started and fires only when the test
	// says so. Acquire asks for it under the tracker's lock and joins the queue before letting go, so a Count call
	// made after the report returns only once that attempt is queued (or refused).
	started := make(chan struct{}, 16)
	fire := make(chan time.Time)
	f.After = func(time.Duration) <-chan time.Time {
		select {
		case started <- struct{}{}:
		default:
		}
		return fire
	}
	queued := func() {
		t.Helper()
		<-started
		f.Count("a")
	}

	ctx, cancel := context.WithCancel(t.Context())
	gaveUp := make(chan bool)
	go func() { gaveUp <- f.Acquire(ctx, "a") }()
	queued()
	cancel()
	require.False(t, <-gaveUp, "the cancelled attempt gives up")

	results := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		go func() {
			ok := f.Acquire(t.Context(), "a")
			if ok {
				f.Forget("a") // a verified success: the rest need not wait
				f.Finish("a", false)
			}
			results <- ok
		}()
		queued()
	}
	require.False(t, f.Acquire(t.Context(), "a"), "one more than the queue holds is refused at once")
	<-started

	mu.Lock()
	now = now.Add(2 * time.Second)
	mu.Unlock()
	close(fire)
	for i := 0; i < 4; i++ {
		require.True(t, <-results, "every attempt that queued after the one that gave up gets its turn")
	}
}

// Over every byte value each password character comes up equally often, and only the biased tail is drawn again.
func TestPasswordCharIsUniform(t *testing.T) {
	hits := map[byte]int{}
	refused := 0
	for b := 0; b < 256; b++ {
		c, ok := passwordChar(byte(b))
		if !ok {
			refused++
			continue
		}
		hits[c]++
	}
	require.Len(t, hits, len(passwordAlphabet))
	for c, n := range hits {
		require.Equal(t, 256/len(passwordAlphabet), n, string(c))
	}
	require.Equal(t, 256%len(passwordAlphabet), refused)
}

// The failure count fades only once more than the window has passed: at exactly the window it still counts, a failure
// then adds to it, and on a full table the newcomer is counted while exactly one old client loses its count.
func TestFailureTrackerWindowEdge(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := t0
	f := NewFailureTracker()
	fakePacing(f, &now, nil)
	for i := 0; i < 5; i++ {
		fail(f, "a")
	}
	now = t0.Add(f.window)
	require.Equal(t, 5, f.Count("a"))
	require.True(t, fail(f, "a"))
	require.Equal(t, 6, f.Count("a"), "the sixth failure adds to the five")

	g := NewFailureTracker()
	now = t0
	g.Now = func() time.Time { return now }
	for i := 0; i < maxTracked; i++ {
		fail(g, fmt.Sprintf("ip-%d", i))
	}
	now = t0.Add(g.window)
	fail(g, "newcomer")
	require.Equal(t, 1, g.Count("newcomer"))
	lost := 0
	for i := 0; i < maxTracked; i++ {
		if g.Count(fmt.Sprintf("ip-%d", i)) == 0 {
			lost++
		}
	}
	require.Equal(t, 1, lost, "one old client made room, the others of the same age keep their count")
}

// The delay doubles from 2 s and stops at the 60 s cap exactly.
func TestFailureTrackerDelayCap(t *testing.T) {
	f := NewFailureTracker()
	for n, want := range map[int]time.Duration{5: 2 * time.Second, 6: 4 * time.Second, 9: 32 * time.Second, 10: time.Minute, 1000: time.Minute} {
		require.Equal(t, want, f.delay(n), n)
	}
}

// The wait bound can be lifted or lowered but never removed: a bound of zero would refuse every attempt that has to
// wait.
func TestFailureTrackerWaitBoundIsPositive(t *testing.T) {
	f := NewFailureTracker()
	require.Panics(t, func() { f.SetMaxWait(0) })
	require.NotPanics(t, func() { f.SetMaxWait(time.Nanosecond) })
}

// The warning interval is an hour to the second, and an expected proxy stays silent even when a warning is due.
func TestWarnUntrustedProxyHeadersEdges(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	h := WarnUntrustedProxyHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		nil, func(r *http.Request) bool { return r.Header.Get("X-Test-Expected") != "" }, log, func() time.Time { return now })
	send := func(expected bool) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "198.51.100.7:1"
		r.Header.Set("X-Forwarded-For", "100.64.0.9")
		if expected {
			r.Header.Set("X-Test-Expected", "1")
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	send(true)
	require.Empty(t, buf.String(), "an expected proxy never warns, even the first time")
	send(false)
	require.Contains(t, buf.String(), "level=WARN")
	buf.Reset()
	now = now.Add(proxyWarnEvery)
	send(true)
	require.Empty(t, buf.String(), "an expected proxy never warns, even when a warning is due")
	send(false)
	require.Contains(t, buf.String(), "level=WARN", "an hour later it warns again")
}

// A TLS connection is https whatever the headers say.
func TestEffectiveSchemeTLS(t *testing.T) {
	r := httptest.NewRequest("GET", "https://rss.example.com/", nil)
	r.Header.Set("X-Forwarded-Proto", "http")
	require.NotNil(t, r.TLS)
	require.Equal(t, "https", EffectiveScheme(r, nil))
	r.TLS = nil
	require.Equal(t, "http", EffectiveScheme(r, nil))
}
