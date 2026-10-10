package sched

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
)

// ftFeed builds an RSS document with items whose links are base+"/a/<i>"; a
// higher i is newer.
func ftFeed(base string, idx ...int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
	for _, i := range idx {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>Item %d</title><link>%s/a/%d</link><pubDate>%s</pubDate></item>`,
			i, i, base, i, time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC).Format(time.RFC1123Z))
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

const ftPara = "The quick brown fox jumps over the lazy dog while the reader keeps going through a long and detailed paragraph of article text. "

func ftArticle() string {
	return `<!DOCTYPE html><html><head><title>Story</title><meta charset="utf-8"></head><body><nav><a href="/">Home</a></nav>
<article><h1>Story</h1>` + strings.Repeat("<p>"+strings.Repeat(ftPara, 3)+"</p>", 6) + `</article><footer>junk</footer></body></html>`
}

// ftServer serves a feed at /f (whatever body currently holds) and articles at
// /a/<n>; status maps an article number to the HTTP status it answers.
type ftServer struct {
	*feedSrv
	body atomic.Value
}

func newFTServer(t *testing.T, status map[string]int) *ftServer {
	t.Helper()
	ts := &ftServer{}
	ts.feedSrv = newSrv(t, func(path string, w http.ResponseWriter, _ *http.Request) {
		if path == "/f" {
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(ts.body.Load().(string)))
			return
		}
		for n, code := range status {
			if path == "/a/"+n {
				w.WriteHeader(code)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(ftArticle()))
	})
	return ts
}

func (r *rig) ftFeed(url string) int64 {
	r.t.Helper()
	id := r.add(url, nil)
	r.sql("UPDATE feeds SET fulltext = 1 WHERE id = ?", id)
	return id
}

func (r *rig) ftRows(feedID int64) (ok, failed int64) {
	ok = r.num(`SELECT count(*) FROM item_fulltext ft JOIN items i ON i.id = ft.item_id WHERE i.feed_id = ? AND ft.content_html IS NOT NULL`, feedID)
	failed = r.num(`SELECT count(*) FROM item_fulltext ft JOIN items i ON i.id = ft.item_id WHERE i.feed_id = ? AND ft.error IS NOT NULL`, feedID)
	return ok, failed
}

// waitRows waits until the feed has ok extractions and failed failures.
func (r *rig) waitRows(feedID, ok, failed int64) {
	r.t.Helper()
	waitFor(r.t, fmt.Sprintf("%d ok and %d failed extractions", ok, failed), func() bool {
		o, f := r.ftRows(feedID)
		return o == ok && f == failed
	})
}

func (r *rig) lastNote(feedID int64) string {
	r.t.Helper()
	var n *string
	require.NoError(r.t, r.db.Reader().QueryRow(`SELECT note FROM fetch_log WHERE feed_id = ? AND outcome = 'ok' ORDER BY id DESC LIMIT 1`, feedID).Scan(&n))
	if n == nil {
		return ""
	}
	return *n
}

func TestExtractsNewItemsAfterCommitOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed(srv.URL, 1, 2))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Contains(t, r.lastNote(id), "fulltext_picked: 2")
	r.waitRows(id, 2, 0)
	require.Equal(t, 1, srv.count("/a/1"))
	require.Equal(t, 1, srv.count("/a/2"))
	require.Positive(t, r.num("SELECT word_count FROM item_fulltext ORDER BY item_id LIMIT 1"))

	// The ready event names the finished items, ids as strings.
	waitFor(t, "fulltext.ready", func() bool { return len(r.events("fulltext.ready")) >= 1 })
	got := map[string]bool{}
	for _, ev := range r.events("fulltext.ready") {
		for _, v := range ev["ids"].([]any) {
			got[v.(string)] = true
		}
	}
	require.Len(t, got, 2)
	for k := range got {
		_, err := strconv.ParseInt(k, 10, 64)
		require.NoError(t, err)
	}

	// A later fetch with one more item extracts only the new one.
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	r.advanceTo(r.next(id))
	r.waitEvents("fetch.done", 2)
	r.waitRows(id, 3, 0)
	require.Equal(t, 1, srv.count("/a/1"), "no re-extract of an item that already has its text")
	require.Equal(t, 1, srv.count("/a/2"))
	require.Equal(t, 1, srv.count("/a/3"))
}

func TestFailuresAreClassifiedIsolatedAndNotRetriedByPolling(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newFTServer(t, map[string]int{"1": 500, "2": 404})
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"], "an extraction failure never fails the fetch")
	r.waitRows(id, 1, 2)
	require.Zero(t, r.failures(id))
	var c500, c404 string
	require.NoError(t, r.db.Reader().QueryRow("SELECT error_class FROM item_fulltext WHERE error LIKE '%500'").Scan(&c500))
	require.NoError(t, r.db.Reader().QueryRow("SELECT error_class FROM item_fulltext WHERE error LIKE '%404'").Scan(&c404))
	require.Equal(t, "transient", c500)
	require.Equal(t, "permanent", c404)

	srv.body.Store(ftFeed(srv.URL, 1, 2, 3, 4))
	r.advanceTo(r.next(id))
	r.waitEvents("fetch.done", 2)
	r.waitRows(id, 2, 2)
	require.Equal(t, 1, srv.count("/a/1"), "a failed item is not retried by polling")
}

// fakeExt records calls and how many ran at once, per host and overall. It
// holds each call for hold, or until gate is closed, or until ctx ends.
type fakeExt struct {
	mu      sync.Mutex
	calls   []string
	cur     int
	max     int
	perHost map[string]int
	maxHost int
	hold    time.Duration
	block   bool          // wait for ctx to end
	panics  string        // when set, Extract panics with it for URLs containing it
	gate    chan struct{} // when set, wait for it to close

	// rendezvous, when > 0, makes each call wait (up to ftRendezvousTimeout) until
	// that many calls are in flight at once, so a test can assert a concurrency
	// limit is reached without depending on sleep timing. Once reached, later
	// calls do not wait.
	rendezvous int
	reached    chan struct{}
}

const ftRendezvousTimeout = 10 * time.Second

func (f *fakeExt) Extract(ctx context.Context, t extract.Target) (extract.Result, error) {
	host := strings.SplitN(strings.TrimPrefix(t.URL, "https://"), "/", 2)[0]
	f.mu.Lock()
	f.calls = append(f.calls, t.URL)
	f.cur++
	f.max = max(f.max, f.cur)
	if f.perHost == nil {
		f.perHost = map[string]int{}
	}
	f.perHost[host]++
	f.maxHost = max(f.maxHost, f.perHost[host])
	if f.rendezvous > 0 {
		if f.reached == nil {
			f.reached = make(chan struct{})
		}
		if f.cur >= f.rendezvous {
			select {
			case <-f.reached:
			default:
				close(f.reached)
			}
		}
	}
	reached := f.reached
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.cur--
		f.perHost[host]--
		f.mu.Unlock()
	}()
	if f.panics != "" && strings.Contains(t.URL, f.panics) {
		panic("hostile page")
	}
	if reached != nil {
		select {
		case <-reached:
		case <-ctx.Done():
			return extract.Result{}, ctx.Err()
		case <-time.After(ftRendezvousTimeout):
			return extract.Result{}, errors.New("rendezvous never reached")
		}
	}
	if f.block {
		<-ctx.Done()
		return extract.Result{}, errors.New("cut off")
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return extract.Result{}, ctx.Err()
		}
	}
	select {
	case <-time.After(f.hold):
	case <-ctx.Done():
		return extract.Result{}, ctx.Err()
	}
	return extract.Result{HTML: "<p>text</p>", Text: "text", WordCount: 1, SourceURL: t.URL}, nil
}

func (f *fakeExt) snapshot() (calls []string, maxAll, maxHost int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), f.max, f.maxHost
}

func (f *fakeExt) waitCalls(t *testing.T, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d extractor calls", n), func() bool {
		c, _, _ := f.snapshot()
		return len(c) >= n
	})
}

// Not parallel: it measures wall time.
func TestFetchDoesNotWaitForSlowArticleHosts(t *testing.T) {
	fx := &fakeExt{block: true} // every article host hangs until cancelled
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://slow.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")

	start := time.Now()
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Contains(t, r.lastNote(id), "fulltext_picked: 5")
	fx.waitCalls(t, 2) // extraction is under way, and the worker is already free
	require.Zero(t, r.flights(), "the feed's worker is not tied up by extraction")
	ok, failed := r.ftRows(id)
	require.Zero(t, ok+failed, "until extraction finishes there is no row, so the Reader API serves feed content")
}

func TestQueuedItemsGetTextLater(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	fx := &fakeExt{gate: gate}
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	fx.waitCalls(t, 2)
	ok, failed := r.ftRows(id)
	require.Zero(t, ok+failed)
	close(gate)
	r.waitRows(id, 2, 0)
	waitFor(t, "fulltext.ready", func() bool { return len(r.events("fulltext.ready")) >= 1 })
}

func TestCapDefersTheRestNewestFirst(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx, FulltextMaxItems: 2})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	r.waitRows(id, 2, 0)
	calls, _, _ := fx.snapshot()
	require.ElementsMatch(t, []string{"https://art.test/a/5", "https://art.test/a/4"}, calls, "the two newest")
	note := r.lastNote(id)
	require.Contains(t, note, "fulltext_picked: 2")
	require.Contains(t, note, "fulltext_deferred: 3")
}

func TestQueueBoundDefersWithANote(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{block: true}
	r := newRig(t, Options{Extractor: fx, FulltextQueue: 2, FulltextGlobal: 1})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	note := r.lastNote(id)
	require.Contains(t, note, "fulltext_picked: 2")
	require.Contains(t, note, "fulltext_deferred: 3", "items beyond the queue bound are left to on-demand, and say so")
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
}

func TestPoolConcurrencyAndPerHostLimits(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{rendezvous: 2, hold: 20 * time.Millisecond}
	r := newRig(t, Options{Extractor: fx, FulltextPerHost: 2, FulltextGlobal: 4})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://one.test", 1, 2, 3, 4, 5, 6))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	r.waitRows(id, 6, 0)
	_, _, maxHost := fx.snapshot()
	require.Equal(t, 2, maxHost, "at most 2 at once against one article host, and the limit is used")

	// Spread over hosts, the global pool size is the bound.
	fx2 := &fakeExt{rendezvous: 3, hold: 20 * time.Millisecond}
	r2 := newRig(t, Options{Extractor: fx2, FulltextPerHost: 2, FulltextGlobal: 3})
	srv2 := newFTServer(t, nil)
	srv2.body.Store(spreadFeed(0, 6))
	id2 := r2.ftFeed(srv2.URL + "/f")
	r2.s.Wake()
	r2.waitEvents("fetch.done", 1)
	r2.waitRows(id2, 6, 0)
	_, maxAll, _ := fx2.snapshot()
	require.Equal(t, 3, maxAll)
}

// spreadFeed has n items, each on its own article host.
func spreadFeed(feed, n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
	for j := 1; j <= n; j++ {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>I%d</title><link>https://f%dh%d.test/a</link></item>`, j, j, feed, j)
	}
	b.WriteString(`</channel></rss>`)
	return b.String()
}

func TestGlobalCapAcrossFeeds(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{hold: 150 * time.Millisecond}
	r := newRig(t, Options{Extractor: fx, Workers: 6, FulltextPerHost: 3, FulltextGlobal: 2})
	const feeds = 5
	var ids []int64
	for i := 0; i < feeds; i++ {
		srv := newFTServer(t, nil)
		srv.body.Store(spreadFeed(i, 3))
		ids = append(ids, r.ftFeed(srv.URL+"/f"))
	}
	r.s.Wake()
	r.waitEvents("fetch.done", feeds)
	for _, id := range ids {
		r.waitRows(id, 3, 0)
	}
	calls, maxAll, _ := fx.snapshot()
	require.Len(t, calls, feeds*3)
	require.Equal(t, 2, maxAll, "global cap holds across feeds and is used")
}

func TestOffDoesNothing(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2))
	id := r.add(srv.URL+"/f", nil) // fulltext = 0
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	calls, _, _ := fx.snapshot()
	require.Empty(t, calls)
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext"))
	require.NotContains(t, r.lastNote(id), "fulltext")
}

// A queued item is skipped when the feed, the item or its URL changes before
// its turn comes.
func TestQueuedWorkSurvivesEditsAndDeletes(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	fx := &fakeExt{gate: gate}
	r := newRig(t, Options{Extractor: fx, FulltextGlobal: 1})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	fx.waitCalls(t, 1) // item 4 (newest) is running; 3, 2, 1 wait

	itemID := func(n int) int64 {
		return r.num("SELECT id FROM items WHERE feed_id = ? AND url = ?", id, fmt.Sprintf("https://art.test/a/%d", n))
	}
	i4, i3, i2, i1 := itemID(4), itemID(3), itemID(2), itemID(1)
	r.sql("DELETE FROM items WHERE id = ?", i3)                                 // item gone
	r.sql("UPDATE items SET url = 'https://elsewhere.test/x' WHERE id = ?", i2) // URL changed
	r.sql("UPDATE feeds SET fulltext = 0 WHERE id = ?", id)                     // no longer a full-text feed
	r.sql("UPDATE items SET fulltext_mode = 1 WHERE id = ?", i1)                // ...except this item, forced on
	close(gate)
	waitFor(t, "the surviving work", func() bool {
		ok, _ := r.ftRows(id)
		return ok >= 1
	})
	// item 4 was running when the feed's setting flipped off, so its result is
	// dropped at the write; item 1 is forced on and stored; 3 is gone; 2 changed
	// URL.
	require.Eventually(t, func() bool { return r.num("SELECT count(*) FROM item_fulltext WHERE item_id = ?", i4) == 0 }, time.Second, 20*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext WHERE item_id = ?", i4), "full text was turned off while it ran")
	calls, _, _ := fx.snapshot()
	require.NotContains(t, calls, "https://art.test/a/3")
	require.NotContains(t, calls, "https://art.test/a/2")
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext WHERE item_id = ?", i2))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM item_fulltext WHERE item_id = ?", i1))
}

func TestResultForChangedURLIsDropped(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	fx := &fakeExt{gate: gate}
	r := newRig(t, Options{Extractor: fx, FulltextGlobal: 1})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	fx.waitCalls(t, 1)
	r.sql("UPDATE items SET url = 'https://art.test/moved' WHERE feed_id = ?", id)
	close(gate)
	time.Sleep(300 * time.Millisecond) // let the extraction finish and try to save
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext"), "the page no longer belongs to the item")
	require.Empty(t, r.events("fulltext.ready"))
}

func TestStopCancelsExtractionCleanly(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{block: true}
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5, 6))
	r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	fx.waitCalls(t, 2) // two run (the per-host limit), four wait in the queue
	r.s.Stop()
	select {
	case <-r.s.Stopped():
	case <-time.After(10 * time.Second):
		t.Fatal("scheduler did not stop with extractions in flight")
	}
	done := make(chan struct{})
	go func() { r.s.ftWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("extraction pool goroutines still running after Stopped")
	}
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext"), "a cut-off extraction is not recorded as a failure")
}

func TestQueueDedupesBoundsAndSpreadsHosts(t *testing.T) {
	t.Parallel()
	q := newFTQueue(3, 1)
	a1 := ftJob{itemID: 1, url: "https://a/1", host: "a"}
	require.Equal(t, pushQueued, q.push(a1))
	require.Equal(t, pushDup, q.push(a1), "an item already queued is not queued twice")
	q.push(ftJob{itemID: 2, url: "https://a/2", host: "a"})
	q.push(ftJob{itemID: 3, url: "https://b/3", host: "b"})
	require.Equal(t, pushFull, q.push(ftJob{itemID: 4, url: "https://b/4", host: "b"}), "beyond the bound nothing is queued")
	require.Zero(t, q.free(0))

	j1, done1, ok := q.take()
	require.True(t, ok)
	require.EqualValues(t, 1, j1.itemID)
	j2, done2, ok := q.take()
	require.True(t, ok)
	require.EqualValues(t, 3, j2.itemID, "host a is at its limit, so the next job is for host b")
	require.Equal(t, pushDup, q.push(a1), "a running item is still deduped")
	done1()
	done2()

	q.close()
	_, _, ok = q.take()
	require.False(t, ok)
	require.Equal(t, pushClosed, q.push(ftJob{itemID: 9, url: "https://c/9", host: "c"}), "a shut queue reports closed, not full")
}

// A parser panic on a hostile page must not take the process down: it is stored
// as a permanent failure for that item and the pool carries on.
func TestPanickingExtractionIsStoredAsPermanentErrorAndPoolSurvives(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{panics: "/a/2"}
	r := newRig(t, Options{Extractor: fx, FulltextGlobal: 1})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	r.waitRows(id, 2, 1)
	require.EqualValues(t, 1, r.num(`SELECT count(*) FROM item_fulltext ft JOIN items i ON i.id = ft.item_id
		WHERE i.feed_id = ? AND i.url = 'https://art.test/a/2' AND ft.error IS NOT NULL AND ft.error_class = 'permanent'`, id))
}

// A document that repeats an item (same link, different guids, link dedup) is
// collapsed to its first occurrence before the pick and the commit alike, so
// the URL extracted is the URL stored and it is fetched once.
func TestDuplicateItemsInADocumentExtractTheStoredURLOnce(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t, nil)
	srv.body.Store(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>` +
		`<item><guid>a</guid><title>First</title><link>https://art.test/same</link></item>` +
		`<item><guid>b</guid><title>Second</title><link>https://art.test/same</link></item></channel></rss>`)
	id := r.ftFeed(srv.URL + "/f")
	r.sql("UPDATE feeds SET dedup_mode = 'link' WHERE id = ?", id)
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	r.waitRows(id, 1, 0)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	calls, _, _ := fx.snapshot()
	require.Equal(t, []string{"https://art.test/same"}, calls)
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM items WHERE feed_id = ? AND url = 'https://art.test/same'", id))
}

// fetch.fulltext_all extracts the new items of a feed whose own flag is off,
// takes effect on the next fetch without a restart, and never backfills.
func TestFulltextAllSwitchAppliesToNewItemsOnly(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed(srv.URL, 1, 2))
	id := r.add(srv.URL+"/f", nil) // feed fulltext = 0

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.NotContains(t, r.lastNote(id), "fulltext", "switch off, feed off: nothing extracted")
	require.EqualValues(t, 0, r.num("SELECT count(*) FROM item_fulltext"))

	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"fetch.fulltext_all": true}))
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	r.advanceTo(r.next(id))
	r.waitEvents("fetch.done", 2)
	r.waitRows(id, 1, 0)
	require.Equal(t, 1, srv.count("/a/3"))
	require.Equal(t, 0, srv.count("/a/1"), "old items are not backfilled")

	// Switching it off again stops extraction of the next new item.
	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"fetch.fulltext_all": false}))
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3, 4))
	r.advanceTo(r.next(id))
	r.waitEvents("fetch.done", 3)
	require.Equal(t, 0, srv.count("/a/4"))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM item_fulltext"))
}

// ftItems builds parsed feed items g<i> (url base/a/<i>, newer for a higher i).
func ftItems(base string, idx ...int) (items []fetch.Item) {
	for _, i := range idx {
		pub := time.Date(2026, 9, 1, i, 0, 0, 0, time.UTC)
		items = append(items, fetch.Item{UID: fmt.Sprintf("g%d", i), URL: fmt.Sprintf("%s/a/%d", base, i), Published: &pub})
	}
	return items
}

// commitFTItems inserts items with content rows, standing in for the fetch
// commit that follows the pick: like it, it marks each one pending and returns
// them as CommitInfo.Held (uid -> id).
func (r *rig) commitFTItems(feed int64, items []fetch.Item) map[string]int64 {
	r.t.Helper()
	held := map[string]int64{}
	for n, it := range items {
		id := int64(1_000_000*(n+1)) + feed
		r.sql(`INSERT INTO items (id, feed_id, uid, url, title, published_at, sort_at, content_hash, text_hash)
		       VALUES (?, ?, ?, ?, 't', ?, ?, 'c', 't')`, id, feed, it.UID, it.URL, it.Published.Unix(), it.Published.Unix())
		r.sql(`INSERT INTO item_content (item_id, content_html) VALUES (?, '<p>x</p>')`, id)
		r.db.MarkFulltextPending(id)
		held[it.UID] = id
	}
	return held
}

func (r *rig) okResult(feed int64, snapFT bool, items []fetch.Item) *fetch.Result {
	return &fetch.Result{Snap: fetch.Snapshot{ID: feed, Fulltext: snapFT}, Outcome: fetch.OutcomeOK, Feed: &fetch.Feed{Items: items}}
}

// Mode changes between the fetch's snapshot and its commit apply to the new items:
// queue-time evaluation uses the current switch and feed flag, not the snapshot.
func TestQueueTimeUsesCurrentModeSwitchTurnedOn(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{gate: make(chan struct{})}
	r := newRig(t, Options{Extractor: fx})
	feed := r.add("https://ex.test/f", nil) // feed flag off, switch off: the snapshot says no
	items := ftItems("https://art.test", 1, 2)
	res := r.okResult(feed, false, items)
	require.Empty(t, r.s.pickFulltext(context.Background(), res), "still off: nothing picked")

	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"fetch.fulltext_all": true}))
	cand := r.s.pickFulltext(context.Background(), res)
	require.Len(t, cand, 2, "the switch went on after the snapshot: the new items are picked")
	r.s.queueFulltext(feed, cand, r.commitFTItems(feed, items))
	fx.waitCalls(t, 2)
	require.Equal(t, 1, strings.Count(r.db.HoldPending(), ","), "the queued items are the ones the Reader API holds")
	close(fx.gate)
}

func TestQueueTimeUsesCurrentModeFeedFlagTurnedOn(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx})
	feed := r.add("https://ex.test/f", nil)
	items := ftItems("https://art.test", 1)
	r.sql("UPDATE feeds SET fulltext = 1 WHERE id = ?", feed)
	cand := r.s.pickFulltext(context.Background(), r.okResult(feed, false, items))
	require.Len(t, cand, 1)
	r.s.queueFulltext(feed, cand, r.commitFTItems(feed, items))
	fx.waitCalls(t, 1)
}

func TestQueueTimeUsesCurrentModeTurnedOff(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx})
	feed := r.ftFeed("https://ex.test/f")
	items := ftItems("https://art.test", 1, 2)
	res := r.okResult(feed, true, items)

	// Off before the pick: nothing picked although the snapshot said on.
	r.sql("UPDATE feeds SET fulltext = 0 WHERE id = ?", feed)
	require.Empty(t, r.s.pickFulltext(context.Background(), res))

	// Off between the pick and the queueing: nothing queued, so nothing held.
	r.sql("UPDATE feeds SET fulltext = 1 WHERE id = ?", feed)
	cand := r.s.pickFulltext(context.Background(), res)
	require.Len(t, cand, 2)
	r.sql("UPDATE feeds SET fulltext = 0 WHERE id = ?", feed)
	r.s.queueFulltext(feed, cand, r.commitFTItems(feed, items))
	require.Equal(t, "[]", r.db.HoldPending())
	time.Sleep(50 * time.Millisecond)
	calls, _, _ := fx.snapshot()
	require.Empty(t, calls)
}

// While the switch is on the per-fetch cap and the queue bound are the larger
// "all" limits; concurrency is untouched.
func TestSwitchOnScalesPerFetchCapAndQueue(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{Extractor: &fakeExt{block: true}, FulltextMaxItems: 2, FulltextQueue: 3, FulltextMaxItemsAll: 5, FulltextQueueAll: 4, FulltextGlobal: 1})
	feed := r.add("https://ex.test/f", nil)
	items := ftItems("https://art.test", 1, 2, 3, 4, 5, 6, 7)
	r.sql("UPDATE feeds SET fulltext = 1 WHERE id = ?", feed)
	res := r.okResult(feed, true, items)
	require.Len(t, r.s.pickFulltext(context.Background(), res), 2, "switch off: the plain cap")
	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"fetch.fulltext_all": true}))
	res = r.okResult(feed, true, items)
	require.Len(t, r.s.pickFulltext(context.Background(), res), 4, "switch on: min(all cap 5, all queue 4)")
	require.Equal(t, 4, r.s.ftq.free(r.s.opt.FulltextQueueAll))
	require.Equal(t, 3, r.s.ftq.free(r.s.opt.FulltextQueue), "the plain bound still applies while the switch is off")
}

// The Reader API holds only items that were really queued: deferred ones are
// served at once, and a finished one is released with its row.
func TestOnlyQueuedItemsArePending(t *testing.T) {
	t.Parallel()
	fx := &fakeExt{block: true}
	r := newRig(t, Options{Extractor: fx, FulltextQueue: 2, FulltextGlobal: 1})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))

	var pending []int64
	require.NoError(t, json.Unmarshal([]byte(r.db.HoldPending()), &pending))
	require.Len(t, pending, 2, "the 3 deferred items are not pending")
	for _, p := range pending {
		u := scalarStr(t, r, "SELECT url FROM items WHERE id = ?", p)
		require.Contains(t, []string{"https://art.test/a/5", "https://art.test/a/4"}, u)
	}
}

func scalarStr(t *testing.T, r *rig, q string, args ...any) string {
	t.Helper()
	var s string
	require.NoError(t, r.db.Reader().QueryRow(q, args...).Scan(&s))
	return s
}

func TestPendingClearsWhenExtractionFinishes(t *testing.T) {
	t.Parallel()
	r := newRig(t, Options{})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed(srv.URL, 1, 2))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	r.waitRows(id, 2, 0)
	waitFor(t, "pending cleared", func() bool { return r.db.HoldPending() == "[]" })
}
