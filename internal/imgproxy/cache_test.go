package imgproxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgcache"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type cacheRig struct {
	*rig
	cache *imgcache.Cache
	clk   *testClock
	h     *Handler
}

func newCacheRig(t *testing.T, tune ...func(*Options)) *cacheRig {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	c, err := imgcache.Open(imgcache.Options{
		Dir: filepath.Join(t.TempDir(), "imgcache"), MaxBytes: 64 << 20, Now: clk.Now, NoBackgound: true,
		DiskSpace: func(string) (uint64, uint64, error) { return 500 << 30, 800 << 30, nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	cr := &cacheRig{cache: c, clk: clk}
	fc := fetch.NewClient(fetch.ClientOptions{})
	opt := Options{
		Secret: secret, Cache: c,
		Transport: func(allowPrivate, insecure bool) http.RoundTripper {
			return fc.Transport(allowPrivate, insecure, false)
		},
	}
	for _, f := range tune {
		f(&opt)
	}
	cr.h = New(opt)
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", cr.h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cr.rig = &rig{t: t, srv: srv}
	return cr
}

func (cr *cacheRig) entry(orig string) (imgcache.Entry, bool) {
	e, ok := cr.cache.Peek(context.Background(), imgcache.KeyOrig(FlagPrivateNet, orig))
	return e, ok
}

func read(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return b
}

type countingUpstream struct {
	*httptest.Server
	n atomic.Int32
}

func countingImg(t *testing.T, mut ...func(w http.ResponseWriter, r *http.Request)) *countingUpstream {
	t.Helper()
	cu := &countingUpstream{}
	cu.Server = upstream(t, func(w http.ResponseWriter, r *http.Request) {
		cu.n.Add(1)
		for _, m := range mut {
			m(w, r)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	return cu
}

func TestMissTeesToCacheThenHitServesFromDisk(t *testing.T) {
	cr := newCacheRig(t)
	up := countingImg(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"up-etag"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
	})
	orig := up.URL + "/a.png"

	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp))
	require.EqualValues(t, 1, up.n.Load())
	e, ok := cr.entry(orig)
	require.True(t, ok, "cached at the end of the stream")
	require.Equal(t, int64(len(pngBytes)), e.Size)
	require.Equal(t, "image/png", e.ContentType)
	require.Equal(t, `"up-etag"`, e.ETag)

	resp = cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp))
	require.EqualValues(t, 1, up.n.Load(), "the hit never touches the source")
	require.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	require.Equal(t, "private, max-age=2592000, immutable", resp.Header.Get("Cache-Control"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "default-src 'none'", resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Resource-Policy"))
	require.Equal(t, `"`+e.SHA256[:16]+`"`, resp.Header.Get("ETag"))
	require.Equal(t, "Wed, 01 Jan 2025 00:00:00 GMT", resp.Header.Get("Last-Modified"))

	// Conditional and Range requests are answered from the file.
	resp = cr.fetchOrig(orig, FlagPrivateNet, "If-None-Match", `"`+e.SHA256[:16]+`"`)
	require.Equal(t, 304, resp.StatusCode)
	resp = cr.fetchOrig(orig, FlagPrivateNet, "Range", "bytes=0-7")
	require.Equal(t, 206, resp.StatusCode)
	require.Equal(t, pngBytes[:8], read(t, resp))
	require.Equal(t, "bytes 0-7/"+strconv.Itoa(len(pngBytes)), resp.Header.Get("Content-Range"))
	require.EqualValues(t, 1, up.n.Load())
}

func TestKeyBindsFlags(t *testing.T) {
	cr := newCacheRig(t)
	up := countingImg(t)
	orig := up.URL + "/a.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet|FlagInsecureTLS).StatusCode)
	require.EqualValues(t, 2, up.n.Load(), "another flags variant is another entry")
}

func TestSingleFlightOneUpstreamCall(t *testing.T) {
	cr := newCacheRig(t)
	gate := make(chan struct{})
	up := countingImg(t, func(w http.ResponseWriter, r *http.Request) { <-gate })
	orig := up.URL + "/slow.png"

	var wg sync.WaitGroup
	bodies := make([][]byte, 20)
	codes := make([]int, 20)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := cr.fetchOrig(orig, FlagPrivateNet)
			codes[i] = resp.StatusCode
			bodies[i], _ = io.ReadAll(resp.Body)
		}()
	}
	require.Eventually(t, func() bool { return up.n.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond) // the followers are queued behind the leader
	require.EqualValues(t, 1, up.n.Load())
	close(gate)
	wg.Wait()
	for i := range bodies {
		require.Equal(t, 200, codes[i])
		require.Equal(t, pngBytes, bodies[i])
	}
	require.EqualValues(t, 1, up.n.Load(), "20 concurrent requests made one upstream call")
}

func TestFailedLeaderGivesFollowersAnErrorWithoutAStampede(t *testing.T) {
	cr := newCacheRig(t)
	gate := make(chan struct{})
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		<-gate
		w.WriteHeader(503)
	})
	orig := up.URL + "/down.png"
	var wg sync.WaitGroup
	codes := make([]int, 10)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = cr.fetchOrig(orig, FlagPrivateNet).StatusCode
		}()
	}
	require.Eventually(t, func() bool { return n.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	for _, c := range codes {
		require.Equal(t, 502, c)
	}
	require.EqualValues(t, 1, n.Load())
}

func TestPartialAndOverflowBodiesAreNeverCached(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.MaxBytes = 1 << 20 })
	// A declared length the upstream does not deliver.
	short := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "20000")
		_, _ = w.Write(bigPNG)
		// returning early closes the connection with the body incomplete
	})
	resp := cr.fetchOrig(short.URL+"/short.png", FlagPrivateNet)
	_, err := io.ReadAll(resp.Body)
	require.Error(t, err)
	_, ok := cr.entry(short.URL + "/short.png")
	require.False(t, ok)

	// An undeclared body past the limit aborts and remembers "too large".
	huge := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.(http.Flusher).Flush()
		_, _ = w.Write(pngBytes)
		chunk := bytes.Repeat([]byte{7}, 64<<10)
		for i := 0; i < 100; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	orig := huge.URL + "/huge.png"
	resp = cr.fetchOrig(orig, FlagPrivateNet)
	_, err = io.Copy(io.Discard, resp.Body)
	require.Error(t, err)
	e, ok := cr.entry(orig)
	require.True(t, ok)
	require.False(t, e.OK, "remembered as a failure, not cached")
	require.Equal(t, 24*time.Hour, e.FreshUntil.Sub(e.FetchedAt))
	require.Zero(t, cr.cache.Stats().Files)
	ents, _ := os.ReadDir(filepath.Join(cr.cache.Dir(), "tmp"))
	require.Empty(t, ents)
}

func TestClientAbortMidStreamIsNotCached(t *testing.T) {
	cr := newCacheRig(t)
	gate := make(chan struct{})
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(bigPNG)+1000))
		_, _ = w.Write(bigPNG)
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
		}
	})
	orig := up.URL + "/abort.png"
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", cr.srv.URL+Path(secret, FlagPrivateNet, orig), nil)
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(t, err)
	buf := make([]byte, 16)
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	cancel()
	_ = resp.Body.Close()
	close(gate)
	require.Never(t, func() bool { _, ok := cr.entry(orig); return ok }, 300*time.Millisecond, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		ents, _ := os.ReadDir(filepath.Join(cr.cache.Dir(), "tmp"))
		return len(ents) == 0
	}, 5*time.Second, 10*time.Millisecond)
	// A client walking away is not the image's fault: nothing is remembered as a failure.
	require.Zero(t, cr.cache.Stats().Failures)
}

func TestNegativeCacheReplaysWithoutContactingTheSource(t *testing.T) {
	cr := newCacheRig(t)
	var n atomic.Int32
	status := atomic.Int32{}
	status.Store(404)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(int(status.Load()))
	})
	orig := up.URL + "/gone.png"
	for i := 0; i < 3; i++ {
		resp := cr.fetchOrig(orig, FlagPrivateNet)
		require.Equal(t, 502, resp.StatusCode)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	}
	require.EqualValues(t, 1, n.Load(), "a 404 is remembered")
	cr.clk.Advance(23 * time.Hour)
	require.Equal(t, 502, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.EqualValues(t, 1, n.Load())
	cr.clk.Advance(2 * time.Hour) // past 24 h
	status.Store(200)
	pngUp := countingImg(t)
	_ = pngUp
	require.Equal(t, 502, cr.fetchOrig(orig, FlagPrivateNet).StatusCode, "the source still answers 200 with an empty body")
	require.EqualValues(t, 2, n.Load(), "retried after the retry-after")

	// 5xx and timeouts: a short retry-after.
	up2 := upstream(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(503) })
	o2 := up2.URL + "/err.png"
	before := n.Load()
	require.Equal(t, 502, cr.fetchOrig(o2, FlagPrivateNet).StatusCode)
	require.Equal(t, 502, cr.fetchOrig(o2, FlagPrivateNet).StatusCode)
	require.Equal(t, before+1, n.Load())
	cr.clk.Advance(11 * time.Minute)
	require.Equal(t, 502, cr.fetchOrig(o2, FlagPrivateNet).StatusCode)
	require.Equal(t, before+2, n.Load())

	// An unsupported type keeps its 415.
	svg := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
	})
	o3 := svg.URL + "/x.svg"
	before = n.Load()
	require.Equal(t, 415, cr.fetchOrig(o3, FlagPrivateNet).StatusCode)
	require.Equal(t, 415, cr.fetchOrig(o3, FlagPrivateNet).StatusCode)
	require.Equal(t, before+1, n.Load())
}

func TestStaleEntryIsRevalidatedWith304(t *testing.T) {
	cr := newCacheRig(t)
	var inm, ims atomic.Value
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
		if r.Header.Get("If-None-Match") == `"v1"` {
			inm.Store(r.Header.Get("If-None-Match"))
			ims.Store(r.Header.Get("If-Modified-Since"))
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/r.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	cr.clk.Advance(8 * 24 * time.Hour) // past the 7 day default freshness

	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp))
	require.EqualValues(t, 2, n.Load())
	require.Equal(t, `"v1"`, inm.Load())
	require.Equal(t, "Wed, 01 Jan 2025 00:00:00 GMT", ims.Load())
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.EqualValues(t, 2, n.Load(), "fresh again after the 304")
}

func TestStaleEntryReplacedWhenSourceChanged(t *testing.T) {
	cr := newCacheRig(t)
	var body atomic.Value
	body.Store(pngBytes)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Load().([]byte))
	})
	orig := up.URL + "/c.png"
	require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
	newer := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9}, 100)...)
	body.Store(newer)
	cr.clk.Advance(8 * 24 * time.Hour)
	require.Equal(t, newer, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
	require.Equal(t, newer, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
	e, _ := cr.entry(orig)
	require.Equal(t, int64(len(newer)), e.Size)
}

func TestStaleServedWhenRevalidationFailsOrIsSlow(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.RevalidateWithin = 100 * time.Millisecond })
	var mode atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(503)
			return
		case 2:
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/s.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	cr.clk.Advance(8 * 24 * time.Hour)

	mode.Store(1)
	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp), "a 5xx serves the stale copy")

	mode.Store(2)
	start := time.Now()
	resp = cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp), "a slow source serves the stale copy")
	require.Less(t, time.Since(start), 2500*time.Millisecond)
	_, ok := cr.entry(orig)
	require.True(t, ok, "the stale entry is kept for the next try")
}

func TestStaleEntryDroppedWhenSourceSays404(t *testing.T) {
	cr := newCacheRig(t)
	var gone atomic.Bool
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if gone.Load() {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/d.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	cr.clk.Advance(8 * 24 * time.Hour)
	gone.Store(true)
	require.Equal(t, 502, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.Zero(t, cr.cache.Stats().Files)
	require.Zero(t, cr.cache.Stats().UsedBytes)
}

// bigPNG is longer than the 512 bytes the proxy sniffs and the 4 KiB the server buffers, so a stalled or cut
// upstream is noticed after the response has started.
var bigPNG = append(append([]byte{}, pngBytes[:8]...), bytes.Repeat([]byte{1}, 9000)...)

type seen struct {
	ua, referer, accept string
}

func TestHotlinkRetryLadderAndHostHint(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.UserAgent = "Mozilla/5.0 (compatible; Kipple; +https://rss.example.org)" })
	var mu sync.Mutex
	var log []seen
	needReferer := atomic.Bool{}
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		s := seen{r.Header.Get("User-Agent"), r.Header.Get("Referer"), r.Header.Get("Accept")}
		mu.Lock()
		log = append(log, s)
		mu.Unlock()
		browser := strings.Contains(s.ua, "Chrome")
		ok := browser && (!needReferer.Load() || s.referer == "http://"+r.Host+"/")
		if !ok {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})

	// Step 1: a browser User-Agent without a Referer is enough.
	resp := cr.fetchOrig(up.URL+"/one.png", FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	mu.Lock()
	require.Len(t, log, 2)
	require.Contains(t, log[0].ua, "Kipple")
	require.Empty(t, log[0].referer)
	require.Equal(t, "image/*", log[0].accept)
	require.Contains(t, log[1].ua, "Chrome")
	require.NotContains(t, log[1].ua, "Kipple")
	require.Empty(t, log[1].referer)
	require.Contains(t, log[1].accept, "image/avif")
	mu.Unlock()
	host := strings.TrimPrefix(up.URL, "http://")
	host = host[:strings.LastIndex(host, ":")]
	h, ok := cr.cache.HostHint(host)
	require.True(t, ok)
	require.Equal(t, imgcache.HostHint{UA: "browser", Referer: "none"}, h)

	// The saved hint is used first next time: one request, not two.
	mu.Lock()
	log = nil
	mu.Unlock()
	require.Equal(t, 200, cr.fetchOrig(up.URL+"/two.png", FlagPrivateNet).StatusCode)
	mu.Lock()
	require.Len(t, log, 1)
	require.Contains(t, log[0].ua, "Chrome")
	mu.Unlock()

	// Step 2: this host also wants the site's own origin as the Referer.
	cr.cache.SetHostHint(host, imgcache.HostHint{UA: "kipple", Referer: "none"}) //nolint:errcheck // resets the hint
	needReferer.Store(true)
	mu.Lock()
	log = nil
	mu.Unlock()
	require.Equal(t, 200, cr.fetchOrig(up.URL+"/three.png", FlagPrivateNet).StatusCode)
	mu.Lock()
	require.Len(t, log, 3)
	require.Contains(t, log[0].ua, "Kipple")
	require.Empty(t, log[0].referer)
	require.Contains(t, log[1].ua, "Chrome")
	require.Empty(t, log[1].referer)
	require.Contains(t, log[2].ua, "Chrome")
	require.Equal(t, up.URL+"/", log[2].referer, "the image's own origin, never Kipple's address or an article")
	for _, s := range log {
		require.NotContains(t, s.referer, "rss.example.org")
	}
	mu.Unlock()
	h, _ = cr.cache.HostHint(host)
	require.Equal(t, imgcache.HostHint{UA: "browser", Referer: "self"}, h)
}

func TestNoHotlinkRetryForRateLimitOrChallenge(t *testing.T) {
	cr := newCacheRig(t)
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		switch r.URL.Path {
		case "/limited":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
		case "/cf":
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(403)
		default:
			w.WriteHeader(404)
		}
	})
	for _, p := range []string{"/limited", "/cf", "/other"} {
		before := n.Load()
		require.Equal(t, 502, cr.fetchOrig(up.URL+p, FlagPrivateNet).StatusCode)
		require.Equal(t, before+1, n.Load(), p)
	}
	// A 403 that no shape fixes ends after the whole ladder, once, and is remembered.
	n.Store(0)
	up2 := upstream(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(403) })
	require.Equal(t, 502, cr.fetchOrig(up2.URL+"/x", FlagPrivateNet).StatusCode)
	require.EqualValues(t, 3, n.Load())
	require.Equal(t, 502, cr.fetchOrig(up2.URL+"/x", FlagPrivateNet).StatusCode)
	require.EqualValues(t, 3, n.Load())
}

func TestPerHostConcurrencyLimit(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.Wait = 5 * time.Second })
	var cur, peak atomic.Int32
	release := make(chan struct{})
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		c := cur.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		<-release
		cur.Add(-1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := cr.fetchOrig(up.URL+"/p"+strconv.Itoa(i)+".png", FlagPrivateNet)
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	require.Eventually(t, func() bool { return cur.Load() == 4 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	require.EqualValues(t, 4, cur.Load(), "the fifth waits for a host slot although the global limit is 8")
	close(release)
	wg.Wait()
	require.EqualValues(t, 4, peak.Load())
	require.Eventually(t, func() bool { return cr.h.hosts.active() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestDiskFloorStreamsUncached(t *testing.T) {
	var free atomic.Uint64
	free.Store(500 << 30)
	clk := &testClock{t: time.Now()}
	c, err := imgcache.Open(imgcache.Options{
		Dir: filepath.Join(t.TempDir(), "ic"), MaxBytes: 64 << 20, Now: clk.Now, NoBackgound: true,
		DiskSpace: func(string) (uint64, uint64, error) { return free.Load(), 800 << 30, nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fc := fetch.NewClient(fetch.ClientOptions{})
	h := New(Options{Secret: secret, Cache: c,
		Transport: func(a, i bool) http.RoundTripper { return fc.Transport(a, i, false) }})
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rg := &rig{t: t, srv: srv}

	free.Store(1 << 30) // under the floor
	up := countingImg(t)
	resp := rg.fetchOrig(up.URL+"/f.png", FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp), "the client still gets the image")
	require.Zero(t, c.Stats().Files)
	ents, _ := os.ReadDir(filepath.Join(c.Dir(), "tmp"))
	require.Empty(t, ents)
}

func TestCacheOffStreamsAndForwardsConditionals(t *testing.T) {
	cr := newCacheRig(t)
	var gotINM atomic.Value
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotINM.Store(r.Header.Get("If-None-Match"))
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/o.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	cr.cache.SetCap(0)
	require.Eventually(t, func() bool { return cr.cache.Stats().Files == 0 && !cr.cache.Enabled() }, 5*time.Second, 10*time.Millisecond)
	resp := cr.fetchOrig(orig, FlagPrivateNet, "If-None-Match", `"v1"`)
	require.Equal(t, 304, resp.StatusCode, "with the cache off the client's conditional goes to the source")
	require.Equal(t, `"v1"`, gotINM.Load())
	require.Zero(t, cr.cache.Stats().Files)
}

func TestVanishedFileIsRefetched(t *testing.T) {
	cr := newCacheRig(t)
	up := countingImg(t)
	orig := up.URL + "/v.png"
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	key := imgcache.KeyOrig(FlagPrivateNet, orig)
	require.NoError(t, os.Remove(filepath.Join(cr.cache.Dir(), "v1", key[:2], key)))
	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, pngBytes, read(t, resp))
	require.EqualValues(t, 2, up.n.Load())
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.EqualValues(t, 2, up.n.Load(), "and cached again")
}

func TestCachedResponsesKeepTheGuards(t *testing.T) {
	cr := newCacheRig(t)
	up := countingImg(t)
	orig := up.URL + "/g.png"
	// The signature and the SSRF guard still apply with the cache on: a bad
	// signature never reaches the cache, and an unflagged private address is refused.
	parts := strings.Split(Path(secret, FlagPrivateNet, orig), "/") // "", img, sig, flags, b64
	bad := "/img/AAAAAAAAAAAAAAAAAAAAAA/" + parts[3] + "/" + parts[4]
	require.Equal(t, 403, cr.get(bad).StatusCode)
	require.Equal(t, 502, cr.fetchOrig(orig, 0).StatusCode, "127.0.0.1 without allow_private_net")
	require.EqualValues(t, 0, up.n.Load())
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
}
