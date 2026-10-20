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
	"strconv"
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

// Limits on a stored hash. HashPassword writes the constants above; anything a row holds beyond these is a hand-edited
// or restored row, refused instead of panicking in argon2.IDKey (zero rounds) or holding the one hashing slot for minutes.
const (
	maxMemoryKiB = 1 << 18 // 256 MiB
	maxTimeCost  = 8
	maxThreads   = 4
	minSaltLen   = saltLen / 2
	minKeyLen    = keyLen / 2 // a one-byte tag would match a wrong password 1 time in 256
	maxFieldLen  = 64
)

// storedHash is a parsed PHC string, every field already inside the limits above.
type storedHash struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

// parseDigits reads "<key>=<decimal digits>" as HashPassword writes it: no sign, space, trailing text or leading zero.
// Zero itself is refused too: no field of a stored hash can be zero.
func parseDigits(field, key string) (uint64, bool) {
	digits, found := strings.CutPrefix(field, key+"=")
	if !found || strings.HasPrefix(digits, "0") {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	return n, err == nil
}

// parseHash reads "$argon2id$v=19$m=<KiB>,t=<rounds>,p=<threads>$<salt>$<tag>" as HashPassword writes it.
func parseHash(phc string) (h storedHash, ok bool) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return h, false
	}
	if v, ok := parseDigits(parts[2], "v"); !ok || v != argon2.Version {
		return h, false
	}
	cost := strings.Split(parts[3], ",")
	if len(cost) != 3 {
		return h, false
	}
	m, okM := parseDigits(cost[0], "m")
	t, okT := parseDigits(cost[1], "t")
	p, okP := parseDigits(cost[2], "p")
	if !okM || !okT || !okP || p > maxThreads || t > maxTimeCost || m < 8*p || m > maxMemoryKiB {
		return h, false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) < minSaltLen || len(salt) > maxFieldLen {
		return h, false
	}
	key, err := enc.DecodeString(parts[5])
	if err != nil || len(key) < minKeyLen || len(key) > maxFieldLen {
		return h, false
	}
	return storedHash{memory: uint32(m), time: uint32(t), threads: uint8(p), salt: salt, key: key}, true
}

// CheckPassword verifies pw against a PHC string produced by HashPassword. A stored hash that is not well formed
// never verifies (and is logged, since the owner would otherwise see only a wrong password).
func CheckPassword(pw, phc string) bool {
	h, ok := parseHash(phc)
	if !ok {
		slog.Warn("auth: the stored password hash is malformed or outside the accepted limits; it cannot verify")
		return false
	}
	got := argon2.IDKey([]byte(pw), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(got, h.key) == 1
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
// count fades only once window passes with no failure at all. Every admitted
// attempt has its password verified, so an attempt that gets its turn is never
// refused unchecked; the budget only slows a client down:
//
//   - At most one attempt per client hashes at a time. A second concurrent one
//     (a client retrying a slow login) waits for the first instead of failing.
//   - The first threshold failures are free. After that an attempt starts only
//     when a delay has passed since the client's previous attempt: firstDelay after
//     the threshold-th failure, doubling with each further failure up to
//     maxDelay. It waits for that instead of being refused.
//   - Waiting is bounded: at most maxWaiters attempts per client wait, for at
//     most maxWait (or until the request is cancelled). An attempt that cannot
//     start is not run and counts nothing; its caller answers with a retry
//     (never a 429).
//
// So one client never runs more than one hash at a time and holds at most
// 1+maxWaiters requests open. A verified success clears the client (Forget);
// a busy verifier counts nothing. Clients are keyed by RateKey, so an IPv6 /64
// is one client. Several people who truly share one key (an unlisted proxy,
// Docker's gateway, carrier NAT) share one budget, so even one persistent
// guesser among them can make the others' attempts wait or answer busy.
//
// Build one with NewFailureTracker; the zero value cannot be used.
type FailureTracker struct {
	// The policy is set only by NewFailureTracker, so every tracker has a whole one.
	window     time.Duration
	threshold  int
	firstDelay time.Duration
	maxDelay   time.Duration
	maxWait    time.Duration
	maxWaiters int

	Now func() time.Time
	// After is time.After; tests with a fake Now replace it (the pacing wait).
	After func(time.Duration) <-chan time.Time

	mu   sync.Mutex
	m    map[string]*failure
	live map[string]*attempt
}

type failure struct {
	start time.Time // the client's last failure
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
	return &FailureTracker{window: time.Hour, threshold: 5, firstDelay: 2 * time.Second, maxDelay: time.Minute,
		maxWait: 10 * time.Second, maxWaiters: 4, Now: time.Now, After: time.After,
		m: map[string]*failure{}, live: map[string]*attempt{}}
}

// SetMaxWait changes how long an attempt may wait for its turn; d must be positive. Tests whose fake clock makes
// every wait instant lift it. Call it before the first Acquire; it is not safe to call concurrently with one.
func (f *FailureTracker) SetMaxWait(d time.Duration) {
	if d <= 0 {
		panic("auth: FailureTracker wait bound must be positive")
	}
	f.maxWait = d
}

// Acquire admits one attempt for ip before its password is checked, waiting
// (bounded) while ip has an attempt in flight or is inside its over-budget
// delay. It returns false when the attempt could not start: too many attempts
// already waiting, maxWait passed, or ctx ended; nothing is counted and the
// caller answers with a retry, without hashing. After true the caller must call
// Finish exactly once.
func (f *FailureTracker) Acquire(ctx context.Context, ip string) bool {
	k := RateKey(ip)
	after := f.After
	deadline := time.NewTimer(f.maxWait)
	defer deadline.Stop()

	f.mu.Lock()
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
			if pace > f.maxWait {
				// The turn cannot come within the bound: say so now instead of
				// holding the request for maxWait (Wait tells the caller how long).
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
			if a.waiters >= f.maxWaiters {
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
	if e == nil || now.Sub(e.start) > f.window {
		makeRoom(f.m, now, f.window)
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
	if e != nil && now.Sub(e.start) > f.window {
		delete(f.m, k)
		return 0
	}
	if e == nil || e.n < f.threshold {
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

// delay is how long the client must wait after its n-th failure (n >= threshold):
// firstDelay, doubling per further failure, never above maxDelay.
func (f *FailureTracker) delay(n int) time.Duration {
	d := f.firstDelay
	for i := f.threshold; i < n && d != f.maxDelay; i++ {
		d = min(2*d, f.maxDelay)
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
	if e := f.m[k]; e != nil && f.Now().Sub(e.start) <= f.window {
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
	for i := 0; i < n; {
		c, ok := passwordChar(buf[i])
		if !ok {
			if _, err := rand.Read(buf[i : i+1]); err != nil {
				return "", err
			}
			continue
		}
		out[i] = c
		i++
	}
	return string(out), nil
}

// passwordChar maps a random byte to a password character. 256 is not a multiple of len(passwordAlphabet), so the
// bytes of the biased tail are refused (ok false) and drawn again: every character is equally likely.
func passwordChar(b byte) (c byte, ok bool) {
	const limit = 256 - 256%len(passwordAlphabet)
	if int(b) >= limit {
		return 0, false
	}
	return passwordAlphabet[int(b)%len(passwordAlphabet)], true
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

// maxTracked bounds the per-IP maps. When they are full of live entries the
// oldest one is evicted, so new offenders are always tracked. (Stopping instead
// would let an attacker rotating source addresses switch tracking off.)
const maxTracked = 4096

// makeRoom frees one slot in m when it is full: expired entries first (start
// plus window < now), then the entry with the oldest window start.
func makeRoom(m map[string]*failure, now time.Time, window time.Duration) {
	if len(m) < maxTracked {
		return
	}
	for k, v := range m {
		if now.Sub(v.start) > window {
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

// call is f(), or nil when f is nil.
func call(f func() []netip.Prefix) []netip.Prefix {
	if f == nil {
		return nil
	}
	return f()
}

// proxyWarnEvery is the minimum gap between untrusted-proxy-header warnings.
const proxyWarnEvery = time.Hour

// WarnUntrustedProxyHeaders wraps next and logs a WARN, at most once an hour,
// when CF-Connecting-IP, X-Forwarded-For or X-Forwarded-Proto arrives from a TCP
// peer that is not in trusted() (the trusted proxies setting). Those headers are then
// ignored, so the client IP is the proxy's address and the failure delays of
// every visitor collapse onto it; the log line is how that misconfiguration
// becomes visible. A request for which expected returns true (Tailscale Serve,
// a loopback proxy that is meant to stay untrusted) is not a misconfiguration
// and never warns; expected and trusted may be nil. now is time.Now when nil.
func WarnUntrustedProxyHeaders(next http.Handler, trusted func() []netip.Prefix, expected func(*http.Request) bool, log *slog.Logger, now func() time.Time) http.Handler {
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
		if len(hdrs) > 0 && !PeerTrusted(r, call(trusted)) && (expected == nil || !expected(r)) {
			mu.Lock()
			t := now()
			warn := last.IsZero() || t.Sub(last) >= proxyWarnEvery
			if warn {
				last = t
			}
			mu.Unlock()
			if warn {
				log.Warn("proxy headers from an untrusted peer are ignored; if this peer is your reverse proxy or tunnel, add its address to the trusted proxies (Settings, Account & Devices)",
					"peer", r.RemoteAddr, "headers", strings.Join(hdrs, ","))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Web and Reader API password length limits, in bytes (the upper bound keeps the
// hashing input sane). MinPasswordLen applies only when a web password is set
// (the wizard, a restore, the account endpoint, KIPPLE_PASSWORD and
// `kipple password`); sign-in never checks it, so a shorter password set
// earlier keeps working until it is changed. A chosen (not generated) Reader API password
// must be at least MinAPIPasswordLen: it guards the public ClientLogin, where
// the only brake is a per-client rate budget. Generated ones are 24 characters.
const (
	MinPasswordLen    = 8
	MaxPasswordLen    = 256
	MinAPIPasswordLen = 16
)
