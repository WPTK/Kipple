package greader

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/auth"
	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/feedurl"
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
	wakes  atomic.Int32
	checks atomic.Int32
	// paced counts ClientLogin pacing waits; each one advances clk instead of sleeping.
	paced atomic.Int32
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
	})
	h.api.fails.MaxWait = time.Hour // the fake clock makes every wait instant
	h.api.fails.After = func(d time.Duration) <-chan time.Time {
		h.paced.Add(1)
		clk.Advance(d)
		ch := make(chan time.Time, 1)
		ch <- clk.Now()
		return ch
	}
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
	return h.doFrom("192.0.2.10:5555", method, path, body, hdr)
}

// doFrom is do from the TCP peer remote.
func (h *harness) doFrom(remote, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = remote
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

// ---- seeding ----

// addFolder creates a folder (or returns the existing id).
func (h *harness) addFolder(name string) int64 {
	h.t.Helper()
	var id int64
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM folders WHERE name = ? COLLATE NOCASE", name).Scan(&id)
		if err == sql.ErrNoRows {
			r, err := tx.ExecContext(ctx, "INSERT INTO folders (name, position) SELECT ?, COALESCE(MAX(position)+1,1) FROM folders", name)
			if err != nil {
				return err
			}
			id, err = r.LastInsertId()
			return err
		}
		return err
	}))
	return id
}

// addFeed inserts a feed directly (no fetch). folder "" = Uncategorized.
func (h *harness) addFeed(feedURL, title, folder string) int64 {
	h.t.Helper()
	fid := int64(1)
	if folder != "" {
		fid = h.addFolder(folder)
	}
	var id int64
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		key, _ := feedurl.Key(feedURL)
		host, _ := feedurl.Host(feedURL)
		r, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, site_url, next_fetch_at)
			VALUES (?,?,?,?,?,?,?)`, fid, feedURL, key, host, title, "https://"+host+"/", 4102444800)
		if err != nil {
			return err
		}
		id, err = r.LastInsertId()
		return err
	}))
	return id
}

type itemSeed struct {
	ID        int64 // 0 = next sequential id
	UID       string
	Title     string
	Author    string
	URL       string
	HTML      string
	Published int64
	Updated   int64
	Read      bool
	Starred   bool
	ChangedAt int64 // content_changed_at, seconds
	Enclosure string
}

var seedSeq atomic.Int64

// baseID is the first sequential seed id: 2026-09-24 12:00:00 UTC in microseconds.
const baseID = int64(1790251200) * 1_000_000

// addItem inserts an item and its content directly and returns its id.
func (h *harness) addItem(feed int64, s itemSeed) int64 {
	h.t.Helper()
	n := seedSeq.Add(1)
	if s.ID == 0 {
		s.ID = baseID + n*1000
	}
	if s.UID == "" {
		s.UID = fmt.Sprintf("g:%032d", s.ID)
	}
	if s.Published == 0 {
		s.Published = s.ID/1_000_000 - 60
	}
	if s.HTML == "" {
		s.HTML = "<p>body of " + s.Title + "</p>"
	}
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		var upd, changed any
		if s.Updated != 0 {
			upd = s.Updated
		}
		if s.ChangedAt != 0 {
			changed = s.ChangedAt
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, starred, published_at, updated_at, sort_at,
			content_changed_at, uid, content_hash, text_hash, url, title, author)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			s.ID, feed, b2i(s.Read), b2i(s.Starred), s.Published, upd, s.Published, changed, s.UID, "ch", "th", s.URL, s.Title, s.Author); err != nil {
			return err
		}
		var enc any
		if s.Enclosure != "" {
			enc = s.Enclosure
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO item_content (item_id, content_html, content_text, enclosures_json) VALUES (?,?,?,?)",
			s.ID, s.HTML, s.Title, enc)
		return err
	}))
	return s.ID
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// trim moves an item into the ledger (and a restore stub) the way retention does.
func (h *harness) trim(id int64, withStub bool) {
	h.t.Helper()
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			SELECT id, feed_id, uid, read, unixepoch(), unixepoch() FROM items WHERE id = ?`, id); err != nil {
			return err
		}
		if withStub {
			if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
				url, title, author, image_url, origin_title, fulltext_mode, content_html, content_text, enclosures_json)
				SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash, i.url, i.title, i.author,
				  i.image_url, i.origin_title, i.fulltext_mode, c.content_html, c.content_text, c.enclosures_json
				FROM items i JOIN item_content c ON c.item_id = i.id WHERE i.id = ?`, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM items WHERE id = ?", id)
		return err
	}))
}

// q runs a scalar query on the reader pool.
func q[T any](h *harness, query string, args ...any) T {
	h.t.Helper()
	var v T
	require.NoError(h.t, h.db.Reader().QueryRowContext(context.Background(), query, args...).Scan(&v))
	return v
}

// jsonBody decodes a response body.
func jsonBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
	return m
}

func execSQL(h *harness, query string, args ...any) error {
	return h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}
