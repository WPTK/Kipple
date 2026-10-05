package sched

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
// network request) becomes the page's feed on its first scheduled fetch.
func TestFirstFetchDiscoversThePagesFeed(t *testing.T) {
	r := newRig(t, Options{})
	srv := newSrv(t, servePage)
	id := r.add(srv.URL+"/", nil)

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.Equal(t, "ok", r.events("fetch.done")[0]["outcome"])
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
