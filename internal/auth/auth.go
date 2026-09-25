// Package auth holds password hashing and verification shared by the web login
// and the Reader API (design §1 decision 10, §6.3): argon2id verification behind
// a global semaphore, a keyed in-memory memo of the last good password, and a
// per-IP failure delay.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
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

// NewVerifier returns a Verifier keyed by the account secret.
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

func (v *Verifier) mac(kind, phc, pw string) []byte {
	h := hmac.New(sha256.New, v.secret)
	h.Write([]byte("login|" + kind + "|" + phc + "|" + pw))
	return h.Sum(nil)
}

// Verify reports whether pw matches phc. kind ("api" or "web") separates the
// memos. The memo covers the hash too, so changing the password invalidates it
// without any explicit clearing.
func (v *Verifier) Verify(ctx context.Context, kind, pw, phc string) bool {
	if pw == "" || phc == "" {
		return false
	}
	want := v.mac(kind, phc, pw)
	v.mu.Lock()
	memo := v.memo[kind]
	v.mu.Unlock()
	if memo != nil && hmac.Equal(memo, want) {
		return true
	}
	timer := time.NewTimer(v.wait)
	defer timer.Stop()
	select {
	case v.sem <- struct{}{}:
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
	ok := v.check(pw, phc)
	<-v.sem
	if ok {
		v.mu.Lock()
		v.memo[kind] = want
		v.mu.Unlock()
	}
	return ok
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
		if len(f.m) >= 4096 {
			for k, v := range f.m {
				if now.Sub(v.start) > f.Window {
					delete(f.m, k)
				}
			}
			if len(f.m) >= 4096 { // still full of live entries: stop tracking new IPs
				return 0
			}
		}
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
