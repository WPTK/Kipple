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
	"github.com/WPTK/kipple/internal/reach"
)

// Wrong passwords are slowed, not refused: five are free, then each wait doubles
// (the fake clock advances instead of sleeping), and the right password still
// signs in however many wrong ones came before it from the same client.
func TestLoginWrongPasswordsEscalateButTheRightOneSignsIn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code, "attempt %d", i)
	}
	require.Zero(t, h.paced.Load(), "the first five failures are free")
	start := h.clk.Now()
	for i := 0; i < 8; i++ {
		require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code, "attempt %d", i)
	}
	require.Equal(t, int32(8), h.paced.Load(), "every attempt over budget waits its turn")
	require.Equal(t, (2+4+8+16+32+60+60+60)*time.Second, h.clk.Now().Sub(start), "doubling to the 60 s cap")

	// another client has its own budget
	paced := h.paced.Load()
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), peer("10.20.30.11:1")).Code)
	require.Equal(t, paced, h.paced.Load())

	// the right password waits its turn but is never refused, and clears the client
	rec := h.do("POST", "/api/auth/login", loginBody(testPass))
	require.Equal(t, http.StatusNoContent, rec.Code, "%s", rec.Body.String())
	require.Equal(t, paced+1, h.paced.Load())
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code)
	require.Equal(t, paced+1, h.paced.Load(), "a verified sign-in clears the count")
}

// When a client's own wait is longer than the server will hold a request, it is
// answered at once (503 busy) with that wait as Retry-After, not after 10 s with
// a flat 5.
func TestLoginOwnLongWaitAnswersAtOnceWithTheRealRetryAfter(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := 0; i < 8; i++ {
		h.do("POST", "/api/auth/login", loginBody("wrong"))
	}
	h.srv.fails.MaxWait = 10 * time.Second // the real bound; the harness lifts it for the fake clock
	paced := h.paced.Load()
	rec := h.do("POST", "/api/auth/login", loginBody(testPass))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.JSONEq(t, `{"error":"busy"}`, rec.Body.String())
	require.Equal(t, "17", rec.Header().Get("Retry-After"), "16 s after the 8th failure, rounded up")
	require.Equal(t, paced, h.paced.Load(), "answered at once, nothing waited")
	h.clk.Advance(17 * time.Second)
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass)).Code)
}

// The count fades only after a quiet hour, not every few minutes.
func TestLoginFailuresFadeAfterAQuietHour(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for i := 0; i < 8; i++ {
		h.do("POST", "/api/auth/login", loginBody("wrong"))
	}
	h.clk.Advance(30 * time.Minute)
	paced := h.paced.Load()
	h.do("POST", "/api/auth/login", loginBody("wrong"))
	h.do("POST", "/api/auth/login", loginBody("wrong"))
	require.Greater(t, h.paced.Load(), paced, "half an hour later the client is still being paced")
	h.clk.Advance(61 * time.Minute)
	paced = h.paced.Load()
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/auth/login", loginBody("wrong")).Code)
	require.Equal(t, paced, h.paced.Load(), "an hour with no failure starts it over")
}

// A parallel burst from one client never hashes more than one password at a time
// and holds only a few requests open; the rest are told to retry (503), and
// nothing is ever answered 429. The right password still signs in afterwards.
func TestLoginBurstRunsOneCheckAtATime(t *testing.T) {
	t.Parallel()
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

// A trusted proxy that sends the standard
// X-Forwarded-For (never CF-Connecting-IP): a stranger's wrong passwords cost
// the stranger's address, not the owner's.
func TestLoginBehindXForwardedForProxyKeysOnTheClient(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Reach = reach.Fixed(reach.State{Trusted: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	})
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
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Reach = reach.Fixed(reach.State{Trusted: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}})
	})
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

// Docker Desktop, rootless Docker, an unlisted proxy: every client arrives from
// one address and so shares one budget. Sequential noise from other people on
// that address (a stale password manager) only slows the owner: the right
// password is still checked and signs in. This covers sequential noise only;
// the flood test below covers the case this cannot promise.
func TestLoginSharedAddressSequentialNoiseOnlySlowsTheOwner(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	gw := peer("172.17.0.1:50000")
	for i := 0; i < 10; i++ {
		h.do("POST", "/api/auth/login", loginBody("guess"), gw)
	}
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), gw).Code)
}

// The limit of the promise, pinned so it is never mistaken for a guarantee. When
// many people really share one address key (an unlisted proxy, Docker's gateway,
// CGNAT) and a flood keeps one hash running with the waiting room full, the
// owner's attempt is not run: it answers 503 busy + Retry-After (never 429, and
// nothing is counted), and signs in as soon as the flood eases. With the proxy
// list correct each visitor is a separate key and this cannot happen.
func TestLoginFloodOnASharedKeyMakesTheOwnerBusyNotLockedOut(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	h := newHarness(t, func(o *Options) {
		o.Verifier = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 30 * time.Second, Check: func(pw, phc string) bool {
			if pw == "wrong" {
				started <- struct{}{}
				<-release
			}
			return pw == testPass
		}})
	})
	gw := peer("172.17.0.1:50000")
	var wg sync.WaitGroup
	// One attempt hashing, MaxWaiters (4) waiting behind it, and one more refused at once.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.do("POST", "/api/auth/login", loginBody("wrong"), gw)
		}()
	}
	<-started
	time.Sleep(300 * time.Millisecond) // let the others reach the waiting room

	rec := h.do("POST", "/api/auth/login", loginBody(testPass), gw)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "the owner on the flooded key is told to retry")
	require.JSONEq(t, `{"error":"busy"}`, rec.Body.String())
	require.NotEmpty(t, rec.Header().Get("Retry-After"))

	close(release) // the flood ends
	wg.Wait()
	require.Equal(t, http.StatusNoContent, h.do("POST", "/api/auth/login", loginBody(testPass), gw).Code, "and the owner signs in once it has")
}

// A peer that is not a trusted proxy cannot pick its own client address: rotating
// X-Forwarded-For or CF-Connecting-IP buys no fresh budget.
func TestLoginSpoofedForwardingHeadersFromAnUntrustedPeerAreIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Reach = reach.Fixed(reach.State{Trusted: []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}})
	})
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
	t.Parallel()
	h := newHarness(t, func(o *Options) {
		o.Reach = reach.Fixed(reach.State{Trusted: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}})
	})
	for i := 0; i < 10; i++ {
		fake := netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}).String()
		h.do("POST", "/api/auth/login", loginBody("guess"), peer("172.17.0.1:1"),
			hdr("X-Forwarded-For", fake+", 198.51.100.7, 172.17.0.9"))
	}
	require.Equal(t, int32(5), h.paced.Load())
}
