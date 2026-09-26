// Package auth holds password hashing and verification shared by the web login
// and the Reader API (design §1 decision 10, §6.3): argon2id verification behind
// a global semaphore, a keyed in-memory memo of the last good password, and a
// per-IP failure delay.
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
	"net"
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
	ok = v.check(pw, phc)
	<-v.sem
	if ok {
		v.mu.Lock()
		if bytes.Equal(v.secret, secret) { // not rotated while hashing
			v.memo[kind] = want
		}
		v.mu.Unlock()
	}
	return ok, false
}

// ClearMemo forgets every remembered login.
func (v *Verifier) ClearMemo() {
	v.mu.Lock()
	v.memo = map[string][]byte{}
	v.mu.Unlock()
}

// FailureTracker delays repeated failures from one IP (design §6.3): failures
// are counted in a fixed window; once the window holds Threshold failures each
// further failing response is delayed. A success clears the IP. Never a 429.
type FailureTracker struct {
	Window    time.Duration
	Threshold int
	Delay     time.Duration
	Now       func() time.Time

	mu sync.Mutex
	m  map[string]*failure
}

type failure struct {
	start time.Time
	n     int
}

// NewFailureTracker returns the design defaults: 10 minute window, 5 failures, 2 s delay.
func NewFailureTracker() *FailureTracker {
	return &FailureTracker{Window: 10 * time.Minute, Threshold: 5, Delay: 2 * time.Second, Now: time.Now, m: map[string]*failure{}}
}

// Fail records a failure for ip and returns how long to delay this response.
func (f *FailureTracker) Fail(ip string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.Now()
	e := f.m[ip]
	if e == nil || now.Sub(e.start) > f.Window {
		makeRoom(f.m, now, f.Window, false)
		e = &failure{start: now}
		f.m[ip] = e
	}
	delay := time.Duration(0)
	if e.n >= f.Threshold {
		delay = f.Delay
	}
	e.n++
	return delay
}

// Clear forgets ip's failures (after a successful login).
func (f *FailureTracker) Clear(ip string) {
	f.mu.Lock()
	delete(f.m, ip)
	f.mu.Unlock()
}

// Count returns the failures currently recorded for ip.
func (f *FailureTracker) Count(ip string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e := f.m[ip]; e != nil && f.Now().Sub(e.start) <= f.Window {
		return e.n
	}
	return 0
}

// ClientIP is CF-Connecting-IP when the TCP peer is a trusted proxy, else the peer.
func ClientIP(r *http.Request, trusted []netip.Addr) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, perr := netip.ParseAddr(host)
	if perr != nil {
		return host
	}
	peer = peer.Unmap()
	for _, t := range trusted {
		if t == peer {
			if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
				if a, err := netip.ParseAddr(cf); err == nil {
					return a.Unmap().String()
				}
			}
			break
		}
	}
	return peer.String()
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

// PeerTrusted reports whether the TCP peer of r is one of the trusted proxies.
func PeerTrusted(r *http.Request, trusted []netip.Addr) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	peer = peer.Unmap()
	for _, t := range trusted {
		if t == peer {
			return true
		}
	}
	return false
}

// EffectiveScheme is "https" when the connection is TLS, or when a trusted
// proxy says so with X-Forwarded-Proto (design §7); otherwise "http".
func EffectiveScheme(r *http.Request, trusted []netip.Addr) string {
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
	l.mu.Lock()
	delete(l.m, ip)
	l.mu.Unlock()
}

// proxyWarnEvery is the minimum gap between untrusted-proxy-header warnings.
const proxyWarnEvery = time.Hour

// WarnUntrustedProxyHeaders wraps next and logs a WARN, at most once an hour,
// when CF-Connecting-IP or X-Forwarded-Proto arrives from a TCP peer that is
// not in trusted (KIPPLE_TRUSTED_PROXY_IPS). Those headers are then ignored, so
// the client IP is the proxy's address and the lockouts and failure delays of
// every visitor collapse onto it; the log line is how that misconfiguration
// becomes visible. now is time.Now when nil.
func WarnUntrustedProxyHeaders(next http.Handler, trusted []netip.Addr, log *slog.Logger, now func() time.Time) http.Handler {
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
		if r.Header.Get("X-Forwarded-Proto") != "" {
			hdrs = append(hdrs, "X-Forwarded-Proto")
		}
		if len(hdrs) > 0 && !PeerTrusted(r, trusted) {
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
// `kipple password` share them.
const (
	MinPasswordLen = 5
	MaxPasswordLen = 256
)
