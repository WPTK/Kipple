package access

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testIssuer = "https://myteam.cloudflareaccess.com"
	testAUD    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var (
	keyOnce          sync.Once
	keyA, keyB, key3 *rsa.PrivateKey
)

// testKeys generates the RSA keys once per test binary (2048-bit generation is
// the slow part of these tests).
func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		for _, k := range []**rsa.PrivateKey{&keyA, &keyB, &key3} {
			if *k, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
				panic(err)
			}
		}
	})
	return keyA, keyB, key3
}

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jwk(kid string, k *rsa.PublicKey) map[string]string {
	return map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
		"n": enc(k.N.Bytes()), "e": enc(big.NewInt(int64(k.E)).Bytes())}
}

// certsServer serves a JWKS document built by keys() on every request and
// counts the requests.
type certsServer struct {
	*httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	keys []map[string]string
	code int
}

func newCertsServer(t *testing.T, keys ...map[string]string) *certsServer {
	cs := &certsServer{keys: keys, code: http.StatusOK}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.hits.Add(1)
		cs.mu.Lock()
		defer cs.mu.Unlock()
		if cs.code != http.StatusOK {
			w.WriteHeader(cs.code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": cs.keys})
	}))
	t.Cleanup(cs.Close)
	return cs
}

func (cs *certsServer) set(code int, keys ...map[string]string) {
	cs.mu.Lock()
	cs.code, cs.keys = code, keys
	cs.mu.Unlock()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newVerifier(t *testing.T, cs *certsServer, clk *clock) *Verifier {
	t.Helper()
	v, err := New("myteam.cloudflareaccess.com", testAUD, Options{CertsURL: cs.URL, Now: clk.now, Client: cs.Client()})
	require.NoError(t, err)
	return v
}

func sign(t *testing.T, k *rsa.PrivateKey, hdr, claims map[string]any) string {
	t.Helper()
	hb, err := json.Marshal(hdr)
	require.NoError(t, err)
	cb, err := json.Marshal(claims)
	require.NoError(t, err)
	in := enc(hb) + "." + enc(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return in + "." + enc(sig)
}

func claimsAt(now time.Time) map[string]any {
	return map[string]any{
		"iss": testIssuer, "aud": []string{testAUD}, "email": "owner@example.com", "sub": "user-1",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(), "type": "app",
	}
}

func rs(kid string) map[string]any { return map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"} }

func TestVerifyValid(t *testing.T) {
	a, _, _ := testKeys(t)
	cs := newCertsServer(t, jwk("a", &a.PublicKey))
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	require.Equal(t, testIssuer, v.Issuer())

	id, err := v.Verify(context.Background(), sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err)
	require.Equal(t, "owner@example.com", id.Email)
	require.Equal(t, "user-1", id.Subject)
	require.Equal(t, clk.now().Add(time.Hour), id.Expires)

	// aud as a plain string is accepted too.
	c := claimsAt(clk.now())
	c["aud"] = testAUD
	_, err = v.Verify(context.Background(), sign(t, a, rs("a"), c))
	require.NoError(t, err)

	// Through the request header.
	r := httptest.NewRequest("GET", "/api/auth/me", nil)
	r.Header.Set(Header, sign(t, a, rs("a"), claimsAt(clk.now())))
	id, err = v.VerifyRequest(r)
	require.NoError(t, err)
	require.Equal(t, "owner@example.com", id.Email)
	require.Equal(t, int32(1), cs.hits.Load(), "the key set is cached")
}

func TestVerifyRejects(t *testing.T) {
	a, b, _ := testKeys(t)
	cs := newCertsServer(t, jwk("a", &a.PublicKey), jwk("b", &b.PublicKey))
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	now := clk.now()
	with := func(k string, val any) map[string]any {
		c := claimsAt(now)
		if val == nil {
			delete(c, k)
		} else {
			c[k] = val
		}
		return c
	}
	good := sign(t, a, rs("a"), claimsAt(now))
	parts := strings.Split(good, ".")

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", ErrNoToken},
		{"expired", sign(t, a, rs("a"), with("exp", now.Add(-time.Minute).Unix())), ErrExpired},
		{"no exp", sign(t, a, rs("a"), with("exp", nil)), ErrMalformed},
		{"not yet valid", sign(t, a, rs("a"), with("nbf", now.Add(5*time.Minute).Unix())), ErrNotYet},
		{"wrong aud", sign(t, a, rs("a"), with("aud", []string{"another-app"})), ErrAudience},
		{"wrong aud string", sign(t, a, rs("a"), with("aud", "another-app")), ErrAudience},
		{"no aud", sign(t, a, rs("a"), with("aud", nil)), ErrAudience},
		{"wrong iss", sign(t, a, rs("a"), with("iss", "https://otherteam.cloudflareaccess.com")), ErrIssuer},
		{"iss without scheme", sign(t, a, rs("a"), with("iss", "myteam.cloudflareaccess.com")), ErrIssuer},
		{"signed by a different key than kid names", sign(t, b, rs("a"), claimsAt(now)), ErrSignature},
		{"tampered payload", parts[0] + "." + enc([]byte(`{"iss":"`+testIssuer+`","aud":"`+testAUD+`","exp":9999999999,"email":"evil@example.com"}`)) + "." + parts[2], ErrSignature},
		{"tampered signature", parts[0] + "." + parts[1] + "." + enc([]byte("not a signature")), ErrSignature},
		{"alg none", enc([]byte(`{"alg":"none","kid":"a"}`)) + "." + parts[1] + ".", ErrAlg},
		{"alg HS256", sign(t, a, map[string]any{"alg": "HS256", "kid": "a"}, claimsAt(now)), ErrAlg},
		{"unknown kid", sign(t, a, rs("zzz"), claimsAt(now)), ErrUnknownKey},
		{"two parts", parts[0] + "." + parts[1], ErrMalformed},
		{"bad base64", "!!!." + parts[1] + "." + parts[2], ErrMalformed},
		{"huge", strings.Repeat("a", maxTokenLen+1), ErrMalformed},
	}
	for _, tc := range cases {
		_, err := v.Verify(context.Background(), tc.token)
		require.ErrorIs(t, err, tc.want, tc.name)
	}

	// exp is honoured with the 30 s leeway: 10 s past is fine, 31 s is not.
	tok := sign(t, a, rs("a"), with("exp", now.Unix()))
	clk.add(10 * time.Second)
	_, err := v.Verify(context.Background(), tok)
	require.NoError(t, err)
	clk.add(21 * time.Second)
	_, err = v.Verify(context.Background(), tok)
	require.ErrorIs(t, err, ErrExpired)
}

func TestNilVerifierVerifiesNothing(t *testing.T) {
	var v *Verifier
	a, _, _ := testKeys(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(Header, sign(t, a, rs("a"), claimsAt(time.Now())))
	_, err := v.VerifyRequest(r)
	require.ErrorIs(t, err, ErrNoToken)
	require.NoError(t, v.Prefetch(context.Background()))
}

func TestKeyRotationAndRefetchLimit(t *testing.T) {
	a, b, _ := testKeys(t)
	cs := newCertsServer(t, jwk("a", &a.PublicKey))
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	ctx := context.Background()

	_, err := v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err)
	require.Equal(t, int32(1), cs.hits.Load())

	// Cloudflare rotates: key b appears. A token signed by b triggers one
	// refetch, but not within a minute of the previous fetch.
	cs.set(http.StatusOK, jwk("a", &a.PublicKey), jwk("b", &b.PublicKey))
	_, err = v.Verify(ctx, sign(t, b, rs("b"), claimsAt(clk.now())))
	require.ErrorIs(t, err, ErrUnknownKey, "refetch is rate-limited")
	require.Equal(t, int32(1), cs.hits.Load())
	clk.add(61 * time.Second)
	_, err = v.Verify(ctx, sign(t, b, rs("b"), claimsAt(clk.now())))
	require.NoError(t, err)
	require.Equal(t, int32(2), cs.hits.Load())

	// A flood of unknown kids fetches at most once a minute.
	for i := 0; i < 20; i++ {
		_, err = v.Verify(ctx, sign(t, a, rs("nope"), claimsAt(clk.now())))
		require.ErrorIs(t, err, ErrUnknownKey)
	}
	require.Equal(t, int32(2), cs.hits.Load())

	// After the refresh interval a known kid still verifies at once from the
	// cache (stale-while-revalidate) while the set is refetched in the
	// background; key a has been retired upstream, so once that refresh lands
	// it no longer verifies.
	cs.set(http.StatusOK, jwk("b", &b.PublicKey))
	clk.add(time.Hour + time.Second)
	_, err = v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err)
	v.bg.Wait()
	require.Equal(t, int32(3), cs.hits.Load())
	_, err = v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.ErrorIs(t, err, ErrUnknownKey)
	require.Equal(t, int32(3), cs.hits.Load())
}

func TestStaleKeysWhenRefreshFails(t *testing.T) {
	a, _, _ := testKeys(t)
	cs := newCertsServer(t, jwk("a", &a.PublicKey))
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	ctx := context.Background()
	require.NoError(t, v.Prefetch(ctx))

	cs.set(http.StatusInternalServerError)
	clk.add(2 * time.Hour)
	_, err := v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err, "a cached set outlives a failed refresh")
	v.bg.Wait()
	require.Equal(t, int32(2), cs.hits.Load(), "the stale set was refreshed in the background")

	clk.add(23 * time.Hour)
	_, err = v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.ErrorIs(t, err, ErrNoKeys, "but not past MaxStale")

	cs.set(http.StatusOK, jwk("a", &a.PublicKey))
	clk.add(2 * time.Minute)
	_, err = v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err, "a successful fetch recovers")
}

func TestKeySetUnreachable(t *testing.T) {
	a, _, _ := testKeys(t)
	cs := newCertsServer(t)
	cs.set(http.StatusNotFound)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	require.Error(t, v.Prefetch(context.Background()))
	clk.add(2 * time.Minute)
	_, err := v.Verify(context.Background(), sign(t, a, rs("a"), claimsAt(clk.now())))
	require.ErrorIs(t, err, ErrNoKeys)
}

func TestKeySetFiltersUnusableKeys(t *testing.T) {
	a, b, c := testKeys(t)
	small, err := rsa.GenerateKey(rand.Reader, 1024) // #nosec G403 -- deliberately weak: the key set must refuse it
	require.NoError(t, err)
	hs := jwk("b", &b.PublicKey)
	hs["alg"] = "HS256"
	encKey := jwk("c", &c.PublicKey)
	encKey["use"] = "enc"
	cs := newCertsServer(t, jwk("a", &a.PublicKey), hs, encKey, jwk("small", &small.PublicKey), map[string]string{"kty": "EC", "kid": "ec"})
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v := newVerifier(t, cs, clk)
	ctx := context.Background()
	_, err = v.Verify(ctx, sign(t, a, rs("a"), claimsAt(clk.now())))
	require.NoError(t, err)
	for _, kid := range []string{"b", "c", "small", "ec"} {
		k := map[string]*rsa.PrivateKey{"b": b, "c": c, "small": small, "ec": a}[kid]
		_, err = v.Verify(ctx, sign(t, k, rs(kid), claimsAt(clk.now())))
		require.ErrorIs(t, err, ErrUnknownKey, kid)
	}
}

func TestNewValidates(t *testing.T) {
	_, err := New("", testAUD, Options{})
	require.Error(t, err)
	_, err = New("myteam.cloudflareaccess.com", " ", Options{})
	require.Error(t, err)
	v, err := New("https://myteam.cloudflareaccess.com/", testAUD, Options{})
	require.NoError(t, err)
	require.Equal(t, testIssuer, v.Issuer())
	require.Equal(t, testIssuer+"/cdn-cgi/access/certs", v.certsURL)
}

// A display-only check never waits on a slow key-set endpoint.
func TestVerifyRequestCachedNeverWaits(t *testing.T) {
	a, _, _ := testKeys(t)
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{jwk("a", &a.PublicKey)}})
	}))
	t.Cleanup(srv.Close)
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	v, err := New("myteam.cloudflareaccess.com", testAUD, Options{CertsURL: srv.URL, Client: srv.Client(), Now: clk.now})
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/api/bootstrap", nil)
	r.Header.Set(Header, sign(t, a, rs("a"), claimsAt(clk.now())))

	start := time.Now()
	_, err = v.VerifyRequestCached(r)
	require.ErrorIs(t, err, ErrNoKeys)
	require.Less(t, time.Since(start), 2*time.Second, "no wait for the fetch")
	// A second cached check while that fetch is in flight starts no other.
	_, err = v.VerifyRequestCached(r)
	require.ErrorIs(t, err, ErrNoKeys)
	close(release)
	v.bg.Wait()
	require.Equal(t, int32(1), hits.Load())
	id, err := v.VerifyRequestCached(r)
	require.NoError(t, err, "the background fetch filled the cache")
	require.Equal(t, "owner@example.com", id.Email)
}

// A wrong team domain (a real team, but not the one signing the tokens) shows
// up as unknown key ids: that warns, at most once an hour.
func TestRefusedTokenWarnsHourly(t *testing.T) {
	a, b, _ := testKeys(t)
	cs := newCertsServer(t, jwk("b", &b.PublicKey))
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	var buf strings.Builder
	var mu sync.Mutex
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), &slog.HandlerOptions{Level: slog.LevelInfo}))
	v, err := New("myteam.cloudflareaccess.com", testAUD, Options{CertsURL: cs.URL, Client: cs.Client(), Now: clk.now, Logger: logger})
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(Header, sign(t, a, rs("a"), claimsAt(clk.now())))
	for i := 0; i < 5; i++ {
		_, err = v.VerifyRequest(r)
		require.ErrorIs(t, err, ErrUnknownKey)
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return strings.Count(buf.String(), "token refused") }
	require.Equal(t, 1, count())
	// A forged signature is someone trying a token on: debug only.
	clk.add(2 * time.Hour)
	r.Header.Set(Header, sign(t, a, rs("b"), claimsAt(clk.now())))
	_, err = v.VerifyRequest(r)
	require.ErrorIs(t, err, ErrSignature)
	require.Equal(t, 1, count())
	v.bg.Wait()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
