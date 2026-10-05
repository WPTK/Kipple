package sched

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

const blogPage = `<!doctype html><html><head><title>Blog</title>
<link rel="alternate" type="application/rss+xml" title="Posts" href="/feed.xml">
<link rel="alternate" type="application/rss+xml" title="Comments" href="/comments.xml">
</head><body>hello</body></html>`

func servePage(p string, w http.ResponseWriter, req *http.Request) {
	if p == "/" {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(blogPage))
		return
	}
	serveOK(p, w, req)
}

// A feed stored as a page address (as a Reader API subscribe or an OPML import stores it, with no
// network request) becomes the page's feed: the page fetch adopts the link, and the feed is fetched
// right after, as a fetch of its own.
func TestFirstFetchDiscoversThePagesFeed(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, servePage)
	id := r.add(srv.URL+"/", nil)

	r.s.Wake()
	r.waitEvents("fetch.done", 2)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
	require.EqualValues(t, 0, r.events("fetch.done")[0]["new_items"])
	require.EqualValues(t, 2, r.events("fetch.done")[1]["new_items"])
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM feeds WHERE id = ? AND url = ? AND url_original = ?", id, srv.URL+"/feed.xml", srv.URL+"/"))
	require.EqualValues(t, 2, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.Equal(t, 1, srv.count("/"))
	require.Equal(t, 1, srv.count("/feed.xml"))
	require.Zero(t, srv.count("/comments.xml"))
	waitFor(t, "feed.changed", func() bool { return len(r.events("feed.changed")) >= 1 })
}

// A page whose feed is already subscribed: the new feed is removed, the existing one is untouched.
func TestFirstFetchOfAPageWhoseFeedExistsLeavesOneFeed(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, servePage)
	kept := r.add(srv.URL+"/feed.xml", nil)
	r.sql("UPDATE feeds SET next_fetch_at = ? WHERE id = ?", base.Add(24*time.Hour).Unix(), kept)
	page := r.add(srv.URL+"/", nil)

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Zero(t, r.num("SELECT count(*) FROM feeds WHERE id = ?", page))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM feeds WHERE id = ? AND url = ?", kept, srv.URL+"/feed.xml"))
	require.Zero(t, r.num("SELECT count(*) FROM items"), "the kept feed is fetched on its own schedule")
	waitFor(t, "feed.changed", func() bool { return len(r.events("feed.changed")) >= 1 })
}

// An OPML import of many page addresses on one site whose feeds all live on another host: the
// linked feeds are fetched under that host's per-host limit, not the pages' host's.
func TestDiscoveredFeedsKeepTheirHostsLimit(t *testing.T) {
	r := newRig(t, Options{PerHost: 2, Workers: 8})
	client := fetch.NewClient(fetch.ClientOptions{})
	var inFlight, peak atomic.Int32
	feeds := newSrv(t, func(p string, w http.ResponseWriter, req *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		serveOK(p, w, req)
	})
	feedBase := strings.Replace(feeds.URL, "127.0.0.1", "localhost", 1)
	// The pages' fetch is what fetch.Fetch returns for a page that links a feed (tested in the fetch
	// package); the linked feeds are fetched for real, with the private-network exception the loopback
	// test server needs.
	r.s.fetchFn = func(ctx context.Context, snap fetch.Snapshot, now time.Time) *fetch.Result {
		if i := strings.LastIndex(snap.URL, "/p"); i >= 0 && !strings.HasPrefix(snap.URL, feedBase) {
			return &fetch.Result{Snap: snap, StartedAt: now, Outcome: fetch.OutcomeOK, Status: 200,
				Discovered: feedBase + "/f" + snap.URL[i+2:]}
		}
		snap.AllowPrivateNet = true
		return client.Fetch(ctx, snap, now)
	}
	pages := "http://pages.example"
	const n = 8
	for i := 0; i < n; i++ {
		r.add(fmt.Sprintf("%s/p%d", pages, i), func(f *store.NewFeed) { f.AllowPrivateNet = false })
	}
	r.s.Wake()
	r.waitEvents("fetch.done", 2*n)
	require.EqualValues(t, n, r.num("SELECT count(*) FROM feeds WHERE host = 'localhost' AND last_success_at IS NOT NULL"))
	require.LessOrEqual(t, peak.Load(), int32(2), "the feeds' host never had more than PerHost fetches at once")
	require.EqualValues(t, 2*n, r.num("SELECT count(*) FROM items"))
}
