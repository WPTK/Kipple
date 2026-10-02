// Package auth holds password hashing and verification shared by the web login
// and the Reader API (design §1 decision 10, §6.3): argon2id verification behind
// a global semaphore, a keyed in-memory memo of the last good password, and a
// per-client attempt budget.
package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP minimum, design decision 10).
const (
	memoryKiB = 19456
	timeCost  = 2
	threads   = 1
	saltLen   = 16
	keyLen    = 32
)

// HashPassword returns an argon2id PHC string for pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, timeCost, memoryKiB, threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword verifies pw against a PHC string produced by HashPassword.
func CheckPassword(pw, phc string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m > 1<<20 || t > 16 || p == 0 || p > 16 { // refuse absurd stored parameters
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Verifier verifies passwords with at most one argon2id run at a time and a
// bounded wait. A success is remembered as HMAC(secret, ...) so a client that
// re-runs ClientLogin costs no hashing.
type Verifier struct {
	secret []byte
	sem    chan struct{}
	wait   time.Duration
	check  func(pw, phc string) bool

	mu   sync.Mutex
	memo map[string][]byte // kind -> HMAC of the last verified (phc, password)
}

// VerifierOptions tunes NewVerifier (tests only).
type VerifierOptions struct {
	// Check replaces CheckPassword (a fake with a concurrency gauge).
	Check func(pw, phc string) bool
	// Wait bounds the wait for the semaphore; default 5 s.
	Wait time.Duration
}

// NewVerifier returns a Verifier keyed by the account secret. There is one per
// process, shared by the web login and ClientLogin so the semaphore of 1 bounds
// hashing across both; the secret may be (re)set later with SetSecret.
func NewVerifier(secret []byte, opts VerifierOptions) *Verifier {
	v := &Verifier{secret: secret, sem: make(chan struct{}, 1), wait: opts.Wait, check: opts.Check, memo: map[string][]byte{}}
	if v.wait <= 0 {
		v.wait = 5 * time.Second
	}
	if v.check == nil {
		v.check = CheckPassword
	}
	return v
}

func mac(secret []byte, kind, phc, pw string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("login|" + kind + "|" + phc + "|" + pw))
	return h.Sum(nil)
}

// SetSecret installs the account secret that keys the success memo. It is a
// no-op when the secret is unchanged and drops every remembered login when it
// changes, so one Verifier can be shared by the web login and ClientLogin and
// survive a secret rotation.
func (v *Verifier) SetSecret(secret []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if bytes.Equal(v.secret, secret) {
		return
	}
	v.secret = append([]byte(nil), secret...)
	v.memo = map[string][]byte{}
}

// Verify reports whether pw matches phc. kind ("api" or "web") separates the
// memos. The memo covers the hash too, so changing the password invalidates it
// without any explicit clearing.
func (v *Verifier) Verify(ctx context.Context, kind, pw, phc string) bool {
	ok, _ := v.VerifyBusy(ctx, kind, pw, phc)
	return ok
}

// VerifyBusy is Verify that also reports busy: the hashing slot could not be
// had (timeout or cancelled context), which says nothing about the password and
// must not count as a login failure.
func (v *Verifier) VerifyBusy(ctx context.Context, kind, pw, phc string) (ok, busy bool) {
	if pw == "" || phc == "" {
		return false, false
	}
	v.mu.Lock()
	secret := v.secret
	v.mu.Unlock()
	want := mac(secret, kind, phc, pw)
	v.mu.Lock()
	memo := v.memo[kind]
	v.mu.Unlock()
	if memo != nil && hmac.Equal(memo, want) {
		return true, false
	}
	timer := time.NewTimer(v.wait)
	defer timer.Stop()
	select {
	case v.sem <- struct{}{}:
	case <-timer.C:
		return false, true
	case <-ctx.Done():
		return false, true
	}
	// Deferred, so a panic in check cannot hold the only hashing slot forever.
	ok = func() bool {
		defer func() { <-v.sem }()
		return v.check(pw, phc)
	}()
	if ok {
		v.mu.Lock()
		if bytes.Equal(v.secret, secret) { // not rotated while hashing
			v.memo[kind] = want
		}
		v.mu.Unlock()
	}
	return ok, false
}

// Remembered reports whether pw matches the remembered success for kind and
// phc, without hashing (a cheap check made before any rate budget is spent, so
// a client already signed in keeps working while its address is over budget).
func (v *Verifier) Remembered(kind, pw, phc string) bool {
	if pw == "" || phc == "" {
		return false
	}
	v.mu.Lock()
	secret, memo := v.secret, v.memo[kind]
	v.mu.Unlock()
	return memo != nil && hmac.Equal(memo, mac(secret, kind, phc, pw))
}

// ClearMemo forgets every remembered login.
func (v *Verifier) ClearMemo() {
	v.mu.Lock()
	v.memo = map[string][]byte{}
	v.mu.Unlock()
}

// FailureTracker paces password checks per client (design §6.3): the Reader
// API's ClientLogin and the web sign-in. Only failures are counted, and the
// count fades only once Window passes with no failure at all. Every admitted
// attempt has its password verified, so an attempt that gets its turn is never
// refused unchecked; the budget only slows a client down:
//
//   - At most one attempt per client hashes at a time. A second concurrent one
//     (a client retrying a slow login) waits for the first instead of failing.
//   - The first Threshold failures are free. After that an attempt starts only
//     when a delay has passed since the client's previous attempt: Delay after
//     the Threshold-th failure, doubling with each further failure up to
//     MaxDelay. It waits for that instead of being refused.
//   - Waiting is bounded: at most MaxWaiters attempts per client wait, for at
//     most MaxWait (or until the request is cancelled). An attempt that cannot
//     start is not run and counts nothing; its caller answers with a retry
//     (never a 429).
//
// So one client never runs more than one hash at a time and holds at most
// 1+MaxWaiters requests open. A verified success clears the client (Forget);
// a busy verifier counts nothing. Clients are keyed by RateKey, so an IPv6 /64
// is one client. Several people who truly share one key (an unlisted proxy,
// Docker's gateway, carrier NAT) share one budget, so even one persistent
// guesser among them can make the others' attempts wait or answer busy.
type FailureTracker struct {
	Window     time.Duration
	Threshold  int
	Delay      time.Duration
	MaxDelay   time.Duration
	MaxWait    time.Duration
	MaxWaiters int
	Now        func() time.Time
	// After is time.After; tests with a fake Now replace it (the pacing wait).
	After func(time.Duration) <-chan time.Time

	mu   sync.Mutex
	m    map[string]*failure
	live map[string]*attempt
}

type failure struct {
	start time.Time // Lockout: when the window began. FailureTracker: the client's last failure
	n     int
	last  time.Time // FailureTracker: when the client's last attempt started or failed
}

// attempt is the in-flight state of one client.
type attempt struct {
	busy    bool          // an attempt is admitted and not finished
	waiters int           // attempts waiting to start
	done    chan struct{} // closed (and replaced) when an admitted attempt finishes
}

// NewFailureTracker returns the design defaults: failures fade after an hour
// with none, 5 are free, then 2 s doubling per failure to 60 s; at most 4
// waiting attempts per client, each waiting at most 10 s.
func NewFailureTracker() *FailureTracker {
	return &FailureTracker{Window: time.Hour, Threshold: 5, Delay: 2 * time.Second, MaxDelay: time.Minute,
		MaxWait: 10 * time.Second, MaxWaiters: 4, Now: time.Now,
		m: map[string]*failure{}, live: map[string]*attempt{}}
}

// Acquire admits one attempt for ip before its password is checked, waiting
// (bounded) while ip has an attempt in flight or is inside its over-budget
// delay. It returns false when the attempt could not start: too many attempts
// already waiting, MaxWait passed, or ctx ended; nothing is counted and the
// caller answers with a retry, without hashing. After true the caller must call
// Finish exactly once.
func (f *FailureTracker) Acquire(ctx context.Context, ip string) bool {
	k := RateKey(ip)
	maxWait := f.MaxWait
	if maxWait <= 0 {
		maxWait = 10 * time.Second
	}
	after := f.After
	if after == nil {
		after = time.After
	}
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()

	f.mu.Lock()
	if f.live == nil {
		f.live = map[string]*attempt{}
	}
	if f.m == nil {
		f.m = map[string]*failure{}
	}
	a := f.live[k]
	if a == nil {
		a = &attempt{done: make(chan struct{})}
		f.live[k] = a
	}
	waiting := false
	for {
		var wake <-chan time.Time
		var done <-chan struct{}
		if a.busy {
			done = a.done
		} else {
			now := f.Now()
			pace := f.paceLocked(k, now)
			if pace <= 0 {
				a.busy = true
				if waiting {
					a.waiters--
				}
				if e := f.m[k]; e != nil {
					e.last = now
				}
				f.mu.Unlock()
				return true
			}
			if pace > maxWait {
				// The turn cannot come within the bound: say so now instead of
				// holding the request for MaxWait (Wait tells the caller how long).
				if waiting {
					a.waiters--
				}
				f.dropIdle(k, a)
				f.mu.Unlock()
				return false
			}
			wake = after(pace)
		}
		if !waiting {
			if a.waiters >= f.MaxWaiters {
				f.dropIdle(k, a)
				f.mu.Unlock()
				return false
			}
			a.waiters++
			waiting = true
		}
		f.mu.Unlock()
		gaveUp := false
		select {
		case <-done:
		case <-wake:
		case <-deadline.C:
			gaveUp = true
		case <-ctx.Done():
			gaveUp = true
		}
		f.mu.Lock()
		if gaveUp {
			a.waiters--
			f.dropIdle(k, a)
			f.mu.Unlock()
			return false
		}
	}
}

// dropIdle forgets a's entry once nothing is in flight or waiting. f.mu is held.
func (f *FailureTracker) dropIdle(k string, a *attempt) {
	if !a.busy && a.waiters == 0 && f.live[k] == a {
		delete(f.live, k)
	}
}

// Finish ends an attempt admitted by Acquire. failed is true only for a wrong
// password (or email): that is the one outcome counted. A busy
// verifier that said nothing about the password counts nothing; a verified
// success calls Forget as well.
func (f *FailureTracker) Finish(ip string, failed bool) {
	k := RateKey(ip)
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.live[k]; a != nil && a.busy {
		a.busy = false
		close(a.done)
		a.done = make(chan struct{})
		f.dropIdle(k, a)
	}
	if !failed {
		return
	}
	now := f.Now()
	e := f.m[k]
	if e == nil || now.Sub(e.start) > f.Window {
		makeRoom(f.m, now, f.Window, false)
		e = &failure{start: now}
		f.m[k] = e
	}
	e.n++
	e.start = now
	e.last = now
}

// paceLocked is how much longer ip's next attempt must wait (0: none). An
// expired entry is dropped. f.mu is held.
func (f *FailureTracker) paceLocked(k string, now time.Time) time.Duration {
	e := f.m[k]
	if e != nil && now.Sub(e.start) > f.Window {
		delete(f.m, k)
		return 0
	}
	if e == nil || e.n < f.Threshold {
		return 0
	}
	return e.last.Add(f.delay(e.n)).Sub(now)
}

// Wait is how much longer ip's next attempt must wait because of its own
// failures (0 when none): what to tell a client whose Acquire returned false.
func (f *FailureTracker) Wait(ip string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return max(f.paceLocked(RateKey(ip), f.Now()), 0)
}

// delay is how long the client must wait after its n-th failure (n >= Threshold):
// Delay, doubling per further failure, never above MaxDelay.
func (f *FailureTracker) delay(n int) time.Duration {
	d := f.Delay
	for i := f.Threshold; i < n && d < f.MaxDelay; i++ {
		d *= 2
	}
	if f.MaxDelay > 0 && d > f.MaxDelay {
		d = f.MaxDelay
	}
	return d
}

// Forget clears ip's failures: its owner just proved the password.
func (f *FailureTracker) Forget(ip string) {
	f.mu.Lock()
	delete(f.m, RateKey(ip))
	f.mu.Unlock()
}

// Count returns the failures currently recorded for ip.
func (f *FailureTracker) Count(ip string) int {
	k := RateKey(ip)
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.m[k]; e != nil && f.Now().Sub(e.start) <= f.Window {
		return e.n
	}
	return 0
}

// RateKey is the key the per-client trackers use for ip: an IPv4 address (an
// IPv4-mapped IPv6 address is unmapped) as is, an IPv6 address as its /64,
// because one subscriber usually holds a whole /64 and could otherwise rotate
// through unlimited keys. Anything unparsable is used verbatim.
func RateKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.WithZone("").Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// passwordAlphabet has no look-alike characters (no 0/O, 1/l/I).
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratePassword returns n random characters from an unambiguous alphabet
// (24 characters is about 137 bits).
func GeneratePassword(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("auth: password length %d", n)
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	// 256 is not a multiple of len(alphabet): reject the biased tail.
	const limit = 256 - 256%len(passwordAlphabet)
	for i := 0; i < n; {
		b := buf[i]
		if int(b) >= limit {
			if _, err := rand.Read(buf[i : i+1]); err != nil {
				return "", err
			}
			continue
		}
		out[i] = passwordAlphabet[int(b)%len(passwordAlphabet)]
		i++
	}
	return string(out), nil
}

// EffectiveScheme is "https" when the connection is TLS, or when a trusted
// proxy says so with X-Forwarded-Proto (design §7); otherwise "http".
func EffectiveScheme(r *http.Request, trusted []netip.Prefix) string {
	if r.TLS != nil {
		return "https"
	}
	if PeerTrusted(r, trusted) && strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") {
		return "https"
	}
	return "http"
}

// Lockout is the web login lockout: after Max failures from one IP inside a
// fixed Window (started by the first failure), that IP is refused until the
// window ends. Per IP, never global, so an attacker elsewhere cannot lock the owner
// out. A success clears the IP.
type Lockout struct {
	Max    int
	Window time.Duration
	Now    func() time.Time

	mu sync.Mutex
	m  map[string]*failure
}

// NewLockout returns the plan defaults: 10 attempts (failures) per 15 minutes.
func NewLockout(now func() time.Time) *Lockout {
	if now == nil {
		now = time.Now
	}
	return &Lockout{Max: 10, Window: 15 * time.Minute, Now: now, m: map[string]*failure{}}
}

// Locked reports whether ip is locked out and for how much longer.
func (l *Lockout) Locked(ip string) (bool, time.Duration) {
	ip = RateKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[ip]
	if e == nil {
		return false, 0
	}
	now := l.Now()
	if now.Sub(e.start) >= l.Window {
		delete(l.m, ip)
		return false, 0
	}
	if e.n >= l.Max {
		return true, e.start.Add(l.Window).Sub(now)
	}
	return false, 0
}

// Reserve counts one attempt for ip *before* its password is checked, so a
// parallel burst cannot run more than Max verifications: the check and the
// increment are one atomic step. It returns false (and how long the lockout
// has left) when ip is locked. The caller then calls Clear on success, Release
// when the attempt says nothing about the password (busy, malformed), and does
// nothing on a wrong password: the reservation stays as the recorded failure.
func (l *Lockout) Reserve(ip string) (ok bool, left time.Duration) {
	ip = RateKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	e := l.m[ip]
	if e != nil && now.Sub(e.start) >= l.Window {
		delete(l.m, ip)
		e = nil
	}
	if e != nil && e.n >= l.Max {
		return false, e.start.Add(l.Window).Sub(now)
	}
	if e == nil {
		makeRoom(l.m, now, l.Window, true)
		e = &failure{start: now}
		l.m[ip] = e
	}
	e.n++
	return true, 0
}

// Release gives back one reservation.
func (l *Lockout) Release(ip string) {
	ip = RateKey(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[ip]; e != nil {
		if e.n--; e.n <= 0 {
			delete(l.m, ip)
		}
	}
}

// maxTracked bounds the per-IP maps. When they are full of live entries the
// oldest one is evicted, so new offenders are always tracked. (Stopping instead
// would let an attacker rotating source addresses switch tracking off.)
const maxTracked = 4096

// makeRoom frees one slot in m when it is full: expired entries first (start
// plus window <= now; inclusive reports whether the boundary itself is expired),
// then the entry with the oldest window start.
func makeRoom(m map[string]*failure, now time.Time, window time.Duration, inclusive bool) {
	if len(m) < maxTracked {
		return
	}
	for k, v := range m {
		if d := now.Sub(v.start); d > window || (inclusive && d == window) {
			delete(m, k)
		}
	}
	if len(m) < maxTracked {
		return
	}
	var oldest string
	var oldestAt time.Time
	for k, v := range m {
		if oldest == "" || v.start.Before(oldestAt) {
			oldest, oldestAt = k, v.start
		}
	}
	delete(m, oldest)
}

// Clear forgets ip.
func (l *Lockout) Clear(ip string) {
	ip = RateKey(ip)
	l.mu.Lock()
	delete(l.m, ip)
	l.mu.Unlock()
}

// proxyWarnEvery is the minimum gap between untrusted-proxy-header warnings.
const proxyWarnEvery = time.Hour

// WarnUntrustedProxyHeaders wraps next and logs a WARN, at most once an hour,
// when CF-Connecting-IP, X-Forwarded-For or X-Forwarded-Proto arrives from a TCP
// peer that is not in trusted (KIPPLE_TRUSTED_PROXY_IPS). Those headers are then
// ignored, so the client IP is the proxy's address and the failure delays of
// every visitor collapse onto it; the log line is how that misconfiguration
// becomes visible. A request for which expected returns true (Tailscale Serve,
// a loopback proxy that is meant to stay untrusted) is not a misconfiguration
// and never warns; expected may be nil. now is time.Now when nil.
func WarnUntrustedProxyHeaders(next http.Handler, trusted []netip.Prefix, expected func(*http.Request) bool, log *slog.Logger, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	var mu sync.Mutex
	var last time.Time
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var hdrs []string
		if r.Header.Get("CF-Connecting-IP") != "" {
			hdrs = append(hdrs, "CF-Connecting-IP")
		}
		if r.Header.Get("X-Forwarded-For") != "" {
			hdrs = append(hdrs, "X-Forwarded-For")
		}
		if r.Header.Get("X-Forwarded-Proto") != "" {
			hdrs = append(hdrs, "X-Forwarded-Proto")
		}
		if len(hdrs) > 0 && !PeerTrusted(r, trusted) && (expected == nil || !expected(r)) {
			mu.Lock()
			t := now()
			warn := last.IsZero() || t.Sub(last) >= proxyWarnEvery
			if warn {
				last = t
			}
			mu.Unlock()
			if warn {
				log.Warn("proxy headers from an untrusted peer are ignored; if this peer is your reverse proxy or tunnel, add its address to KIPPLE_TRUSTED_PROXY_IPS",
					"peer", r.RemoteAddr, "headers", strings.Join(hdrs, ","))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Web and Reader API password length limits, in bytes (the owner chose 5; the upper
// bound keeps the hashing input sane). The account endpoints and
// `kipple password` share them. A chosen (not generated) Reader API password
// must be at least MinAPIPasswordLen: it guards the public ClientLogin, where
// the only brake is a per-client rate budget. Generated ones are 24 characters.
const (
	MinPasswordLen    = 5
	MaxPasswordLen    = 256
	MinAPIPasswordLen = 16
)
