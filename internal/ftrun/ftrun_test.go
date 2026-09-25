package ftrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/store"
)

type fakeExt struct {
	calls   atomic.Int32
	cur     atomic.Int32
	max     atomic.Int32
	gate    chan struct{} // when set, Extract waits for it
	started chan struct{} // signalled (non-blocking) at the start of every call
	fn      func(t extract.Target) (extract.Result, error)
}

func (f *fakeExt) Extract(ctx context.Context, t extract.Target) (extract.Result, error) {
	f.calls.Add(1)
	n := f.cur.Add(1)
	defer f.cur.Add(-1)
	for {
		m := f.max.Load()
		if n <= m || f.max.CompareAndSwap(m, n) {
			break
		}
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return extract.Result{}, ctx.Err()
		}
	}
	if f.fn != nil {
		return f.fn(t)
	}
	return extract.Result{HTML: "<p>text</p>", Text: "text", WordCount: 1, SourceURL: t.URL}, nil
}

type env struct {
	t  *testing.T
	db *store.DB
	fd int64
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/kipple.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fd, err := db.AddFeed(context.Background(), store.NewFeed{URL: "https://feed.test/f", AllowPrivateNet: true})
	require.NoError(t, err)
	e := &env{t: t, db: db, fd: fd}
	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", fd)
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

var nextID atomic.Int64

// item inserts a live item and returns it as GetFulltextItem sees it.
func (e *env) item(url string) store.FulltextItem {
	e.t.Helper()
	id := nextID.Add(1) + 1_000_000_000
	e.exec(`INSERT INTO items (id, feed_id, uid, url, title, author, word_count, content_hash, text_hash, published_at, sort_at, read)
		VALUES (?, ?, ?, ?, 't', '', 1, 'h', 'h', 1, 1, 0)`, id, e.fd, fmt.Sprintf("u%d", id), url)
	e.exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?, '<p>x</p>', 'x')`, id)
	it, ok, err := e.db.GetFulltextItem(context.Background(), id)
	require.NoError(e.t, err)
	require.True(e.t, ok)
	return it
}

func (e *env) rows(id int64) (ok, failed int) {
	e.t.Helper()
	r := e.db.Reader()
	require.NoError(e.t, r.QueryRow(`SELECT count(*) FROM item_fulltext WHERE item_id = ? AND content_html IS NOT NULL`, id).Scan(&ok))
	require.NoError(e.t, r.QueryRow(`SELECT count(*) FROM item_fulltext WHERE item_id = ? AND error IS NOT NULL`, id).Scan(&failed))
	return ok, failed
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestConcurrentCallersShareOneRunAndSeeItStored(t *testing.T) {
	e := newEnv(t)
	fx := &fakeExt{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	r := New(Options{DB: e.db, Extractor: fx, Log: quiet()})
	it := e.item("https://art.test/1")
	req := Request{Item: it, Now: 100, Timeout: 5 * time.Second}

	type res struct {
		out Outcome
		err error
		ok  int
	}
	results := make(chan res, 3)
	run := func(background bool) {
		rq := req
		rq.Background = background
		out, err := r.Run(context.Background(), rq)
		ok, _ := e.rows(it.ID) // what a joiner may rely on: the row exists when Run returns
		results <- res{out, err, ok}
	}
	go run(true)
	<-fx.started
	go run(false)
	go run(false)
	time.Sleep(100 * time.Millisecond) // let both joiners reach the wait
	close(fx.gate)

	joined := 0
	for i := 0; i < 3; i++ {
		x := <-results
		require.NoError(t, x.err)
		require.Equal(t, 1, x.ok, "stored before any caller is released")
		require.True(t, x.out.Written)
		if x.out.Joined {
			joined++
		}
	}
	require.Equal(t, 2, joined)
	require.EqualValues(t, 1, fx.calls.Load(), "the page is fetched once")
}

func TestPerHostLimitCoversEveryCaller(t *testing.T) {
	e := newEnv(t)
	fx := &fakeExt{fn: func(t extract.Target) (extract.Result, error) {
		time.Sleep(60 * time.Millisecond)
		return extract.Result{HTML: "<p>t</p>", Text: "t", WordCount: 1}, nil
	}}
	r := New(Options{DB: e.db, Extractor: fx, PerHost: 2, Log: quiet()})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		it := e.item(fmt.Sprintf("https://one.test/%d", i))
		wg.Add(1)
		go func(bg bool) {
			defer wg.Done()
			_, err := r.Run(context.Background(), Request{Item: it, Now: 1, Timeout: 5 * time.Second, Background: bg})
			require.NoError(t, err)
		}(i%2 == 0) // pool and endpoint style callers alike
	}
	wg.Wait()
	require.EqualValues(t, 2, fx.max.Load(), "never more than 2 at once for one host")
	require.EqualValues(t, 6, fx.calls.Load())
}

func TestPanicBecomesAStoredPermanentError(t *testing.T) {
	e := newEnv(t)
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, nil))
	fx := &fakeExt{fn: func(extract.Target) (extract.Result, error) { panic("hostile page") }}
	r := New(Options{DB: e.db, Extractor: fx, Log: log})
	for _, bg := range []bool{true, false} {
		it := e.item(fmt.Sprintf("https://art.test/panic-%v", bg))
		out, err := r.Run(context.Background(), Request{Item: it, Now: 1, Timeout: time.Second, Background: bg})
		require.NoError(t, err)
		require.NotEmpty(t, out.Save.Error)
		require.False(t, out.Save.ErrorTransient, "the same page will panic again")
		_, failed := e.rows(it.ID)
		require.Equal(t, 1, failed)
		got, _, _ := e.db.GetFulltextItem(context.Background(), it.ID)
		require.False(t, got.ErrorTransient)
		// the id is free again and the slot was released
		fx.fn = func(t extract.Target) (extract.Result, error) {
			return extract.Result{HTML: "<p>x</p>", Text: "x", WordCount: 1}, nil
		}
		out, err = r.Run(context.Background(), Request{Item: got, Now: 2, Timeout: time.Second})
		require.NoError(t, err)
		require.Empty(t, out.Save.Error)
		fx.fn = func(extract.Target) (extract.Result, error) { panic("hostile page") }
	}
	require.Contains(t, logs.String(), "extraction panicked")
	require.Contains(t, logs.String(), "goroutine", "the stack is logged")
}

func TestBackgroundSaveIsGuardedAndShutdownStoresNothing(t *testing.T) {
	e := newEnv(t)
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	fx := &fakeExt{gate: gate, started: started}
	r := New(Options{DB: e.db, Extractor: fx, Log: quiet()})

	// Full text switched off while the page is being fetched.
	it := e.item("https://art.test/guard")
	done := make(chan Outcome, 1)
	go func() {
		out, err := r.Run(context.Background(), Request{Item: it, Now: 1, Timeout: 5 * time.Second, Background: true})
		require.NoError(t, err)
		done <- out
	}()
	<-started
	e.exec("UPDATE feeds SET fulltext = 0 WHERE id = ?", e.fd)
	close(gate)
	out := <-done
	require.False(t, out.Written)
	ok, failed := e.rows(it.ID)
	require.Zero(t, ok+failed)

	// Shutdown mid-run: not stored, not recorded as the page's failure.
	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", e.fd)
	fx2 := &fakeExt{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	r2 := New(Options{DB: e.db, Extractor: fx2, Log: quiet()})
	it2 := e.item("https://art.test/shutdown")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := r2.Run(ctx, Request{Item: it2, Now: 1, Timeout: 5 * time.Second, Background: true})
		errc <- err
	}()
	<-fx2.started
	cancel()
	require.True(t, errors.Is(<-errc, ErrAborted))
	ok, failed = e.rows(it2.ID)
	require.Zero(t, ok+failed)
}

func TestOnDemandRunIsDetachedFromItsCaller(t *testing.T) {
	e := newEnv(t)
	fx := &fakeExt{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	r := New(Options{DB: e.db, Extractor: fx, Log: quiet()})
	it := e.item("https://art.test/detached")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Run(ctx, Request{Item: it, Now: 1, Timeout: 5 * time.Second})
	}()
	<-fx.started
	cancel()
	close(fx.gate)
	<-done
	ok, _ := e.rows(it.ID)
	require.Equal(t, 1, ok, "the result is stored even though the caller's context ended")
}
