package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

var base = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

const feedXML = `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>
<item><guid>a</guid><title>A</title><link>https://ex.com/a</link><pubDate>Wed, 23 Sep 2026 10:00:00 +0000</pubDate></item>
<item><guid>b</guid><title>B</title><link>https://ex.com/b</link><pubDate>Wed, 23 Sep 2026 11:00:00 +0000</pubDate></item>
</channel></rss>`

type rig struct {
	t   *testing.T
	db  *store.DB
	clk *clock.Fake
	hub *events.Hub
	s   *Scheduler
	jit atomic.Uint64 // math.Float64bits of the jitter source

	mu  sync.Mutex
	evs []events.Event
}

func newRig(t *testing.T, opt Options) *rig {
	t.Helper()
	r := &rig{t: t, clk: clock.NewFake(base), hub: events.New()}
	r.setJitter(0.5) // factor exactly 1.0
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	var err error
	r.db, err = store.Open(context.Background(), store.Options{Path: t.TempDir() + "/kipple.db", Clock: r.clk, Logger: quiet})
	require.NoError(t, err)
	if opt.Rand == nil {
		opt.Rand = func() float64 { return math.Float64frombits(r.jit.Load()) }
	}
	r.s = New(r.db, fetch.NewClient(fetch.ClientOptions{}), r.hub, r.clk, quiet, opt)

	sub := r.hub.Subscribe(0)
	go func() {
		for ev := range sub.C {
			r.mu.Lock()
			r.evs = append(r.evs, ev)
			r.mu.Unlock()
		}
	}()
	r.s.Start()
	t.Cleanup(func() {
		r.s.Stop()
		select {
		case <-r.s.Stopped():
		case <-time.After(15 * time.Second):
			t.Error("scheduler did not stop")
		}
		r.hub.Close()
		require.NoError(t, r.db.Close())
	})
	return r
}

func (r *rig) setJitter(v float64) { r.jit.Store(math.Float64bits(v)) }

// barrier makes sure a delivered tick has been processed.
func (r *rig) barrier() { r.s.inDispatcher(func() {}) }

func (r *rig) flights() (n int) {
	r.s.inDispatcher(func() { n = len(r.s.flights) })
	return n
}

func (r *rig) events(typ string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, ev := range r.evs {
		if ev.Type != typ {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(ev.Data, &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *rig) waitEvents(typ string, n int) {
	r.t.Helper()
	waitFor(r.t, fmt.Sprintf("%d %s events", n, typ), func() bool { return len(r.events(typ)) >= n })
}

func (r *rig) num(q string, args ...any) int64 {
	r.t.Helper()
	var v int64
	require.NoError(r.t, r.db.Reader().QueryRow(q, args...).Scan(&v))
	return v
}

type sqlTx = sql.Tx

type feedSrv struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newSrv(t *testing.T, h func(path string, w http.ResponseWriter, req *http.Request)) *feedSrv {
	t.Helper()
	fs := &feedSrv{hits: map[string]int{}}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fs.mu.Lock()
		fs.hits[req.URL.Path]++
		fs.mu.Unlock()
		h(req.URL.Path, w, req)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (f *feedSrv) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *feedSrv) total() (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.hits {
		n += c
	}
	return n
}

func serveOK(_ string, w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/rss+xml")
	_, _ = w.Write([]byte(feedXML))
}

func (r *rig) add(url string, mod func(*store.NewFeed)) int64 {
	r.t.Helper()
	nf := store.NewFeed{URL: url, AllowPrivateNet: true}
	if mod != nil {
		mod(&nf)
	}
	id, err := r.db.AddFeed(context.Background(), nf)
	require.NoError(r.t, err)
	return id
}

func (r *rig) setHost(id int64, host string) {
	r.t.Helper()
	require.NoError(r.t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sqlTx) error {
		_, err := tx.ExecContext(ctx, "UPDATE feeds SET host = ? WHERE id = ?", host, id)
		return err
	}))
}

func (r *rig) sql(q string, args ...any) {
	r.t.Helper()
	require.NoError(r.t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sqlTx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (r *rig) next(id int64) int64 { return r.num("SELECT next_fetch_at FROM feeds WHERE id = ?", id) }
func (r *rig) failures(id int64) int64 {
	return r.num("SELECT consecutive_failures FROM feeds WHERE id = ?", id)
}

// ---------------------------------------------------------------------------

func TestIntervalAndPerFeedOverride(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	a := r.add(srv.URL+"/a", nil)
	b := r.add(srv.URL+"/b", func(f *store.NewFeed) { f.IntervalMinutes = 120 })
	r.setHost(b, "other.test") // keep the per-host cap out of the way

	r.s.Wake()
	r.waitEvents("fetch.done", 2)
	require.EqualValues(t, base.Add(30*time.Minute).Unix(), r.next(a), "global interval, jitter factor 1.0")
	require.EqualValues(t, base.Add(120*time.Minute).Unix(), r.next(b), "per-feed override")
	require.Equal(t, 1, srv.count("/a"))
	require.Equal(t, 1, srv.count("/b"))

	r.clk.Advance(29 * time.Minute)
	r.barrier()
	require.Zero(t, r.flights(), "nothing is due at 29 min")
	require.Equal(t, 1, srv.count("/a"))

	r.clk.Advance(2 * time.Minute) // 31 min
	r.waitEvents("fetch.done", 3)
	require.Equal(t, 2, srv.count("/a"))
	require.Equal(t, 1, srv.count("/b"), "the 120 min feed is not due yet")
	require.Equal(t, "unchanged", r.events("fetch.done")[2]["outcome"], "identical body short-circuits on the body hash")

	r.clk.Advance(90 * time.Minute) // 121 min
	waitFor(t, "b refetched", func() bool { return srv.count("/b") == 2 })
	r.waitEvents("fetch.done", 5)
}

func TestInFlightFeedIsNotDispatchedTwice(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	r.add(srv.URL+"/slow", nil)
	for i := 0; i < 5; i++ {
		r.s.Wake()
		r.barrier()
	}
	waitFor(t, "first hit", func() bool { return srv.count("/slow") >= 1 })
	require.Equal(t, 1, r.flights())
	once.Do(func() { close(release) })
	r.waitEvents("fetch.done", 1)
	require.Equal(t, 1, srv.count("/slow"), "wake storms never duplicate an in-flight feed")
}

func TestBackoffGrowthJitterAndReset(t *testing.T) {
	r := newRig(t, Options{})
	var fail atomic.Bool
	fail.Store(true)
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		serveOK(p, w, req)
	})
	id := r.add(srv.URL+"/f", nil)

	delay := 30 * time.Minute
	for n := 1; n <= 8; n++ {
		at := r.clk.Now()
		if n == 1 {
			r.s.Wake()
		} else {
			r.clk.Advance(delay) // the previous next_fetch_at
			delay *= 2
			if delay > 24*time.Hour {
				delay = 24 * time.Hour
			}
			at = r.clk.Now()
		}
		r.waitEvents("fetch.done", n)
		require.EqualValues(t, n, r.failures(id))
		want := 30 * time.Minute << (n - 1)
		if want > 24*time.Hour {
			want = 24 * time.Hour
		}
		require.EqualValues(t, at.Add(want).Unix(), r.next(id), "n=%d", n)
		require.EqualValues(t, want.Seconds(), r.num("SELECT current_delay_s FROM feeds WHERE id = ?", id))
		delay = want
	}
	require.EqualValues(t, 8, r.num("SELECT count(*) FROM fetch_log WHERE outcome='error' AND error_class='http' AND http_status=500 AND trigger='scheduled'"))

	// a success resets the count and returns to the interval; last_error is kept
	fail.Store(false)
	r.clk.Advance(delay)
	r.waitEvents("fetch.done", 9)
	require.Zero(t, r.failures(id))
	require.EqualValues(t, r.clk.Now().Add(30*time.Minute).Unix(), r.next(id))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM feeds WHERE id=? AND last_error = 'HTTP 500'", id))

	// jitter bounds at the scheduler: factor 0.85 .. 1.15 on failure, 0.95 .. 1.05 on success
	r.setJitter(0)
	fail.Store(true)
	r.clk.Advance(31 * time.Minute)
	r.waitEvents("fetch.done", 10)
	require.EqualValues(t, r.clk.Now().Add(1530*time.Second).Unix(), r.next(id), "0.85 x 30 min")
	r.setJitter(0.9999999)
	r.clk.Advance(time.Hour)
	r.waitEvents("fetch.done", 11)
	require.EqualValues(t, r.clk.Now().Add(4140*time.Second).Unix(), r.next(id), "1.15 x 60 min")
}

func TestManualRefreshBypassesBackoffAndJoins(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var hold atomic.Bool
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if hold.Load() {
			select {
			case <-release:
			case <-req.Context().Done():
				return
			}
		}
		serveOK(p, w, req)
	})
	a := r.add(srv.URL+"/a", nil)
	off := r.add(srv.URL+"/off", nil)
	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", off)
	// a feed deep in backoff, nowhere near due
	r.sql("UPDATE feeds SET consecutive_failures = 5, next_fetch_at = ? WHERE id = ?", base.Add(20*time.Hour).Unix(), a)

	hold.Store(true)
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 1, info.Total, "disabled feeds are not part of a run")
	require.False(t, info.Joined)
	waitFor(t, "manual fetch", func() bool { return srv.count("/a") == 1 })
	require.EqualValues(t, 5, r.failures(a), "still failing until the fetch completes")

	again, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.True(t, again.Joined, "a second press joins the active manual run")
	require.Equal(t, info.RunID, again.RunID)

	once.Do(func() { close(release) })
	r.waitEvents("run.done", 1)
	require.Zero(t, r.failures(a), "manual success resets the backoff")
	require.EqualValues(t, r.clk.Now().Add(30*time.Minute).Unix(), r.next(a))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND trigger = 'manual'", a))
	require.Equal(t, 1, srv.count("/a"))
	require.Equal(t, 0, srv.count("/off"))

	hold.Store(false)
	next, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.False(t, next.Joined)
	require.NotEqual(t, info.RunID, next.RunID, "a press after run.done starts a new run")
	r.waitEvents("run.done", 2)
}

func TestManualRunAttachesToInFlightFetches(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	var ids []int64
	for i := 0; i < 3; i++ {
		id := r.add(fmt.Sprintf("%s/f%d", srv.URL, i), nil)
		r.setHost(id, fmt.Sprintf("h%d", i))
		ids = append(ids, id)
	}
	r.add(srv.URL+"/dormant", func(f *store.NewFeed) { f.NextFetchAt = base.Add(10 * time.Hour).Unix() })

	r.s.Wake()
	waitFor(t, "three scheduled fetches in flight", func() bool { return srv.total() == 3 })

	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 4, info.Total, "total = every enabled feed, in-flight ones attached")
	once.Do(func() { close(release) })
	r.waitEvents("run.done", 1)

	// the 3 attached feeds fetched once (not twice); the 4th only by the manual run
	r.waitEvents("fetch.done", 4)
	require.Equal(t, 4, srv.total())
	byFeed := map[string][]any{}
	for _, ev := range r.events("fetch.done") {
		byFeed[ev["feed_id"].(string)] = ev["run_ids"].([]any)
	}
	for _, id := range ids {
		require.Contains(t, byFeed[fmt.Sprint(id)], fmt.Sprint(info.RunID), "run ids are strings")
	}
	start := r.events("run.start")
	require.Len(t, start, 1)
	require.EqualValues(t, 4, start[0]["total"])
	require.Equal(t, fmt.Sprint(info.RunID), start[0]["run_id"], "run.start carries the run id as a string")
	require.Equal(t, fmt.Sprint(info.RunID), r.events("run.done")[0]["run_id"])
}

// A scheduled fetch a manual refresh joins must not report "scheduled" in its fetch.done: the client's pill and
// screen-reader announcement key off the trigger, and "scheduled" means silently skip both, which is wrong for
// new items a person explicitly asked to see (issue found in code review of #56).
func TestManualJoinReportsManualTrigger(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	scheduled := r.add(srv.URL+"/scheduled", nil)
	r.s.Wake()
	waitFor(t, "the scheduled fetch is in flight", func() bool { return srv.total() == 1 })

	_, err := r.s.RefreshAll()
	require.NoError(t, err)
	once.Do(func() { close(release) })
	r.waitEvents("run.done", 1)
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, fmt.Sprint(scheduled), ev["feed_id"])
	require.Equal(t, "manual", ev["trigger"], "a scheduled fetch a manual run joined reports manual, not scheduled")
}

// The single-feed equivalent: a person's own refresh request joining an already-running scheduled fetch for
// that feed must report feed_manual, not scheduled.
func TestSingleFeedJoinReportsFeedManualTrigger(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	scheduled := r.add(srv.URL+"/scheduled", nil)
	r.s.Wake()
	waitFor(t, "the scheduled fetch is in flight", func() bool { return srv.total() == 1 })

	ch, err := r.s.Submit(Priority{FeedID: scheduled})
	require.NoError(t, err)
	once.Do(func() { close(release) })
	<-ch
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "feed_manual", ev["trigger"], "a scheduled fetch a person's own refresh joined reports feed_manual, not scheduled")
}

// Only a person's own refresh counts as manual. An OPML import run or a subscribe-time fetch that joins a scheduled
// fetch already in flight must stay "scheduled", or the new feed's first items would raise the "N new articles" pill
// (found in an independent review of the fix above).
func TestImportAndSubscribeJoinsStayScheduled(t *testing.T) {
	for _, tc := range []struct {
		name string
		join func(r *rig, id int64) error
	}{
		{"import run", func(r *rig, id int64) error { _, err := r.s.StartImport([]int64{id}); return err }},
		{"subscribe fetch", func(r *rig, id int64) error {
			_, err := r.s.Submit(Priority{FeedID: id, Trigger: fetch.TriggerSubscribe})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, Options{})
			release := make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
				select {
				case <-release:
					serveOK(p, w, req)
				case <-req.Context().Done():
				}
			})
			id := r.add(srv.URL+"/scheduled", nil)
			r.s.Wake()
			waitFor(t, "the scheduled fetch is in flight", func() bool { return srv.total() == 1 })
			require.NoError(t, tc.join(r, id))
			once.Do(func() { close(release) })
			r.waitEvents("fetch.done", 1)
			require.Equal(t, "scheduled", r.events("fetch.done")[0]["trigger"])
		})
	}
}

func TestRunKindsDoNotJoinAndEmptyRunFinishes(t *testing.T) {
	r := newRig(t, Options{})
	// total = 0 finishes immediately
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Zero(t, info.Total)
	r.waitEvents("run.done", 1)

	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-release:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	a := r.add(srv.URL+"/a", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })
	imp, err := r.s.StartImport([]int64{a})
	require.NoError(t, err)
	require.Equal(t, RunImport, imp.Kind)
	require.Equal(t, 1, imp.Total)
	waitFor(t, "import fetch", func() bool { return srv.count("/a") == 1 })

	man, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.False(t, man.Joined, "an active import run is never joined")
	require.NotEqual(t, imp.RunID, man.RunID)
	require.Equal(t, 1, man.Total, "the in-flight import fetch is attached to the manual run too")
	once.Do(func() { close(release) })
	r.waitEvents("run.done", 3)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE trigger='import'"))
	require.Equal(t, 1, srv.count("/a"))
}

func TestPerHostCapAndPendingDrain(t *testing.T) {
	r := newRig(t, Options{})
	var cur, peak atomic.Int32
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		n := cur.Add(1)
		for {
			m := peak.Load()
			if n <= m || peak.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		cur.Add(-1)
		serveOK(p, w, req)
	})
	for i := 0; i < 7; i++ {
		r.add(fmt.Sprintf("%s/f%d", srv.URL, i), nil)
	}
	r.s.Wake()
	r.waitEvents("fetch.done", 7)
	require.LessOrEqual(t, int(peak.Load()), 2, "no more than 2 in flight per host")
	require.Equal(t, 2, int(peak.Load()), "and the cap is actually used")
	require.Equal(t, 7, srv.total(), "the pending FIFO drained as host slots freed")
}

func TestRetryAfterFloorHostDeadlineAndSkippedRows(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if p == "/a" {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
			return
		}
		serveOK(p, w, req)
	})
	a := r.add(srv.URL+"/a", nil)
	b := r.add(srv.URL+"/b", func(f *store.NewFeed) { f.NextFetchAt = base.Add(10 * time.Minute).Unix() })

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, base.Add(time.Hour).Unix(), r.next(a), "next = max(now+30m backoff, now+Retry-After)")
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id=? AND note LIKE '%retry_after=3600s%'", a))
	holds := r.s.HostHolds() // what the health view calls "throttled"
	require.Len(t, holds, 1)
	for _, until := range holds {
		require.EqualValues(t, base.Add(time.Hour).Unix(), until.Unix())
	}

	// B is due at 15 min but the host is held until 60 min: it stays due, no request is made
	r.clk.Advance(15 * time.Minute)
	r.barrier()
	require.Zero(t, r.flights())
	require.Equal(t, 0, srv.count("/b"))
	require.EqualValues(t, base.Add(10*time.Minute).Unix(), r.next(b), "nothing is written at selection time")

	// a manual run writes skipped rows and leaves the schedule alone
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 2, info.Total)
	r.waitEvents("run.done", 1)
	require.Equal(t, 0, srv.count("/b"))
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM fetch_log WHERE outcome='skipped' AND note LIKE 'skipped: host retry-after until %'"))
	require.EqualValues(t, base.Add(10*time.Minute).Unix(), r.next(b))
	require.EqualValues(t, base.Add(time.Hour).Unix(), r.next(a))

	// once the deadline passes, B is fetched
	r.clk.Advance(46 * time.Minute)
	waitFor(t, "b fetched after the deadline", func() bool { return srv.count("/b") == 1 })
}

func TestPriorityRepliesSurviveAbandonedHandlers(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	a := r.add(srv.URL+"/a", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })
	b := r.add(srv.URL+"/b", func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() })

	// the first caller gives up and never reads its reply
	_, err := r.s.Submit(Priority{FeedID: a})
	require.NoError(t, err)
	ch, err := r.s.Submit(Priority{FeedID: b, Full: true, Trigger: fetch.TriggerSubscribe})
	require.NoError(t, err)
	select {
	case rep := <-ch:
		require.NoError(t, rep.Err)
		require.Equal(t, fetch.OutcomeOK, rep.Outcome)
		require.Equal(t, 2, rep.NewItems)
	case <-time.After(10 * time.Second):
		t.Fatal("priority reply never arrived")
	}
	r.waitEvents("fetch.done", 2)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id=? AND trigger='subscribe'", b))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id=? AND trigger='feed_manual'", a))

	// the dispatcher keeps ticking and serving afterwards
	r.clk.Advance(10 * time.Hour)
	r.waitEvents("fetch.done", 4)
	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 2, info.Total)
	r.waitEvents("run.done", 1)

	// errors for unknown and disabled feeds
	ch, _ = r.s.Submit(Priority{FeedID: 9999})
	require.ErrorIs(t, (<-ch).Err, ErrNotFound)
	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", a)
	ch, _ = r.s.Submit(Priority{FeedID: a})
	require.ErrorIs(t, (<-ch).Err, ErrDisabled)
	// ...but a trim needs no network and still runs on a disabled feed
	ch, _ = r.s.Submit(Priority{FeedID: a, Kind: PriorityTrim})
	require.NoError(t, (<-ch).Err)
}

func TestGoneDisablesAndStopsFetching(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, func(_ string, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) })
	id := r.add(srv.URL+"/g", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM feeds WHERE id=? AND enabled=0 AND disabled_reason='gone'", id))
	r.waitEvents("feed.changed", 1)
	require.Equal(t, fmt.Sprint(id), r.events("feed.changed")[0]["feed_id"])
	r.clk.Advance(72 * time.Hour)
	r.barrier()
	require.Equal(t, 1, srv.count("/g"))
}

func TestRetentionJobsAndRun(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	inherit := r.add(srv.URL+"/i", nil)
	pinned := r.add(srv.URL+"/p", func(f *store.NewFeed) { z := 0; f.Retention = &z })
	r.setHost(pinned, "other.test")
	r.s.Wake()
	r.waitEvents("fetch.done", 2)
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id=?", inherit))

	r.sql(`INSERT INTO settings(key, value) VALUES ('retention.default', '50')`) // above the item count: no-op run
	// per-feed trim after lowering N
	r.sql("UPDATE feeds SET retention = 50 WHERE id = ?", inherit)
	r.sql(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 60)
	       INSERT INTO items (id, feed_id, uid, url, title, published_at, sort_at, content_hash, text_hash)
	       SELECT 1000+n, ?1, 'x'||n, '', 't', 1, 1, 'c', 't' FROM seq`, inherit)
	ch, err := r.s.Submit(Priority{FeedID: inherit, Kind: PriorityTrim})
	require.NoError(t, err)
	rep := <-ch
	require.NoError(t, rep.Err)
	require.EqualValues(t, 12, rep.Trimmed)
	require.EqualValues(t, 50, r.num("SELECT count(*) FROM items WHERE feed_id=?", inherit))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE outcome='trim_only' AND trigger='retention' AND trimmed_items=12"))

	// a retention run covers only the feeds inheriting the default
	r.sql("UPDATE feeds SET retention = NULL WHERE id = ?", inherit)
	r.sql(`INSERT INTO settings(key, value) VALUES ('retention.default', '20') ON CONFLICT(key) DO UPDATE SET value = '20'`)
	info, err := r.s.ApplyRetention(false)
	require.NoError(t, err)
	require.Equal(t, 1, info.Total)
	r.waitEvents("run.done", 1)
	require.EqualValues(t, 20, r.num("SELECT count(*) FROM items WHERE feed_id=?", inherit))
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id=?", pinned), "an explicit unlimited feed is untouched")

	info, err = r.s.ApplyRetention(true)
	require.NoError(t, err)
	require.Equal(t, 2, info.Total)
}

func TestShutdownDuringLargeRun(t *testing.T) {
	r := newRig(t, Options{PerHost: 8})
	var completed atomic.Int32
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(p, "/stall") {
			<-req.Context().Done() // aborted by shutdown
			return
		}
		time.Sleep(40 * time.Millisecond)
		serveOK(p, w, req)
		completed.Add(1)
	})
	for i := 0; i < 138; i++ {
		id := r.add(fmt.Sprintf("%s/f%d", srv.URL, i), nil)
		r.setHost(id, fmt.Sprintf("h%d", i%40))
	}
	stall := r.add(srv.URL+"/stall", nil)
	r.setHost(stall, "stall.test")

	info, err := r.s.RefreshAll()
	require.NoError(t, err)
	require.Equal(t, 139, info.Total)
	waitFor(t, "some fetches committed", func() bool { return len(r.events("fetch.done")) >= 20 })
	waitFor(t, "the stall fetch in flight", func() bool { return srv.count("/stall") == 1 })

	// a waiting handler returns at once when Stop is called
	waiting := make(chan error, 1)
	go func() { _, err := r.s.RefreshAll(); waiting <- err }()

	begin := time.Now()
	r.s.Stop()
	select {
	case <-r.s.Shutdown():
	default:
		t.Fatal("Shutdown() must be closed as soon as Stop returns")
	}
	select {
	case <-r.s.Stopped():
	case <-time.After(15 * time.Second):
		t.Fatal("scheduler did not stop within 15 s")
	}
	require.Less(t, time.Since(begin), 15*time.Second)

	_, err = r.s.RefreshAll()
	require.ErrorIs(t, err, ErrStopped)
	_, err = r.s.Submit(Priority{FeedID: 1})
	require.ErrorIs(t, err, ErrStopped)
	select {
	case err := <-waiting:
		if err != nil {
			require.ErrorIs(t, err, ErrStopped)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a handler waiting on the scheduler did not return")
	}

	// every fetch that completed was committed with its fetch_log row; the aborted one wrote nothing
	rows := r.num("SELECT count(*) FROM fetch_log WHERE outcome IN ('ok','unchanged','not_modified','error')")
	countDone := func() (n int) { // the rig's collector goroutine may lag the hub briefly
		for _, ev := range r.events("fetch.done") {
			if ev["outcome"] != "" {
				n++
			}
		}
		return n
	}
	waitFor(t, "every published result collected", func() bool { return int64(countDone()) >= rows })
	done := countDone()
	require.EqualValues(t, done, r.num("SELECT count(*) FROM fetch_log WHERE outcome IN ('ok','unchanged','not_modified','error')"),
		"one committed row per reported result")
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM fetch_log WHERE feed_id = ?", stall), "an aborted fetch writes nothing")
	require.EqualValues(t, 0, r.num("SELECT consecutive_failures FROM feeds WHERE id = ?", stall))
	require.GreaterOrEqual(t, int(completed.Load()), done-1)
}

func TestCommitGateKeepsAPIWritesResponsive(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	r := newRig(t, Options{PerHost: 8})
	srv := newSrv(t, serveOK)
	for i := 0; i < 138; i++ {
		id := r.add(fmt.Sprintf("%s/f%d", srv.URL, i), nil)
		r.setHost(id, fmt.Sprintf("h%d", i%40))
	}
	// items the "API" will flip while the run commits
	r.sql(`INSERT INTO feeds (url, url_key, host, enabled, disabled_reason, retention) VALUES ('kipple:archive','kipple:archive','',0,'archive',0)`)
	r.sql(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 50)
	       INSERT INTO items (id, feed_id, uid, url, title, published_at, sort_at, content_hash, text_hash)
	       SELECT 5000+n, (SELECT id FROM feeds WHERE disabled_reason='archive'), 'u'||n, '', 't', 1, 1, 'c', 't' FROM seq`)

	_, err := r.s.RefreshAll()
	require.NoError(t, err)
	var lat []time.Duration
	for i := 0; len(r.events("run.done")) == 0 && i < 20000; i++ {
		start := time.Now()
		require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sqlTx) error {
			_, err := tx.ExecContext(ctx, "UPDATE items SET read = 1 - read WHERE id = ?", 5001+i%50)
			return err
		}))
		lat = append(lat, time.Since(start))
	}
	require.NotEmpty(t, lat)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99 := lat[len(lat)*99/100]
	require.LessOrEqual(t, p99, 250*time.Millisecond, "API write p99 during a 138-feed run (%d writes)", len(lat))
	r.waitEvents("run.done", 1)
	require.EqualValues(t, 138*2, r.num("SELECT count(*) FROM items WHERE feed_id IN (SELECT id FROM feeds WHERE url LIKE 'http%')"))
}

func TestSlowFetchStillCommits(t *testing.T) {
	// the commit deadline must start after the fetch, not before it
	r := newRig(t, Options{CommitTimeout: 150 * time.Millisecond})
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		time.Sleep(400 * time.Millisecond) // longer than the commit timeout
		serveOK(p, w, req)
	})
	id := r.add(srv.URL+"/slow", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id=?", id))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id=? AND outcome='ok'", id))
}

func TestOverlappingImportRunsKeepTheNewerRegistered(t *testing.T) {
	r := newRig(t, Options{})
	rel := map[string]chan struct{}{"/a": make(chan struct{}), "/b": make(chan struct{})}
	var onceA, onceB sync.Once
	release := func(p string) {
		if p == "/a" {
			onceA.Do(func() { close(rel["/a"]) })
		} else {
			onceB.Do(func() { close(rel["/b"]) })
		}
	}
	t.Cleanup(func() { release("/a"); release("/b") })
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		select {
		case <-rel[p]:
			serveOK(p, w, req)
		case <-req.Context().Done():
		}
	})
	far := func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() }
	a := r.add(srv.URL+"/a", far)
	b := r.add(srv.URL+"/b", far)
	r.setHost(b, "other.test")

	first, err := r.s.StartImport([]int64{a})
	require.NoError(t, err)
	waitFor(t, "a in flight", func() bool { return srv.count("/a") == 1 })
	second, err := r.s.StartImport([]int64{b})
	require.NoError(t, err)
	require.NotEqual(t, first.RunID, second.RunID)
	waitFor(t, "b in flight", func() bool { return srv.count("/b") == 1 })

	release("/a") // the older run finishes first
	r.waitEvents("run.done", 1)
	var live int64
	r.s.inDispatcher(func() {
		for id := range r.s.runs {
			live = id
		}
		require.Len(t, r.s.runs, 1)
	})
	require.Equal(t, second.RunID, live, "finishing the older run must not unregister the newer one")

	release("/b")
	r.waitEvents("run.done", 2)
	r.s.inDispatcher(func() { require.Empty(t, r.s.runs) })
}

func TestPriorityOnHeldHost(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		if p == "/a" {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
			return
		}
		serveOK(p, w, req)
	})
	far := func(f *store.NewFeed) { f.NextFetchAt = base.Add(9 * time.Hour).Unix() }
	r.add(srv.URL+"/a", nil)
	b := r.add(srv.URL+"/b", far)
	c := r.add(srv.URL+"/c", far)
	d := r.add(srv.URL+"/d", far)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)

	reply := func(p Priority) Reply {
		ch, err := r.s.Submit(p)
		require.NoError(t, err)
		select {
		case rep := <-ch:
			return rep
		case <-time.After(10 * time.Second):
			t.Fatal("no reply")
			return Reply{}
		}
	}
	rep := reply(Priority{FeedID: b})
	require.Equal(t, fetch.OutcomeSkipped, rep.Outcome, "a plain refresh skips on a held host, idle queue or not")
	require.Zero(t, srv.count("/b"))

	rep = reply(Priority{FeedID: c, Full: true})
	require.Equal(t, fetch.OutcomeOK, rep.Outcome, "a full refresh is not skipped")
	rep = reply(Priority{FeedID: d, Trigger: fetch.TriggerSubscribe})
	require.Equal(t, fetch.OutcomeOK, rep.Outcome, "subscribe is not skipped")
	require.Equal(t, 1, srv.count("/c"))
	require.Equal(t, 1, srv.count("/d"))
}

func TestCommitFailureBacksOffInMemory(t *testing.T) {
	r := newRig(t, Options{})
	var fail atomic.Bool
	fail.Store(true)
	r.s.inDispatcher(func() {
		r.s.failCommit = func(int64) error {
			if fail.Load() {
				return fmt.Errorf("injected commit failure")
			}
			return nil
		}
	})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "error", r.events("fetch.done")[0]["outcome"])
	require.Equal(t, 1, srv.count("/f"))
	var nb time.Time
	r.s.inDispatcher(func() { nb = r.s.notBefore[id] })
	require.True(t, nb.After(r.clk.Now()), "backoff recorded")

	// the row is still due (nothing was written), but the next tick leaves it alone
	r.clk.Advance(time.Minute)
	r.s.Wake()
	r.barrier()
	require.Zero(t, r.flights())
	require.Equal(t, 1, srv.count("/f"), "not redispatched before notBefore")

	fail.Store(false)
	r.clk.Set(nb.Add(time.Second))
	r.s.Wake()
	r.waitEvents("fetch.done", 2)
	require.Equal(t, 2, srv.count("/f"))
	r.s.inDispatcher(func() { require.Empty(t, r.s.notBefore, "cleared by the next successful commit") })
}

func TestPartialChunkedCommitStillReportsCommittedItems(t *testing.T) {
	r := newRig(t, Options{})
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
	for i := 0; i < 620; i++ {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>t%d</title><link>https://ex.com/%d</link><pubDate>%s</pubDate></item>`,
			i, i, i, base.Add(-time.Duration(620-i)*time.Minute).Format(time.RFC1123Z))
	}
	b.WriteString(`</channel></rss>`)
	srv := newSrv(t, func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(b.String()))
	})
	id := r.add(srv.URL+"/big", nil)
	r.sql("UPDATE feeds SET retention = 0 WHERE id = ?", id)
	// oldest first: chunk 1 = g0..g249, chunk 2 aborts at g300
	r.sql(fmt.Sprintf(`CREATE TRIGGER boom BEFORE INSERT ON items WHEN NEW.uid = 'g:%s' BEGIN SELECT RAISE(ABORT, 'boom'); END`, fetch.H("g300")))

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	ev := r.events("fetch.done")[0]
	require.Equal(t, "error", ev["outcome"], "the fetch did not fully commit")
	require.EqualValues(t, 250, ev["new_items"], "but chunk 1's items are reported")
	ids, _ := ev["new_item_ids"].([]any)
	require.Len(t, ids, maxEventIDs)
	require.IsType(t, "", ids[0], "item ids are strings")
	require.EqualValues(t, 250, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
}

func TestStatusNeverBlocksShutdown(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), store.Options{Path: t.TempDir() + "/kipple.db", Logger: quiet})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// A scheduler whose dispatcher never runs cannot answer. With the old
	// unconditional wait this Status call would hang forever and hold up
	// srv.Shutdown; now Stop releases it at once.
	s := New(db, fetch.NewClient(fetch.ClientOptions{}), events.New(), nil, quiet, Options{})
	done := make(chan struct{})
	var runs []RunStatus
	go func() { defer close(done); runs, _ = s.Status() }()
	select {
	case <-done:
		t.Fatal("Status returned before Stop with no dispatcher; it should wait")
	case <-time.After(50 * time.Millisecond):
	}
	s.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Status still blocked after Stop")
	}
	require.NotNil(t, runs)
	require.Empty(t, runs)

	// and it gives up by itself if the dispatcher is wedged
	old := statusWait
	statusWait = 30 * time.Millisecond
	t.Cleanup(func() { statusWait = old })
	s2 := New(db, fetch.NewClient(fetch.ClientOptions{}), events.New(), nil, quiet, Options{})
	start := time.Now()
	runs, inflight := s2.Status()
	require.Less(t, time.Since(start), 2*time.Second)
	require.Empty(t, runs)
	require.Zero(t, inflight)
}

// A per-feed request that arrives while the feed is already in flight used to
// borrow the running job's reply and lose its intent. A Full refresh must run
// as its own fetch (validators dropped) after the in-flight one, and a trim
// request must run a trim.
func TestPriorityIntentSurvivesInFlightFeed(t *testing.T) {
	r := newRig(t, Options{})
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var mu sync.Mutex
	var inms []string
	srv := newSrv(t, func(_ string, w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		inms = append(inms, req.Header.Get("If-None-Match"))
		first := len(inms) == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			<-release
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedXML))
	})
	id := r.add(srv.URL+"/f", nil)
	// seed a validator so a non-full fetch would send If-None-Match
	r.sql("UPDATE feeds SET etag = '\"v1\"' WHERE id = ?", id)
	r.s.Wake()
	<-started // the scheduled fetch is now in flight

	full, err := r.s.Submit(Priority{FeedID: id, Full: true})
	require.NoError(t, err)
	trim, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	r.barrier()
	close(release)

	for name, ch := range map[string]<-chan Reply{"full": full, "trim": trim} {
		select {
		case rep := <-ch:
			require.NoError(t, rep.Err, name)
			if name == "trim" {
				require.Equal(t, fetch.OutcomeTrimOnly, rep.Outcome)
			} else {
				require.NotEqual(t, fetch.OutcomeNotModified, rep.Outcome)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s reply never arrived", name)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, inms, 2, "the full refresh must run as its own fetch")
	require.Equal(t, `"v1"`, inms[0])
	require.Empty(t, inms[1], "a full refetch drops validators")
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM fetch_log WHERE feed_id=? AND outcome='trim_only'", id))
}

// A trim needs no network, so a disabled feed still honors a lowered cap.
func TestTrimRunsOnDisabledFeed(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/d", nil)
	r.sql("UPDATE feeds SET enabled = 0, disabled_reason = 'user', retention = 50 WHERE id = ?", id)
	r.sql(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 60)
	       INSERT INTO items (id, feed_id, uid, url, title, published_at, sort_at, content_hash, text_hash)
	       SELECT 1000+n, ?1, 'x'||n, '', 't', 1, 1, 'c', 't' FROM seq`, id)
	ch, err := r.s.Submit(Priority{FeedID: id, Kind: PriorityTrim})
	require.NoError(t, err)
	rep := <-ch
	require.NoError(t, rep.Err)
	require.EqualValues(t, 10, rep.Trimmed)
	require.EqualValues(t, 50, r.num("SELECT count(*) FROM items WHERE feed_id=?", id))
	require.Zero(t, srv.total(), "a trim never touches the network")

	// while a fetch-type job on the same disabled feed is still refused
	ch, _ = r.s.Submit(Priority{FeedID: id, Full: true})
	require.ErrorIs(t, (<-ch).Err, ErrDisabled)
}

// uaSrv refuses any User-Agent without "Chrome" with 403 and records every UA.
func uaSrv(t *testing.T) (*feedSrv, func() []string) {
	var mu sync.Mutex
	var uas []string
	srv := newSrv(t, func(_ string, w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		uas = append(uas, req.UserAgent())
		mu.Unlock()
		if !strings.Contains(req.UserAgent(), "Chrome") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveOK("", w, req)
	})
	return srv, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), uas...) }
}

func TestBrowserUARetryIsRememberedPerFeed(t *testing.T) {
	r := newRig(t, Options{})
	srv, uas := uaSrv(t)
	id := r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Len(t, uas(), 2, "Kipple UA refused, then one browser retry")
	require.Contains(t, uas()[0], "Kipple")
	require.Contains(t, uas()[1], "Chrome")
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
	require.EqualValues(t, 1, r.num("SELECT ua_fallback FROM feeds WHERE id = ?", id))

	r.clk.Advance(31 * time.Minute)
	r.waitEvents("fetch.done", 2)
	require.Len(t, uas(), 3, "the remembered feed goes straight to the browser UA")
	require.Contains(t, uas()[2], "Chrome")
}

func TestUAModeDefaultNeverRetries(t *testing.T) {
	r := newRig(t, Options{})
	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"fetch.user_agent_mode": "default"}))
	srv, uas := uaSrv(t)
	id := r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Len(t, uas(), 1)
	require.Equal(t, "error", r.events("fetch.done")[0]["outcome"])
	require.Zero(t, r.num("SELECT ua_fallback FROM feeds WHERE id = ?", id))
}

// A fetch whose feed URL is edited while the browser-UA retry is in flight is
// stale: nothing is committed, so the browser UA must not be learned for the
// new URL either.
func TestStaleFetchDoesNotLearnBrowserUA(t *testing.T) {
	r := newRig(t, Options{})
	var id atomic.Int64
	var srv *feedSrv
	var once sync.Once
	srv = newSrv(t, func(path string, w http.ResponseWriter, req *http.Request) {
		if path != "/f" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.Contains(req.UserAgent(), "Chrome") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		once.Do(func() {
			nu := srv.URL + "/other"
			_, err := r.db.PatchFeed(context.Background(), id.Load(), store.FeedPatch{URL: &nu, Cols: map[string]any{}})
			require.NoError(t, err)
		})
		serveOK("", w, req)
	})
	id.Store(r.add(srv.URL+"/f", nil))
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Zero(t, r.num("SELECT ua_fallback FROM feeds WHERE id = ?", id.Load()))
}

func TestCommitFailureBackoffEscalatesAndCaps(t *testing.T) {
	r := newRig(t, Options{})
	r.s.inDispatcher(func() { r.s.failCommit = func(int64) error { return fmt.Errorf("injected commit failure") } })
	srv := newSrv(t, serveOK)
	id := r.add(srv.URL+"/f", nil)

	var delays []time.Duration
	for i := 1; i <= 10; i++ {
		r.s.Wake()
		r.waitEvents("fetch.done", i)
		var nb time.Time
		r.s.inDispatcher(func() { nb = r.s.notBefore[id] })
		delays = append(delays, nb.Sub(r.clk.Now()))
		r.clk.Set(nb.Add(time.Second))
	}
	for i := 1; i < 6; i++ {
		require.Greater(t, delays[i], delays[i-1], "delay %d grows", i)
	}
	require.InDelta(t, (24 * time.Hour).Seconds(), delays[9].Seconds(), 24*3600*0.16, "capped near 24 h")
	require.LessOrEqual(t, delays[9], time.Duration(float64(24*time.Hour)*1.16))
	require.InDelta(t, delays[8].Seconds(), delays[9].Seconds(), 24*3600*0.32, "stays at the cap")
}

func TestCommitFetchGetsNoDeadlineAndPerChunkBudget(t *testing.T) {
	r := newRig(t, Options{CommitTimeout: 7 * time.Second})
	type seen struct {
		deadline bool
		perChunk time.Duration
	}
	got := make(chan seen, 4)
	r.s.inDispatcher(func() {
		r.s.commitFetchFn = func(ctx context.Context, _ *fetch.Result, perChunk time.Duration) (store.CommitInfo, error) {
			_, has := ctx.Deadline()
			got <- seen{has, perChunk}
			return store.CommitInfo{}, nil
		}
	})
	srv := newSrv(t, serveOK)
	r.add(srv.URL+"/f", nil)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	s := <-got
	require.False(t, s.deadline, "chunks must not share a deadline on ctx")
	require.Equal(t, 7*time.Second, s.perChunk)
}
