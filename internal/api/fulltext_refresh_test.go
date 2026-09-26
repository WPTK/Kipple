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
	"github.com/WPTK/kipple/internal/sched"
)

type slowExtractor struct{ started atomic.Int32 }

func (e *slowExtractor) Extract(ctx context.Context, _ extract.Target) (extract.Result, error) {
	e.started.Add(1)
	<-ctx.Done()
	return extract.Result{}, ctx.Err()
}

// POST /api/feeds/{id}/refresh answers when the fetch is committed, not when
// the feed's article pages are extracted.
func TestRefreshOfFulltextFeedDoesNotWaitForSlowArticleHosts(t *testing.T) {
	feedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>`)
		for i := 1; i <= 5; i++ {
			fmt.Fprintf(w, `<item><guid>g%d</guid><title>I%d</title><link>https://slow%d.test/a</link></item>`, i, i, i)
		}
		fmt.Fprint(w, `</channel></rss>`)
	}))
	t.Cleanup(feedSrv.Close)

	ext := &slowExtractor{}
	var real *sched.Scheduler
	h := newHarness(t, func(o *Options) {
		real = sched.New(o.DB, fetch.NewClient(fetch.ClientOptions{}), o.Hub, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)), sched.Options{Extractor: ext, Tick: time.Hour})
		real.Start()
		o.Sched = real
	})
	t.Cleanup(func() {
		real.Stop()
		<-real.Stopped()
	})
	c := h.login()
	id := h.storeFeed(feedSrv.URL)
	h.exec("UPDATE feeds SET fulltext = 1, allow_private_net = 1 WHERE id = ?", id)

	start := time.Now()
	code, body, _ := h.api(c, "POST", fmt.Sprintf("/api/feeds/%d/refresh", id), "")
	took := time.Since(start)
	require.Equal(t, 200, code, "a full result, not 202 pending")
	require.Nil(t, body["pending"])
	require.Equal(t, "ok", body["outcome"])
	require.EqualValues(t, 5, body["new_items"])
	require.Less(t, took, 5*time.Second, "extraction hangs for 10 s per article; the refresh must not wait for it")
	require.Equal(t, 5, h.count("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Eventually(t, func() bool { return ext.started.Load() > 0 }, 10*time.Second, 10*time.Millisecond, "extraction proceeds in the background")
}
