package greader

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
)

func login(h *harness, email, pass string) (int, string) {
	w := h.do(http.MethodPost, base+"/accounts/ClientLogin", "Email="+email+"&Passwd="+pass, map[string]string{"Authorization": ""})
	return w.Code, w.Body.String()
}

func TestClientLoginSuccessShape(t *testing.T) {
	h := newHarness(t)
	code, body := login(h, "OWNER", testPass) // Email is case-insensitive
	require.Equal(t, 200, code)
	require.Equal(t, "SID="+h.tok+"\nLSID=null\nAuth="+h.tok+"\n", body)
	// NNW splits every line on '=' and needs exactly two parts.
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		require.Len(t, strings.Split(line, "="), 2, line)
	}
	require.Regexp(t, `^owner/[0-9a-f]{64}$`, h.tok)

	w := h.do(http.MethodGet, base+"/accounts/ClientLogin?Email=owner&Passwd="+testPass+"&output=json", "", map[string]string{"Authorization": ""})
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"SID":"`+h.tok+`","LSID":null,"Auth":"`+h.tok+`"}`, w.Body.String())
}

func TestClientLoginFailure(t *testing.T) {
	h := newHarness(t)
	for _, c := range [][2]string{{"owner", "wrong"}, {"mallory", testPass}, {"owner", ""}} {
		w := h.do(http.MethodPost, base+"/accounts/ClientLogin", "Email="+c[0]+"&Passwd="+c[1], nil)
		require.Equal(t, 401, w.Code, c)
		require.Equal(t, "Error=BadAuthentication\n", w.Body.String())
		require.Equal(t, "true", w.Header().Get("Google-Bad-Token"))
		require.Equal(t, "true", w.Header().Get("X-Reader-Google-Bad-Token"))
		require.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	}
}

func TestClientLoginDisabledAPI(t *testing.T) {
	h := newHarness(t, harnessOpts{noAPIPassword: true})
	code, _ := login(h, "owner", testPass)
	require.Equal(t, 401, code)
	require.Equal(t, int32(0), h.checks.Load(), "no hashing when the API is disabled")
	w := h.get(rd + "token")
	require.Equal(t, 401, w.Code)
}

func TestAuthMatrix(t *testing.T) {
	h := newHarness(t)
	bad := func(w interface{ Result() *http.Response }, msg string) {
		t.Helper()
		res := w.Result()
		require.Equal(t, 401, res.StatusCode, msg)
		require.Equal(t, "true", res.Header.Get("X-Reader-Google-Bad-Token"), msg)
		require.Equal(t, "true", res.Header.Get("Google-Bad-Token"), msg)
	}
	bad(h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": ""}), "no header")
	bad(h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "GoogleLogin auth=owner/deadbeef"}), "wrong token")
	bad(h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "GoogleLogin auth=" + h.tok + " extra"}), "three fields")
	bad(h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "Bearer " + h.tok}), "wrong scheme")
	bad(h.do(http.MethodGet, base+rd+"token", "", map[string]string{"Authorization": "", "Cookie": "kipple_session=abc"}), "cookie only")
	// A GET never authenticates with T.
	bad(h.do(http.MethodGet, base+rd+"token?T="+h.tok, "", map[string]string{"Authorization": ""}), "GET with T")

	w := h.get(rd + "token")
	require.Equal(t, 200, w.Code)
	require.Equal(t, h.tok+"\n", w.Body.String())
	require.Contains(t, w.Header().Get("Content-Type"), "text/plain")

	w = h.get(rd + "user-info?output=json")
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"userId":"1","userName":"owner","userProfileId":"1","userEmail":"owner"}`, w.Body.String())
	w = h.get(rd + "user-info") // FeedMe/Unread call it without output
	require.Equal(t, 200, w.Code)
}

func TestWriteTokenRules(t *testing.T) {
	h := newHarness(t)
	h.api.routes["echo-write"] = route{post: true, h: func(c *call) { c.ok() }}
	noHdr := map[string]string{"Authorization": ""}
	cases := []struct {
		name string
		body string
		hdr  map[string]string
		want int
	}{
		{"header only", "i=1", nil, 200},
		{"header + T=token", "T=" + h.tok + "&i=1", nil, 200},
		{"header + T=x (Reeder 4)", "T=x&i=1", nil, 200},
		{"header + empty T", "T=&i=1", nil, 200},
		{"header + stale T", "T=old&i=1", nil, 401},
		{"no header + T=token", "T=" + h.tok + "&i=1", noHdr, 200},
		{"no header + T=x", "T=x&i=1", noHdr, 401},
		{"no header + no T", "i=1", noHdr, 401},
		{"no header + wrong T", "T=owner/00&i=1", noHdr, 401},
	}
	for _, c := range cases {
		w := h.do(http.MethodPost, base+rd+"echo-write", c.body, c.hdr)
		require.Equal(t, c.want, w.Code, c.name)
		if c.want == 401 {
			require.Equal(t, "true", w.Header().Get("Google-Bad-Token"), c.name)
		}
	}
	// T in the query string counts too.
	w := h.do(http.MethodPost, base+rd+"echo-write?T="+h.tok, "i=1", noHdr)
	require.Equal(t, 200, w.Code)
}

// The budget is spent before hashing: after 5 attempts in the window only one
// attempt per 2 s is hashed; the rest are the ordinary 401 (never 429) without
// hashing. The correct password still gets through on the next granted slot.
func TestClientLoginBudgetIsCheckedBeforeHashing(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 20; i++ {
		code, body := login(h, "owner", "wrong")
		require.Equal(t, 401, code)
		require.Equal(t, "Error=BadAuthentication\n", body)
	}
	require.Equal(t, int32(5), h.checks.Load(), "attempts 6..20 inside 2 s are refused without hashing")

	code, _ := login(h, "owner", testPass)
	require.Equal(t, 401, code, "over budget: even the right password is not hashed yet")
	require.Equal(t, int32(5), h.checks.Load())

	h.clk.Advance(2 * time.Second)
	code, _ = login(h, "owner", testPass)
	require.Equal(t, 200, code, "the next granted attempt verifies the correct password")
	require.Equal(t, int32(6), h.checks.Load())

	// Cleared: five more attempts are hashed straight away.
	for i := 0; i < 5; i++ {
		login(h, "owner", "wrong")
	}
	require.Equal(t, int32(11), h.checks.Load())

	// The remembered password needs no hashing, so it is not budgeted: a
	// signed-in device keeps working while a stale one burns the budget.
	for i := 0; i < 10; i++ {
		login(h, "owner", "wrong")
	}
	code, _ = login(h, "owner", testPass)
	require.Equal(t, 200, code, "memo hit succeeds over budget")
}

// One attempt per client is in flight; a second one from the same client (or
// the same IPv6 /64) is refused without waiting for or taking the hash slot.
func TestClientLoginOneAttemptInFlightPerClient(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var checks atomic.Int32
	h := newHarness(t)
	h.api.ver = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 50 * time.Millisecond, Check: func(pw, phc string) bool {
		checks.Add(1)
		started <- struct{}{}
		<-release
		return false
	}})
	send := func(remote string) int {
		return h.doFrom(remote, http.MethodPost, base+"/accounts/ClientLogin", "Email=owner&Passwd=x", map[string]string{"Authorization": ""}).Code
	}
	done := make(chan struct{})
	go func() { defer close(done); send("[2001:db8:1:2::1]:1000") }()
	<-started

	start := time.Now()
	require.Equal(t, 401, send("[2001:db8:1:2::1]:1001"), "same address")
	require.Equal(t, 401, send("[2001:db8:1:2:ffff::9]:1002"), "same /64")
	require.Less(t, time.Since(start), 40*time.Millisecond, "refused at once, not after the semaphore wait")
	require.Equal(t, int32(1), checks.Load())
	require.Equal(t, 1, h.api.fails.Count("2001:db8:1:2::1"), "a refused attempt is not counted")

	// Another /64 is another client: it waits for the slot (busy, not counted).
	require.Equal(t, 401, send("[2001:db8:1:3::1]:1003"))
	require.Zero(t, h.api.fails.Count("2001:db8:1:3::1"))
	close(release)
	<-done
}

func TestClientLoginIPv6BudgetIsPer64(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 20; i++ {
		remote := "[2001:db8:0:7:" + strconv.Itoa(i+1) + "::1]:443"
		require.Equal(t, 401, h.doFrom(remote, http.MethodPost, base+"/accounts/ClientLogin", "Email=owner&Passwd=wrong", map[string]string{"Authorization": ""}).Code)
	}
	require.Equal(t, int32(5), h.checks.Load(), "rotating addresses inside one /64 shares one budget")
}

func TestMemoizedLoginSkipsHashing(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		code, _ := login(h, "owner", testPass)
		require.Equal(t, 200, code)
	}
	require.Equal(t, int32(1), h.checks.Load(), "only the first login hashes")
}

func TestConcurrentWrongLoginsRunOneHashAtATime(t *testing.T) {
	var cur, peak, total atomic.Int32
	ver := auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 30 * time.Second, Check: func(pw, phc string) bool {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		total.Add(1)
		time.Sleep(2 * time.Millisecond)
		cur.Add(-1)
		return false
	}})
	h := newHarness(t)
	h.api.ver = ver
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// distinct clients: one client has only one attempt in flight
			remote := "198.51.100." + strconv.Itoa(i+1) + ":1"
			w := h.doFrom(remote, http.MethodPost, base+"/accounts/ClientLogin", "Email=owner&Passwd=wrong", map[string]string{"Authorization": ""})
			require.Equal(t, 401, w.Code)
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), peak.Load())
	require.Equal(t, int32(50), total.Load())
}

func TestVerifierSemaphoreWaitIsBounded(t *testing.T) {
	release := make(chan struct{})
	v := auth.NewVerifier([]byte("k"), auth.VerifierOptions{Wait: 20 * time.Millisecond, Check: func(pw, phc string) bool {
		<-release
		return true
	}})
	done := make(chan bool)
	go func() { done <- v.Verify(context.Background(), "api", "a", "h") }()
	time.Sleep(10 * time.Millisecond)
	require.False(t, v.Verify(context.Background(), "api", "b", "h"), "second caller gives up after the wait")
	close(release)
	require.True(t, <-done)
}

func TestClientFamilyAndLastSeen(t *testing.T) {
	require.Equal(t, "reeder", family("Reeder/5.0 CFNetwork"))
	require.Equal(t, "netnewswire", family("NetNewsWire (RSS Reader; https://netnewswire.com/)"))
	require.Equal(t, "unread", family("Unread/4.0"))
	require.Equal(t, "api", family("curl/8"))

	h := newHarness(t)
	h.do(http.MethodGet, base+rd+"token", "", map[string]string{"User-Agent": "Reeder/5"})
	first := h.api.LastSeen()["reeder"]
	require.False(t, first.IsZero())
	h.clk.Advance(20 * time.Second)
	h.do(http.MethodGet, base+rd+"token", "", map[string]string{"User-Agent": "Reeder/5"})
	require.Equal(t, first, h.api.LastSeen()["reeder"], "updated at most once a minute")
	h.clk.Advance(50 * time.Second)
	h.do(http.MethodGet, base+rd+"token", "", map[string]string{"User-Agent": "Reeder/5"})
	require.True(t, h.api.LastSeen()["reeder"].After(first))
}

func TestClientLoginBusyHashingSlotIs401WithoutFailureOrRetryAfter(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	h := newHarness(t)
	h.api.ver = auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Wait: 20 * time.Millisecond, Check: func(pw, phc string) bool {
		started <- struct{}{}
		<-release
		return false
	}})
	done := make(chan struct{})
	go func() { defer close(done); login(h, "owner", "first") }()
	<-started // the only hashing slot is now held

	// another client, so it reaches the (held) hashing slot
	w := h.doFrom("192.0.2.99:1", http.MethodPost, base+"/accounts/ClientLogin", "Email=owner&Passwd=second", map[string]string{"Authorization": ""})
	require.Equal(t, http.StatusUnauthorized, w.Code, "design 6.3: a busy slot is a 401")
	require.Equal(t, "Error=BadAuthentication\n", w.Body.String())
	require.Empty(t, w.Header().Get("Retry-After"))
	require.Equal(t, "true", w.Header().Get("Google-Bad-Token"))
	require.Zero(t, h.api.fails.Count("192.0.2.99"), "busy is not a failure")
	close(release)
	<-done
}

// A snapshot read before InvalidateAccount must not be cached after it, or the
// old token would keep working until the TTL expires.
func TestInvalidateAccountDuringInflightLoad(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.api.afterAcctRead = func() {
		// The password changes and is invalidated while the load is in flight.
		h.api.afterAcctRead = nil
		require.NoError(t, h.db.SetAPIPasswordHash(ctx, "new-hash"))
		h.api.InvalidateAccount()
	}
	_, err := h.api.account(ctx) // read the old row, then lost the race
	require.NoError(t, err)

	s, err := h.api.account(ctx)
	require.NoError(t, err)
	require.Equal(t, "new-hash", s.hash, "stale snapshot was cached across InvalidateAccount")
	require.NotEqual(t, h.tok, s.token)
}
