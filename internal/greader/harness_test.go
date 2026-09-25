package greader

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/store"
)

const (
	testUser   = "owner"
	testPass   = "correct-horse"
	testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testHash   = "fake-api-hash"
)

// harness is an API over a temp database with a fake password check (argon2 is
// exercised in the auth package tests) and a fake clock shared with the store.
type harness struct {
	t      *testing.T
	db     *store.DB
	api    *API
	h      http.Handler
	clk    *clock.Fake
	tok    string
	mu     sync.Mutex
	slept  []time.Duration
	wakes  atomic.Int32
	checks atomic.Int32
}

type harnessOpts struct {
	noAPIPassword bool
	logger        *slog.Logger
	logForms      bool
}

func newHarness(t *testing.T, o ...harnessOpts) *harness {
	t.Helper()
	var opt harnessOpts
	if len(o) > 0 {
		opt = o[0]
	}
	clk := clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	acc := store.Account{Username: testUser, PasswordHash: "web-hash", APIPasswordHash: testHash, Secret: testSecret}
	if opt.noAPIPassword {
		acc.APIPasswordHash = ""
	}
	created, err := db.CreateAccount(context.Background(), acc)
	require.NoError(t, err)
	require.True(t, created)

	h := &harness{t: t, db: db, clk: clk}
	ver := auth.NewVerifier([]byte(testSecret), auth.VerifierOptions{Check: func(pw, phc string) bool {
		h.checks.Add(1)
		return pw == testPass && phc == testHash
	}})
	h.api = New(Options{
		DB: db, Logger: opt.logger, Verifier: ver, LogForms: opt.logForms,
		Wake: func() { h.wakes.Add(1) },
		Now:  clk.Now,
		Sleep: func(_ context.Context, d time.Duration) {
			h.mu.Lock()
			h.slept = append(h.slept, d)
			h.mu.Unlock()
		},
	})
	h.h = h.api.Front(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "fallthrough")
	}))
	h.tok = makeToken(testUser, testSecret, testHash)
	return h
}

const base = "/api/greader.php"

// do sends a request with the Authorization header unless hdr overrides it.
func (h *harness) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "192.0.2.10:5555"
	if method == http.MethodPost && body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("Authorization", "GoogleLogin auth="+h.tok)
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func (h *harness) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodGet, base+path, "", nil)
}

func (h *harness) post(path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.do(http.MethodPost, base+path, body, nil)
}

const rd = "/reader/api/0/"
