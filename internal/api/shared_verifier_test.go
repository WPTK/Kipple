package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/greader"
	"github.com/WPTK/kipple/internal/store"
)

// TestClientLoginAndWebLoginShareOneHashingSlot wires the web API and the Reader
// API to one Verifier, as runServe does, and proves the two logins never run
// argon2 at the same time.
func TestClientLoginAndWebLoginShareOneHashingSlot(t *testing.T) {
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.CreateAccount(context.Background(), store.Account{
		Username: testUser, PasswordHash: "web-hash", APIPasswordHash: "api-hash", Secret: testSecret})
	require.NoError(t, err)

	var cur, peak atomic.Int32
	var calls atomic.Int32
	ver := auth.NewVerifier(nil, auth.VerifierOptions{Wait: 10 * time.Second, Check: func(pw, phc string) bool {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		calls.Add(1)
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		return pw == testPass
	}})

	mux := http.NewServeMux()
	New(Options{DB: db, Sched: &fakeSched{}, Hub: events.New(), Verifier: ver}).Register(mux)
	front := greader.New(greader.Options{DB: db, Verifier: ver}).Front(mux)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		// one ClientLogin per client address: a client has one attempt in flight
		remote := "192.0.2." + strconv.Itoa(10+i) + ":2"
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(loginBody(testPass)))
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-Kipple-Client", "web")
			r.RemoteAddr = "10.20.30." + strconv.Itoa(30+i) + ":1"
			w := httptest.NewRecorder()
			front.ServeHTTP(w, r)
			require.Equal(t, http.StatusNoContent, w.Code)
		}()
		go func() {
			defer wg.Done()
			form := url.Values{"Email": {testUser}, "Passwd": {testPass}}.Encode()
			r := httptest.NewRequest("POST", "/api/greader.php/accounts/ClientLogin", strings.NewReader(form))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.RemoteAddr = remote
			w := httptest.NewRecorder()
			front.ServeHTTP(w, r)
			body, _ := io.ReadAll(w.Result().Body)
			require.Equal(t, http.StatusOK, w.Code, string(body))
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, peak.Load(), "web login and ClientLogin never hash concurrently")
	require.GreaterOrEqual(t, calls.Load(), int32(2), "both kinds hashed")
}
