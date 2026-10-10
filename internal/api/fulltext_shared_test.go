package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/extract"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/ftrun"
	"github.com/WPTK/kipple/internal/sched"
)

type gatedExtractor struct {
	calls   atomic.Int32
	started chan struct{}
	gate    chan struct{}
}

func (e *gatedExtractor) Extract(ctx context.Context, t extract.Target) (extract.Result, error) {
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	select {
	case <-e.gate:
	case <-ctx.Done():
		return extract.Result{}, ctx.Err()
	}
	return extract.Result{HTML: "<p>full text</p>", Text: "full text", WordCount: 2, SourceURL: t.URL}, nil
}

// Opening an item while the ingest pool is extracting it joins that run: the
// page is fetched once, and the response carries the stored text.
func TestOpeningAnItemTheIngestPoolIsExtractingJoinsTheRun(t *testing.T) {
	t.Parallel()
	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`+
			`<item><guid>g1</guid><title>I1</title><link>https://art.test/a</link></item></channel></rss>`)
	}))
	t.Cleanup(feedSrv.Close)

	ext := &gatedExtractor{started: make(chan struct{}), gate: make(chan struct{})}
	var real *sched.Scheduler
	h := newHarness(t, func(o *Options) {
		runner := ftrun.New(ftrun.Options{DB: o.DB, Extractor: ext, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		real = sched.New(o.DB, fetch.NewClient(fetch.ClientOptions{}), o.Hub, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)), sched.Options{Runner: runner, Tick: time.Hour})
		real.Start()
		o.Sched = real
		o.Runner = runner
	})
	t.Cleanup(func() {
		real.Stop()
		<-real.Stopped()
	})
	c := h.login()
	fid := h.storeFeed(feedSrv.URL)
	h.exec("UPDATE feeds SET fulltext = 1, allow_private_net = 1 WHERE id = ?", fid)
	code, _, _ := h.api(c, "POST", fmt.Sprintf("/api/feeds/%d/refresh", fid), "")
	require.Equal(t, 200, code)
	select {
	case <-ext.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the pool never started extracting")
	}
	item := h.count("SELECT id FROM items WHERE feed_id = ?", fid)

	type reply struct {
		code int
		body map[string]any
	}
	got := make(chan reply, 1)
	go func() {
		code, body, _ := h.api(c, "POST", fmt.Sprintf("/api/items/%d/fulltext", item), "")
		got <- reply{code, body}
	}()
	time.Sleep(200 * time.Millisecond) // the request is now waiting on the pool's run
	close(ext.gate)
	select {
	case r := <-got:
		require.Equal(t, 200, r.code)
		require.Equal(t, "ok", r.body["status"])
		require.Contains(t, r.body["content_html"], "full text")
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not finish")
	}
	require.EqualValues(t, 1, ext.calls.Load(), "one fetch for the pool and the endpoint together")
	require.Equal(t, 1, h.count("SELECT count(*) FROM item_fulltext WHERE item_id = ? AND content_html IS NOT NULL", item))
}
