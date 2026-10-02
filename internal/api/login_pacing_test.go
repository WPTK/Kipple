package api

import (
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
)

// Wrong passwords are slowed, never refused: the right password signs in however
// many wrong ones came before it from the same client.
func TestLoginWrongPasswordsSlowTheClientNeverLockIt(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code, "attempt %d", i)
	}
	require.Zero(t, h.paced.Load(), "the first five failures are free")
	for i := 0; i < 20; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code, "attempt %d", i)
	}
	require.Equal(t, int32(20), h.paced.Load(), "every attempt over budget waits its turn")
	before := h.clk.Now()
	rec := h.do("POST", "/api/auth/login", loginBody(testPass))
	require.Equal(t, http.StatusNoContent, rec.Code, "the right password is never refused: %s", rec.Body.String())
	require.True(t, h.clk.Now().After(before), "the right password waited for its turn")
	require.NotEqual(t, http.StatusTooManyRequests, h.do("POST", "/api/auth/login", loginBody("wrong")).Code)

	// another client has its own budget
	paced := h.paced.Load()
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), peer("10.20.30.11:1")).Code)
	require.Equal(t, paced, h.paced.Load())

	// the failures age out with the window
	h.clk.Advance(11 * time.Minute)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
	require.Equal(t, paced, h.paced.Load())
}

// A parallel burst from one client never hashes more than one password at a time
// and holds only a few requests open; the rest are told to retry (503), and
// nothing is ever answered 429. The right password still signs in afterwards.
func TestLoginBurstRunsOneCheckAtATime(t *testing.T) {
	var cur, peak atomic.Int32
	h := newHarness(t, func(o *Options) {
		o.Verifier = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 30 * time.Second, Check: func(pw, phc string) bool {
			n := cur.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(5 * time.Millisecond) // slow enough that the burst overlaps
			cur.Add(-1)
			return pw == testPass
		}})
	})
	var wg sync.WaitGroup
	var wrong, busy, other atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch h.do("POST", "/api/auth/login", loginBody("wrong")).Code {
			case http.StatusUnauthorized:
				wrong.Add(1)
			case http.StatusServiceUnavailable:
				busy.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, peak.Load())
	require.Zero(t, other.Load(), "only 401 and 503, never 429")
	require.Positive(t, busy.Load(), "a burst past the waiting room is told to retry")
	require.EqualValues(t, 40, wrong.Load()+busy.Load())
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
}

// A proxy listed in KIPPLE_TRUSTED_PROXY_IPS that sends the standard
// X-Forwarded-For (never CF-Connecting-IP): a stranger's wrong passwords cost
// the stranger's address, not the owner's.
func TestLoginBehindXForwardedForProxyKeysOnTheClient(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")} })
	via := func(client string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "127.0.0.1:40000"
			r.Header.Set("X-Forwarded-For", client)
			r.Header.Set("X-Forwarded-Proto", "https")
		}
	}
	for i := 0; i < 10; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("guess"), via("203.0.113.66")).Code)
	}
	paced := h.paced.Load()
	require.Positive(t, paced)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), via("198.51.100.7")).Code)
	require.Equal(t, paced, h.paced.Load(), "the owner at another address waits for nothing")
}

// Same for Cloudflare Tunnel, which sends CF-Connecting-IP.
func TestLoginBehindCloudflareKeysOnTheClient(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")} })
	via := func(ip string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "192.0.2.20:4000"
			r.Header.Set("CF-Connecting-IP", ip)
		}
	}
	for i := 0; i < 10; i++ {
		h.do("POST", "/api/auth/login", loginBody("wrong"), via("203.0.113.9"))
	}
	paced := h.paced.Load()
	require.Positive(t, paced)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), via("203.0.113.10")).Code)
	require.Equal(t, paced, h.paced.Load())
}

// Docker Desktop, rootless Docker, the userland proxy: every client arrives from
// the bridge gateway, so they share one budget. The shared budget delays the
// owner; it cannot refuse the owner's password.
func TestLoginDockerGatewaySharedAddressStillSignsTheOwnerIn(t *testing.T) {
	h := newHarness(t)
	gw := peer("172.17.0.1:50000")
	for i := 0; i < 10; i++ {
		h.do("POST", "/api/auth/login", loginBody("guess"), gw)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), gw).Code)
}

// A peer that is not a trusted proxy cannot pick its own client address: rotating
// X-Forwarded-For or CF-Connecting-IP buys no fresh budget.
func TestLoginSpoofedForwardingHeadersFromAnUntrustedPeerAreIgnored(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")} })
	for i := 0; i < 10; i++ {
		ip := netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}).String()
		h.do("POST", "/api/auth/login", loginBody("guess"), peer("198.51.100.9:1"),
			hdr("X-Forwarded-For", ip), hdr("CF-Connecting-IP", ip))
	}
	require.Equal(t, int32(5), h.paced.Load(), "the ten spoofed attempts all counted against the one real peer")
}

// A trusted proxy's client is the rightmost untrusted hop: a spoofed leftmost
// entry does not give a fresh budget either.
func TestLoginRightmostUntrustedHopIsTheClient(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")} })
	for i := 0; i < 10; i++ {
		fake := netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}).String()
		h.do("POST", "/api/auth/login", loginBody("guess"), peer("172.17.0.1:1"),
			hdr("X-Forwarded-For", fake+", 198.51.100.7, 172.17.0.9"))
	}
	require.Equal(t, int32(5), h.paced.Load())
}
