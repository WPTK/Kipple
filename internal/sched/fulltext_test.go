package sched

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
)

// ftFeed builds an RSS document with items n-1 .. 0 whose links are
// base+"/a/<i>"; a higher i is newer.
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
// /a/<n>; fail lists the article numbers that answer 500.
type ftServer struct {
	*feedSrv
	body atomic.Value
}

func newFTServer(t *testing.T, fail ...string) *ftServer {
	t.Helper()
	ts := &ftServer{}
	ts.feedSrv = newSrv(t, func(path string, w http.ResponseWriter, _ *http.Request) {
		if path == "/f" {
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(ts.body.Load().(string)))
			return
		}
		for _, f := range fail {
			if path == "/a/"+f {
				w.WriteHeader(http.StatusInternalServerError)
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

func (r *rig) lastNote(feedID int64) string {
	r.t.Helper()
	var n *string
	require.NoError(r.t, r.db.Reader().QueryRow(`SELECT note FROM fetch_log WHERE feed_id = ? AND outcome = 'ok' ORDER BY id DESC LIMIT 1`, feedID).Scan(&n))
	if n == nil {
		return ""
	}
	return *n
}

func TestInlineExtractsNewItemsOnce(t *testing.T) {
	r := newRig(t, Options{})
	srv := newFTServer(t)
	srv.body.Store(ftFeed(srv.URL, 1, 2))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	ok, failed := r.ftRows(id)
	require.EqualValues(t, 2, ok)
	require.Zero(t, failed)
	require.Contains(t, r.lastNote(id), "fulltext: 2/2")
	require.Equal(t, 1, srv.count("/a/1"))
	require.Equal(t, 1, srv.count("/a/2"))
	require.Positive(t, r.num("SELECT word_count FROM item_fulltext ORDER BY item_id LIMIT 1"))

	// A later fetch with one more item extracts only the new one.
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	r.clk.Advance(31 * time.Minute)
	r.waitEvents("fetch.done", 2)
	ok, _ = r.ftRows(id)
	require.EqualValues(t, 3, ok)
	require.Equal(t, 1, srv.count("/a/1"), "no re-extract of an item that already has its text")
	require.Equal(t, 1, srv.count("/a/2"))
	require.Equal(t, 1, srv.count("/a/3"))
}

func TestInlineFailureIsIsolatedAndNotRetried(t *testing.T) {
	r := newRig(t, Options{})
	srv := newFTServer(t, "1")
	srv.body.Store(ftFeed(srv.URL, 1, 2))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"], "an extraction failure never fails the fetch")
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	ok, failed := r.ftRows(id)
	require.EqualValues(t, 1, ok)
	require.EqualValues(t, 1, failed)
	var msg string
	require.NoError(t, r.db.Reader().QueryRow("SELECT error FROM item_fulltext WHERE error IS NOT NULL").Scan(&msg))
	require.Contains(t, msg, "HTTP 500")
	require.Zero(t, r.failures(id))
	require.Contains(t, r.lastNote(id), "fulltext: 1/2")

	// Polling again does not hammer the broken page.
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	r.clk.Advance(31 * time.Minute)
	r.waitEvents("fetch.done", 2)
	require.Equal(t, 1, srv.count("/a/1"), "a failed item is not retried by polling")
}

// fakeExt records calls and how many ran at once, per host and overall.
type fakeExt struct {
	mu      sync.Mutex
	calls   []string
	cur     int
	max     int
	perHost map[string]int
	maxHost int
	hold    time.Duration
	block   bool // wait for ctx to end
}

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
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.cur--
		f.perHost[host]--
		f.mu.Unlock()
	}()
	if f.block {
		<-ctx.Done()
		return extract.Result{}, errors.New("cut off")
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

func TestInlineCapDefersTheRestNewestFirst(t *testing.T) {
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx, FulltextMaxItems: 2})
	srv := newFTServer(t)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	calls, _, _ := fx.snapshot()
	require.ElementsMatch(t, []string{"https://art.test/a/5", "https://art.test/a/4"}, calls, "the two newest")
	ok, failed := r.ftRows(id)
	require.EqualValues(t, 2, ok)
	require.Zero(t, failed, "deferred items get no row, the on-demand endpoint handles them")
	note := r.lastNote(id)
	require.Contains(t, note, "fulltext: 2/2")
	require.Contains(t, note, "fulltext_deferred: 3")
}

func TestInlineConcurrencyAndPerHostLimits(t *testing.T) {
	fx := &fakeExt{hold: 60 * time.Millisecond}
	r := newRig(t, Options{Extractor: fx, FulltextConcurrency: 3, FulltextPerHost: 2})
	srv := newFTServer(t)
	srv.body.Store(ftFeed("https://one.test", 1, 2, 3, 4, 5, 6))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	ok, _ := r.ftRows(id)
	require.EqualValues(t, 6, ok)
	_, maxAll, maxHost := fx.snapshot()
	require.LessOrEqual(t, maxHost, 2, "at most 2 at once against one article host")
	require.Equal(t, 2, maxHost, "and the limit is actually used")
	require.LessOrEqual(t, maxAll, 3)

	// Spread over hosts, the per-fetch concurrency is the bound.
	fx2 := &fakeExt{hold: 100 * time.Millisecond}
	r2 := newRig(t, Options{Extractor: fx2, FulltextConcurrency: 3, FulltextPerHost: 2})
	srv2 := newFTServer(t)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&b, `<item><guid>g%d</guid><title>I%d</title><link>https://h%d.test/a</link></item>`, i, i, i)
	}
	b.WriteString(`</channel></rss>`)
	srv2.body.Store(b.String())
	id2 := r2.ftFeed(srv2.URL + "/f")
	r2.s.Wake()
	r2.waitEvents("fetch.done", 1)
	ok, _ = r2.ftRows(id2)
	require.EqualValues(t, 6, ok)
	_, maxAll, _ = fx2.snapshot()
	require.LessOrEqual(t, maxAll, 3)
	require.Equal(t, 3, maxAll)
}

func TestInlineOffDoesNothing(t *testing.T) {
	fx := &fakeExt{}
	r := newRig(t, Options{Extractor: fx})
	srv := newFTServer(t)
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

func TestInlineRunBudgetDefersInsteadOfFailing(t *testing.T) {
	fx := &fakeExt{block: true}
	r := newRig(t, Options{Extractor: fx, FulltextTotal: 300 * time.Millisecond})
	srv := newFTServer(t)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	id := r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
	require.EqualValues(t, 5, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	ok, failed := r.ftRows(id)
	require.Zero(t, ok)
	require.Zero(t, failed, "running out of budget is not the page's failure")
	require.Contains(t, r.lastNote(id), "fulltext_deferred: 5")
}

func TestInlineBlockedTargetIsRecordedAsError(t *testing.T) {
	r := newRig(t, Options{})
	srv := newFTServer(t) // article server on 127.0.0.1
	now := time.Now()
	res := &fetch.Result{
		Outcome: fetch.OutcomeOK,
		Snap:    fetch.Snapshot{ID: 999, Fulltext: true, AllowPrivateNet: false},
		Feed: &fetch.Feed{Items: []fetch.Item{
			{UID: "u1", URL: srv.URL + "/a/1", Published: &now},
		}},
	}
	r.s.extractInline(context.Background(), res)
	got, ok := res.Fulltext["u1"]
	require.True(t, ok)
	require.NotEmpty(t, got.Error, "the SSRF guard refuses a private address")
	require.Empty(t, got.HTML)
	require.Zero(t, srv.count("/a/1"), "the request never reached the server")
}

func TestHostLimiterReleasesAndCancels(t *testing.T) {
	l := newHostLimiter(1)
	rel, ok := l.acquire(context.Background(), "h")
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, ok = l.acquire(ctx, "h")
	require.False(t, ok, "a full host waits until the context ends")
	rel()
	rel2, ok := l.acquire(context.Background(), "h")
	require.True(t, ok)
	rel2()
	l.mu.Lock()
	require.Empty(t, l.m, "idle hosts are forgotten")
	l.mu.Unlock()
}

func TestInlineGlobalCapAcrossFeeds(t *testing.T) {
	fx := &fakeExt{hold: 150 * time.Millisecond}
	r := newRig(t, Options{Extractor: fx, Workers: 6, FulltextConcurrency: 3, FulltextPerHost: 3, FulltextGlobal: 2, FulltextTotal: 30 * time.Second})
	const feeds = 5
	for i := 0; i < feeds; i++ {
		srv := newFTServer(t)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
		for j := 1; j <= 3; j++ {
			fmt.Fprintf(&b, `<item><guid>g%d</guid><title>I%d</title><link>https://f%dh%d.test/a</link></item>`, j, j, i, j)
		}
		b.WriteString(`</channel></rss>`)
		srv.body.Store(b.String())
		r.ftFeed(srv.URL + "/f")
	}
	r.s.Wake()
	r.waitEvents("fetch.done", feeds)
	calls, maxAll, _ := fx.snapshot()
	require.Len(t, calls, feeds*3)
	require.LessOrEqual(t, maxAll, 2, "global cap holds across workers")
	require.Equal(t, 2, maxAll, "and is actually used")
}
