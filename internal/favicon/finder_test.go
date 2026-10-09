package favicon

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

type fenv struct {
	t   *testing.T
	db  *store.DB
	clk *clock.Fake
	f   *Finder
	ctx context.Context
}

func newFEnv(t *testing.T) *fenv {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_800_000_000, 0))
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := fetch.NewClient(fetch.ClientOptions{})
	f := New(Options{DB: db, Guard: client.Transport, UserAgent: client.DefaultUserAgent, Clock: clk})
	return &fenv{t: t, db: db, clk: clk, f: f, ctx: context.Background()}
}

func (e *fenv) exec(q string, args ...any) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (e *fenv) int(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	require.NoError(e.t, e.db.Reader().QueryRowContext(e.ctx, q, args...).Scan(&n))
	return n
}

// fetchedFeed adds a feed that has had one successful fetch.
func (e *fenv) fetchedFeed(url, site string, private bool) int64 {
	e.t.Helper()
	id, err := e.db.AddFeed(e.ctx, store.NewFeed{URL: url, AllowPrivateNet: private})
	require.NoError(e.t, err)
	e.exec("UPDATE feeds SET last_success_at = ?, site_url = ? WHERE id = ?", e.clk.Now().Unix(), site, id)
	return id
}

func iconSite(t *testing.T, hits *atomic.Int32) *httptest.Server {
	ico := icoOf(t, 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(ico)
			return
		}
		_, _ = w.Write([]byte("<html><head></head><body></body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFinderStoresAnIconThenWaitsAWeek(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	id := e.fetchedFeed(srv.URL+"/feed.xml", "", true) // no site_url: the feed's origin

	did, err := e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	data, ct, ok, err := e.db.FeedIconAny(e.ctx, id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "image/x-icon", ct)
	require.Equal(t, icoOf(t, 32), data)
	subs, err := e.db.Subscriptions(e.ctx)
	require.NoError(t, err)
	require.Len(t, subs[0].IconHash, 16, "the Reader API iconUrl now has a hash")
	require.Equal(t, e.clk.Now().Add(RecheckAfter).Unix(), e.int("SELECT next_check_at FROM feed_icon_checks WHERE feed_id = ?", id))

	before := hits.Load()
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.False(t, did, "not due again within the week")
	e.clk.Set(e.clk.Now().Add(RecheckAfter))
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Greater(t, hits.Load(), before)
}

// A failing lookup backs off in feed_icon_checks and never touches the feed's
// health. Without allow_private_net the guard refuses the loopback test server.
func TestFinderFailureBacksOffAndLeavesFeedHealthAlone(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	// The feed lives on the site's own host (another port): its exception, once
	// on, covers the site (ContentScopedTransport).
	id := e.fetchedFeed("http://127.0.0.1:1/rss", srv.URL+"/", false)

	did, err := e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Zero(t, hits.Load(), "blocked at dial time")
	require.Zero(t, e.int("SELECT count(*) FROM feed_icons"))
	require.Equal(t, int64(1), e.int("SELECT failures FROM feed_icon_checks WHERE feed_id = ?", id))
	require.Equal(t, e.clk.Now().Add(6*time.Hour).Unix(), e.int("SELECT next_check_at FROM feed_icon_checks WHERE feed_id = ?", id))
	require.Equal(t, int64(1), e.int("SELECT count(*) FROM feed_icon_checks WHERE last_error LIKE '%not allowed%'"))
	require.Equal(t, int64(0), e.int("SELECT consecutive_failures FROM feeds WHERE id = ?", id))
	require.Equal(t, int64(1), e.int("SELECT count(*) FROM feeds WHERE id = ? AND last_error IS NULL AND enabled = 1", id))

	e.clk.Set(e.clk.Now().Add(6 * time.Hour))
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Equal(t, int64(2), e.int("SELECT failures FROM feed_icon_checks WHERE feed_id = ?", id))
	require.Equal(t, e.clk.Now().Add(12*time.Hour).Unix(), e.int("SELECT next_check_at FROM feed_icon_checks WHERE feed_id = ?", id))

	// Turning the per-feed opt-in on is honoured on the next due lookup.
	e.exec("UPDATE feeds SET allow_private_net = 1 WHERE id = ?", id)
	e.clk.Set(e.clk.Now().Add(12 * time.Hour))
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Equal(t, int64(1), e.int("SELECT count(*) FROM feed_icons WHERE feed_id = ?", id))
	require.Equal(t, int64(0), e.int("SELECT failures FROM feed_icon_checks WHERE feed_id = ?", id))
}

// A site_url that moves to another host is looked up at once, not after the
// week; one that changes only its path or query is the same site.
func TestFinderRechecksWhenTheSiteChanges(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	id := e.fetchedFeed(srv.URL+"/feed.xml", srv.URL+"/", true)
	did, err := e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	e.exec("UPDATE feeds SET site_url = ? WHERE id = ?", srv.URL+"/moved/?sid=2", id)
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.False(t, did, "the same site")
	moved := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/"
	e.exec("UPDATE feeds SET site_url = ? WHERE id = ?", moved, id)
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	var site string
	require.NoError(t, e.db.Reader().QueryRow("SELECT site_url FROM feed_icon_checks WHERE feed_id = ?", id).Scan(&site))
	require.Equal(t, moved, site)
}

// A cancelled lookup (shutdown) writes nothing, so the feed stays due.
func TestFinderWritesNothingWhenCancelled(t *testing.T) {
	e := newFEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	e.fetchedFeed(srv.URL+"/feed.xml", "", true)
	ctx, cancel := context.WithCancel(e.ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	did, err := e.f.RunOnce(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, did)
	require.Zero(t, e.int("SELECT count(*) FROM feed_icon_checks"))
}

func TestFinderLoopRunsOnTheTickAndStops(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	id := e.fetchedFeed(srv.URL+"/feed.xml", "", true)
	busy := atomic.Bool{}
	busy.Store(true)
	f := New(Options{DB: e.db, Guard: fetch.NewClient(fetch.ClientOptions{}).Transport, Clock: e.clk, Poll: time.Minute, Gap: time.Millisecond,
		Busy: busy.Load})
	f.Start()
	defer f.Stop()

	e.clk.Advance(time.Minute)
	time.Sleep(200 * time.Millisecond)
	require.Zero(t, hits.Load(), "a scheduler run is active: the pass yields")

	busy.Store(false)
	require.Eventually(t, func() bool {
		e.clk.Advance(time.Minute)
		return e.int("SELECT count(*) FROM feed_icons WHERE feed_id = ?", id) == 1
	}, 10*time.Second, 50*time.Millisecond)
	f.Stop()
	f.Stop() // idempotent

	g := New(Options{DB: e.db, Guard: fetch.NewClient(fetch.ClientOptions{}).Transport})
	g.Stop() // never started: returns at once
}
