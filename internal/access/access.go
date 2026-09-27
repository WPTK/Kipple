// Package access verifies Cloudflare Access application tokens (design §7.0):
// the RS256 JWT that Access adds to every request it lets through, in the
// Cf-Access-Jwt-Assertion header. It is optional. With KIPPLE_ACCESS_TEAM_DOMAIN
// and KIPPLE_ACCESS_AUD unset there is no Verifier (a nil *Verifier verifies
// nothing), so Kipple never depends on Cloudflare.
//
// A verified token is a trust signal, never a replacement for the session
// cookie: it proves that Cloudflare Access admitted this request to this
// application (the AUD tag) under this team (the issuer). Anyone can put any
// string in the header, so nothing is trusted until the signature verifies
// against the team's published keys and iss, aud, exp and nbf check out.
package access

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Header is the request header Cloudflare Access puts the application token in.
const Header = "Cf-Access-Jwt-Assertion"

const (
	// maxTokenLen bounds the header before any decoding (Access tokens are about
	// 1 KiB).
	maxTokenLen = 8 << 10
	// maxCertsBody bounds the JWKS response.
	maxCertsBody = 1 << 20
	// minRSABits refuses weak keys in the key set.
	minRSABits = 2048

	defaultRefresh    = time.Hour
	defaultMinRefetch = time.Minute
	defaultMaxStale   = 24 * time.Hour
	defaultLeeway     = 30 * time.Second
	defaultTimeout    = 10 * time.Second
)

// Identity is what a verified token says about the request.
type Identity struct {
	// Email is the signed-in user's email. It is empty for a service token,
	// which is a machine identity, not a person.
	Email string
	// Subject is the token's sub claim (the user's Access id).
	Subject string
	// Expires is the token's exp claim.
	Expires time.Time
}

// Options tunes New. Zero values take the defaults; tests set the endpoints.
type Options struct {
	// Client fetches the key set; default a plain client with a 10 s timeout.
	Client *http.Client
	// CertsURL overrides https://<team domain>/cdn-cgi/access/certs (tests).
	CertsURL string
	// Issuer overrides https://<team domain> (tests).
	Issuer string
	// Refresh is how old the cached key set may get before it is fetched again
	// (default 1 h). A token signed by an unknown key id also triggers a fetch,
	// at most once per MinRefetch (default 1 min).
	Refresh    time.Duration
	MinRefetch time.Duration
	// MaxStale is how long a cached key set stays usable while every refresh
	// fails (default 24 h); after that nothing verifies until a fetch succeeds.
	MaxStale time.Duration
	// Leeway is the clock skew allowed on exp and nbf (default 30 s).
	Leeway time.Duration
	Now    func() time.Time
	Logger *slog.Logger
}

// Verifier checks Access tokens for one team and one application. It is safe
// for concurrent use.
type Verifier struct {
	issuer, aud, certsURL string
	client                *http.Client
	refresh, minRefetch   time.Duration
	maxStale, leeway      time.Duration
	now                   func() time.Time
	log                   *slog.Logger

	fetchMu sync.Mutex // one key-set fetch at a time

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetchedAt   time.Time // last successful fetch
	attemptedAt time.Time // last fetch attempt, successful or not
}

// NormalizeTeamDomain turns KIPPLE_ACCESS_TEAM_DOMAIN into a bare host name.
// It accepts "team.cloudflareaccess.com" or "https://team.cloudflareaccess.com"
// (with or without a trailing slash) and refuses anything with a path, port,
// user info, query or characters a host name cannot have.
func NormalizeTeamDomain(v string) (string, error) {
	h := strings.TrimSpace(v)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimSuffix(h, "/")
	h = strings.ToLower(h)
	if h == "" {
		return "", errors.New("empty team domain")
	}
	if len(h) > 253 || !strings.Contains(h, ".") {
		return "", fmt.Errorf("%q is not a team domain such as yourteam.cloudflareaccess.com", v)
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("%q is not a team domain such as yourteam.cloudflareaccess.com", v)
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("%q is not a team domain such as yourteam.cloudflareaccess.com (host name only, https:// optional)", v)
			}
		}
	}
	return h, nil
}

// New returns a Verifier for the team domain (see NormalizeTeamDomain) and the
// application's AUD tag. Both are required.
func New(teamDomain, aud string, opt Options) (*Verifier, error) {
	host, err := NormalizeTeamDomain(teamDomain)
	if err != nil {
		return nil, err
	}
	aud = strings.TrimSpace(aud)
	if aud == "" {
		return nil, errors.New("empty application AUD")
	}
	v := &Verifier{
		issuer: "https://" + host, aud: aud, certsURL: "https://" + host + "/cdn-cgi/access/certs",
		client: opt.Client, refresh: opt.Refresh, minRefetch: opt.MinRefetch, maxStale: opt.MaxStale,
		leeway: opt.Leeway, now: opt.Now, log: opt.Logger,
	}
	if opt.CertsURL != "" {
		v.certsURL = opt.CertsURL
	}
	if opt.Issuer != "" {
		v.issuer = opt.Issuer
	}
	if v.client == nil {
		v.client = &http.Client{Timeout: defaultTimeout}
	}
	if v.refresh <= 0 {
		v.refresh = defaultRefresh
	}
	if v.minRefetch <= 0 {
		v.minRefetch = defaultMinRefetch
	}
	if v.maxStale <= 0 {
		v.maxStale = defaultMaxStale
	}
	if v.leeway <= 0 {
		v.leeway = defaultLeeway
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.log == nil {
		v.log = slog.Default()
	}
	return v, nil
}

// Issuer is the iss value tokens must carry.
func (v *Verifier) Issuer() string { return v.issuer }

// Errors Verify returns. They are for logs and tests; callers treat every
// error the same (no verified identity).
var (
	ErrNoToken    = errors.New("access: no token")
	ErrMalformed  = errors.New("access: malformed token")
	ErrAlg        = errors.New("access: algorithm is not RS256")
	ErrUnknownKey = errors.New("access: unknown signing key")
	ErrSignature  = errors.New("access: bad signature")
	ErrIssuer     = errors.New("access: wrong issuer")
	ErrAudience   = errors.New("access: wrong audience")
	ErrExpired    = errors.New("access: token expired")
	ErrNotYet     = errors.New("access: token not valid yet")
	ErrNoKeys     = errors.New("access: signing keys unavailable")
)

// VerifyRequest verifies the Cf-Access-Jwt-Assertion header of r. A nil
// Verifier (Access not configured) never verifies anything.
func (v *Verifier) VerifyRequest(r *http.Request) (Identity, error) {
	if v == nil {
		return Identity{}, ErrNoToken
	}
	return v.Verify(r.Context(), r.Header.Get(Header))
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss   string          `json:"iss"`
	Aud   json.RawMessage `json:"aud"`
	Exp   *json.Number    `json:"exp"`
	Nbf   *json.Number    `json:"nbf"`
	Sub   string          `json:"sub"`
	Email string          `json:"email"`
}

// Verify checks token: RS256 signature by a key of the team's key set, iss,
// aud, exp (required) and nbf (when present).
func (v *Verifier) Verify(ctx context.Context, token string) (Identity, error) {
	if v == nil || token == "" {
		return Identity{}, ErrNoToken
	}
	if len(token) > maxTokenLen {
		return Identity{}, ErrMalformed
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrMalformed
	}
	hb, err := b64(parts[0])
	if err != nil {
		return Identity{}, ErrMalformed
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return Identity{}, ErrMalformed
	}
	// The algorithm is pinned: never "none", never HS256 with the public key as
	// the secret, whatever the token says.
	if hdr.Alg != "RS256" {
		return Identity{}, ErrAlg
	}
	sig, err := b64(parts[2])
	if err != nil || len(sig) == 0 {
		return Identity{}, ErrMalformed
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return Identity{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return Identity{}, ErrSignature
	}
	// Only a signed payload is parsed.
	pb, err := b64(parts[1])
	if err != nil {
		return Identity{}, ErrMalformed
	}
	dec := json.NewDecoder(bytes.NewReader(pb))
	dec.UseNumber()
	var c jwtClaims
	if err := dec.Decode(&c); err != nil {
		return Identity{}, ErrMalformed
	}
	if c.Iss != v.issuer {
		return Identity{}, ErrIssuer
	}
	if !audContains(c.Aud, v.aud) {
		return Identity{}, ErrAudience
	}
	now := v.now()
	if c.Exp == nil {
		return Identity{}, ErrMalformed
	}
	exp, err := numericDate(*c.Exp)
	if err != nil {
		return Identity{}, ErrMalformed
	}
	if !now.Before(exp.Add(v.leeway)) {
		return Identity{}, ErrExpired
	}
	if c.Nbf != nil {
		nbf, err := numericDate(*c.Nbf)
		if err != nil {
			return Identity{}, ErrMalformed
		}
		if now.Add(v.leeway).Before(nbf) {
			return Identity{}, ErrNotYet
		}
	}
	return Identity{Email: strings.TrimSpace(c.Email), Subject: c.Sub, Expires: exp}, nil
}

// b64 decodes base64url without padding, as JWTs use it. Trailing padding is
// tolerated (it changes nothing the signature covers: the signing input is the
// text as sent).
func b64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// audContains reports whether the aud claim (a string or an array of strings)
// holds want.
func audContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return false
	}
	for _, a := range many {
		if a == want {
			return true
		}
	}
	return false
}

// numericDate reads a JWT NumericDate (seconds, possibly fractional).
func numericDate(n json.Number) (time.Time, error) {
	if i, err := n.Int64(); err == nil {
		return time.Unix(i, 0), nil
	}
	f, err := n.Float64()
	if err != nil || f < 0 || f > 1<<40 {
		return time.Time{}, errors.New("bad NumericDate")
	}
	return time.Unix(int64(f), 0), nil
}

// key returns the public key for kid. The cached set is used while it is
// younger than Refresh; an older set, or a kid not in it, triggers a fetch
// (single-flight, at most one attempt per MinRefetch). A set whose refreshes
// keep failing stays usable for MaxStale after its last successful fetch.
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := v.now()
	v.mu.Lock()
	k, have := v.keys[kid]
	fresh := !v.fetchedAt.IsZero() && now.Sub(v.fetchedAt) < v.refresh
	v.mu.Unlock()
	if have && fresh {
		return k, nil
	}
	v.fetch(ctx)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fetchedAt.IsZero() || v.now().Sub(v.fetchedAt) >= v.maxStale {
		return nil, ErrNoKeys
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

// Prefetch loads the key set once (at startup), so a wrong team domain shows
// up in the log at once rather than on the first sign-in. It returns the fetch
// error.
func (v *Verifier) Prefetch(ctx context.Context) error {
	if v == nil {
		return nil
	}
	return v.fetch(ctx)
}

// fetch refreshes the key set unless another caller did (or tried) within
// MinRefetch. The fetch runs under its own timeout, detached from the request
// that triggered it, so one cancelled request cannot fail it for everyone
// waiting.
func (v *Verifier) fetch(ctx context.Context) error {
	v.fetchMu.Lock()
	defer v.fetchMu.Unlock()
	v.mu.Lock()
	recent := !v.attemptedAt.IsZero() && v.now().Sub(v.attemptedAt) < v.minRefetch
	v.mu.Unlock()
	if recent {
		return nil
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultTimeout)
	defer cancel()
	keys, err := v.load(fctx)
	v.mu.Lock()
	v.attemptedAt = v.now()
	if err == nil {
		v.keys = keys
		v.fetchedAt = v.attemptedAt
	}
	v.mu.Unlock()
	if err != nil {
		v.log.Warn("Cloudflare Access: cannot load the signing keys; check KIPPLE_ACCESS_TEAM_DOMAIN",
			"url", v.certsURL, "err", err)
	}
	return err
}

type jwks struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (v *Verifier) load(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Errorf("timed out: %w", err)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCertsBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCertsBody {
		return nil, errors.New("key set too large")
	}
	var set jwks
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("key set: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Alg != "" && k.Alg != "RS256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := rsaKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("key set has no usable RS256 keys")
	}
	return keys, nil
}

func rsaKey(nb64, eb64 string) (*rsa.PublicKey, error) {
	nb, err := b64(nb64)
	if err != nil {
		return nil, err
	}
	eb, err := b64(eb64)
	if err != nil || len(eb) == 0 || len(eb) > 4 {
		return nil, errors.New("bad exponent")
	}
	n := new(big.Int).SetBytes(nb)
	if n.BitLen() < minRSABits {
		return nil, errors.New("key too short")
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	if e < 3 || e%2 == 0 {
		return nil, errors.New("bad exponent")
	}
	return &rsa.PublicKey{N: n, E: e}, nil
}
