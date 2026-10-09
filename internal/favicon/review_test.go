package favicon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// localhostURL names srv by "localhost" instead of 127.0.0.1: the same server,
// but a different host, so a feed exception scoped to 127.0.0.1 does not cover it.
func localhostURL(srv *httptest.Server) string {
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
}

// Reproduction of the review finding: a feed on a LAN host with "allow private
// network" whose site_url points at another (here loopback) address. The grant
// covers the feed's own host only, so the lookup is refused at dial time and
// nothing reaches the server.
func TestFinderScopesTheGrantToTheFeedHost(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	id := e.fetchedFeed("http://nas.lan/feed.xml", srv.URL+"/", true)

	did, err := e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Zero(t, hits.Load(), "the grant of nas.lan does not cover 127.0.0.1")
	require.Zero(t, e.int("SELECT count(*) FROM feed_icons"))
	require.Equal(t, int64(1), e.int("SELECT count(*) FROM feed_icon_checks WHERE feed_id = ? AND last_error LIKE '%not allowed%'", id))

	// The same lookup straight through Lookup reports a BlockedError.
	_, err = Lookup(e.ctx, Request{SiteURL: srv.URL + "/", Transport: fetch.ContentScopedTransport(fetch.NewClient(fetch.ClientOptions{}).Transport, "nas.lan", true, false, false), UserAgent: "x"})
	var be *fetch.BlockedError
	require.True(t, errors.As(err, &be), "got %v", err)
	require.Zero(t, hits.Load())
}

// Each hop is scoped: the page is on the feed's host, but an icon link and a
// redirect to another host go through the guarded default and are refused.
func TestLookupScopesEveryHop(t *testing.T) {
	var other atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other.Add(1)
		_, _ = w.Write(pngOf(t, 32))
	}))
	t.Cleanup(elsewhere.Close)
	away := localhostURL(elsewhere)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<head><link rel=icon sizes=64x64 href="` + away + `/direct.png">
				<link rel=icon sizes=48x48 href="/hop.png">`))
		case "/hop.png":
			http.Redirect(w, r, away+"/redirected.png", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	guard := fetch.NewClient(fetch.ClientOptions{}).Transport
	_, err := Lookup(context.Background(), Request{SiteURL: srv.URL, Transport: fetch.ContentScopedTransport(guard, "127.0.0.1", true, false, false), UserAgent: "x"})
	require.Error(t, err)
	require.Zero(t, other.Load(), "neither the link nor the redirect reached the other host")

	// Without a grant, or with no feed host, the default transport is used alone.
	require.NotNil(t, fetch.ContentScopedTransport(guard, "", true, false, false))
	require.Same(t, guard(false, false, false), fetch.ContentScopedTransport(guard, "127.0.0.1", false, false, false))
}

// Userinfo in the site URL, an icon link or a redirect is never sent as
// credentials and never stored.
func TestLookupNeverSendsOrStoresUserinfo(t *testing.T) {
	ico := icoOf(t, 32)
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<head><link rel=icon sizes=64x64 href="http://ann:linksecret@` + r.Host + `/hop">`))
		case "/hop":
			http.Redirect(w, r, "http://bob:redirsecret@"+r.Host+"/i.ico", http.StatusFound)
		case "/i.ico":
			_, _ = w.Write(ico)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	site := strings.Replace(srv.URL, "http://", "http://cy:sitesecret@", 1)
	icon, err := lookup(t, site)
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/i.ico", icon.SourceURL)
	require.NotContains(t, icon.SourceURL, "@")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, auths, 3)
	for _, a := range auths {
		require.Empty(t, a, "no credentials were sent")
	}
	page, err := PageURL(site, "")
	require.NoError(t, err)
	require.NotContains(t, page, "sitesecret")
	for _, c := range iconLinks([]byte(`<link rel=icon href="https://u:p@example.com/i.png">`), "https://example.com/") {
		require.Equal(t, "https://example.com/i.png", c.URL)
	}
}

// icoWith wraps payload in a one-entry ICO directory declaring side x side.
func icoWith(side byte, payload []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, 1})
	b.Write([]byte{side, side, 0, 0})
	_ = binary.Write(&b, binary.LittleEndian, [2]uint16{1, 32})
	_ = binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(payload)), 22})
	b.Write(payload)
	return b.Bytes()
}

// An ICO is checked beyond its directory: the first image must be a PNG or a
// bitmap (BITMAPINFOHEADER, size 40), and at least minSide.
func TestSniffICOPayload(t *testing.T) {
	bmp := make([]byte, 40+32*32*4)
	binary.LittleEndian.PutUint32(bmp[0:4], 40)
	binary.LittleEndian.PutUint32(bmp[4:8], 32)
	binary.LittleEndian.PutUint32(bmp[8:12], 64)
	ct, err := sniff(icoWith(32, bmp))
	require.NoError(t, err)
	require.Equal(t, "image/x-icon", ct)
	ct, err = sniff(icoWith(0, pngOf(t, 256))) // 0 means 256
	require.NoError(t, err)
	require.Equal(t, "image/x-icon", ct)

	for name, b := range map[string][]byte{
		"html payload": icoWith(32, []byte("<!doctype html><html><body>not an icon</body></html>")),
		"svg payload":  icoWith(32, []byte(svg)),
		"tiny":         icoWith(1, pngOf(t, 1)),
		"tiny png":     icoWith(32, pngOf(t, 2)),
		"bad png":      icoWith(32, []byte("\x89PNG\r\n\x1a\ngarbage")),
		"short bmp":    icoWith(32, []byte{40, 0, 0, 0}),
	} {
		_, err := sniff(b)
		require.Error(t, err, name)
	}
}

// One site is looked up at most once per siteSpacing: feeds of the same site
// under the same network scope reuse the outcome with no request, and a feed
// of that site under another scope waits for the spacing to pass.
func TestFinderPacesOneSite(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	var ids []int64
	for _, p := range []string{"/r/a", "/r/b", "/r/c", "/r/d", "/r/e"} {
		ids = append(ids, e.fetchedFeed(srv.URL+p+"/.rss", srv.URL+p+"/", true))
	}
	for range ids {
		did, err := e.f.RunOnce(e.ctx)
		require.NoError(t, err)
		require.True(t, did)
	}
	require.Equal(t, int32(2), hits.Load(), "one lookup (page and favicon.ico) for five feeds of one site")
	require.Equal(t, int64(len(ids)), e.int("SELECT count(*) FROM feed_icons"))
	did, err := e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.False(t, did)

	// Another scope (no exception): deferred, then looked up on its own.
	other := e.fetchedFeed("https://feeds.example.com/x", srv.URL+"/x/", false)
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.False(t, did, "the site was just looked up under another scope")
	e.clk.Set(e.clk.Now().Add(siteSpacing))
	did, err = e.f.RunOnce(e.ctx)
	require.NoError(t, err)
	require.True(t, did)
	require.Equal(t, int64(1), e.int("SELECT failures FROM feed_icon_checks WHERE feed_id = ?", other), "refused by the guard")
	require.Equal(t, int32(2), hits.Load())
}

// The loop does not wait between lookups it answered from a recent outcome.
func TestFinderPassSkipsTheGapForReusedOutcomes(t *testing.T) {
	e := newFEnv(t)
	var hits atomic.Int32
	srv := iconSite(t, &hits)
	for _, p := range []string{"/a", "/b", "/c"} {
		e.fetchedFeed(srv.URL+p+"/.rss", srv.URL+p+"/", true)
	}
	f := New(Options{DB: e.db, Guard: fetch.NewClient(fetch.ClientOptions{}).Transport, Clock: e.clk, Gap: time.Hour})
	t.Cleanup(f.Stop) // cancels a pass stuck in the hour-long gap
	done := make(chan struct{})
	go func() { defer close(done); f.pass() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the pass waited out the gap before a reused outcome")
	}
	require.Equal(t, int64(3), e.int("SELECT count(*) FROM feed_icons"), "one fetch, two reuses")
	require.Equal(t, int32(2), hits.Load())
}
