package imgproxy

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/imgcache"
)

// fullRig is a proxy over a cache with a test clock, a free-space gauge and
// cache options of the test's choosing.
type fullRig struct {
	*rig
	cache *imgcache.Cache
	clk   *testClock
	free  *atomic.Uint64
	h     *Handler
}

func newFullRig(t *testing.T, co func(*imgcache.Options), tune ...func(*Options)) *fullRig {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	free := &atomic.Uint64{}
	free.Store(500 << 30)
	o := imgcache.Options{
		Dir: filepath.Join(t.TempDir(), "ic"), MaxBytes: 64 << 20, Now: clk.Now, NoBackground: true,
		DiskSpace: func(string) (uint64, uint64, error) { return free.Load(), 800 << 30, nil },
	}
	if co != nil {
		co(&o)
	}
	c, err := imgcache.Open(o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	fc := fetch.NewClient(fetch.ClientOptions{})
	opt := Options{Secret: secret, Cache: c, ThumbWait: 60 * time.Second,
		Transport: func(a, i bool) http.RoundTripper { return fc.Transport(a, i, false) }}
	for _, f := range tune {
		f(&opt)
	}
	h := New(opt)
	t.Cleanup(h.Close)
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fullRig{rig: &rig{t: t, srv: srv}, cache: c, clk: clk, free: free, h: h}
}

func (fr *fullRig) entry(orig string) (imgcache.Entry, bool) {
	return fr.cache.Peek(context.Background(), imgcache.KeyOrig(FlagPrivateNet, orig))
}

// ---- review item 4: what a thumbnail URL may be cached as ----

func TestThumbURLCacheHeadersPerPath(t *testing.T) {
	const immutable = "private, max-age=2592000, immutable"
	t.Run("the thumbnail itself is immutable", func(t *testing.T) {
		tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
		resp := tr.thumb("a.jpg")
		cfg, _ := decodeCfg(t, read(t, resp))
		require.Equal(t, ThumbWidth, cfg.Width)
		require.Equal(t, immutable, resp.Header.Get("Cache-Control"))
		require.Equal(t, immutable, tr.thumb("a.jpg").Header.Get("Cache-Control"), "and so is its hit")
	})
	t.Run("a refused image is cached as long as the refusal", func(t *testing.T) {
		gifBig := append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 300<<10)...)
		tr := newThumbRig(t, "image/gif", gifBig)
		resp := tr.thumb("g.gif")
		require.Equal(t, gifBig, read(t, resp))
		require.Equal(t, "private, max-age=86400", resp.Header.Get("Cache-Control"))
		tr.clk.Advance(time.Hour)
		resp = tr.thumb("g.gif")
		require.Equal(t, gifBig, read(t, resp))
		require.Equal(t, "private, max-age=82800", resp.Header.Get("Cache-Control"), "what is left of the refusal")
		require.Equal(t, immutable, tr.orig("g.gif").Header.Get("Cache-Control"), "the original's own URL is unchanged")
	})
	t.Run("load: queue full and a timed-out wait are not cached", func(t *testing.T) {
		tr := newThumbRig(t, "image/jpeg", sampleJPEG(t), func(o *Options) {
			o.ThumbWorkers, o.ThumbQueue, o.ThumbWait = 1, 1, 300*time.Millisecond
		})
		tr.h.lim.budget.acquire(defaultDecodeBudget) // the worker blocks inside its first job
		t.Cleanup(func() { tr.h.lim.budget.release(defaultDecodeBudget) })
		for _, name := range []string{"1.jpg", "2.jpg", "3.jpg"} { // waits, waits, queue full
			resp := tr.thumb(name)
			require.Equal(t, tr.src, read(t, resp))
			require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"), name)
		}
		require.Equal(t, immutable, tr.orig("1.jpg").Header.Get("Cache-Control"))
	})
	t.Run("a marker left by a crash is not cached", func(t *testing.T) {
		tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
		orig := tr.up.URL + "/c.jpg"
		require.NoError(t, tr.cache.PutInProgress(imgcache.KeyThumb(FlagPrivateNet, orig), orig, FlagPrivateNet, imgcache.VariantThumb))
		resp := tr.thumb("c.jpg")
		require.Equal(t, tr.src, read(t, resp))
		require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	})
	t.Run("low disk: the forwarded original is not cached", func(t *testing.T) {
		tr := newLowDiskThumbRig(t)
		tr.free.Store(1 << 30)
		src := sampleJPEG(t)
		up := upstream(t, serve("image/jpeg", src))
		resp := tr.get(Path(secret, FlagPrivateNet|FlagThumb, up.URL+"/low.jpg"))
		require.Equal(t, src, read(t, resp))
		require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	})
}

// ---- review item 5: an oversize chunked body during revalidation ----

func TestOversizeChunkedRevalidationKeepsTheStaleCopy(t *testing.T) {
	for _, lowDisk := range []bool{false, true} { // the fill path, and the pump path (the cache refuses the write)
		t.Run("low disk "+strconv.FormatBool(lowDisk), func(t *testing.T) {
			fr := newFullRig(t, nil, func(o *Options) { o.MaxBytes = 64 << 10 })
			var big atomic.Bool
			var n atomic.Int32
			up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.Header().Set("Content-Type", "image/png")
				if !big.Load() {
					_, _ = w.Write(pngBytes)
					return
				}
				// No Content-Length: the size is only found out mid-body.
				for i := 0; i < 8; i++ {
					if _, err := w.Write(randomPNG(16 << 10)); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			})
			orig := up.URL + "/big.png"
			require.Equal(t, pngBytes, read(t, fr.fetchOrig(orig, FlagPrivateNet)))
			fr.clk.Advance(8 * 24 * time.Hour)
			big.Store(true)
			if lowDisk {
				fr.free.Store(1 << 30)
			}
			resp := fr.fetchOrig(orig, FlagPrivateNet)
			_, err := io.ReadAll(resp.Body)
			require.Error(t, err, "the oversize body is cut")
			e, ok := fr.entry(orig)
			require.True(t, ok)
			require.True(t, e.OK, "the good copy is kept, not replaced by a failure entry")
			require.Equal(t, 10*time.Minute, e.FreshUntil.Sub(fr.clk.Now()), "the next revalidation is put off")
			require.EqualValues(t, 1, fr.cache.Stats().Files)
			before := n.Load()
			require.Equal(t, pngBytes, read(t, fr.fetchOrig(orig, FlagPrivateNet)), "the stale copy is served")
			require.Equal(t, before, n.Load(), "without asking the source within the back-off")
		})
	}
}

// ---- review item 6: no marker, no decode ----

func TestThumbWithoutAMarkerIsNotDecoded(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	// The index refuses exactly the marker write, as a full disk would.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(tr.cache.Dir(), "index.db")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TRIGGER no_marker BEFORE INSERT ON entries WHEN NEW.neg_reason = 'in progress'
		BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)
	require.NoError(t, err)
	var decodes atomic.Int32
	setHook(t, &testHookBeforeDecode, func() { decodes.Add(1) })

	resp := tr.thumb("n.jpg")
	require.Equal(t, 200, resp.StatusCode)
	require.True(t, bytes.Equal(tr.src, read(t, resp)), "the original")
	require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	require.Zero(t, decodes.Load(), "a decode that could crash the process is not started without its marker")
	_, ok := tr.thumbEntry("n.jpg")
	require.False(t, ok)
	require.EqualValues(t, 1, tr.up.n.Load())
}

// ---- review item 8: slow clients cannot hold the fetch slots ----

func TestSlowClientsReleaseTheirFetchSlots(t *testing.T) {
	body := randomPNG(1 << 20) // 32 writes of 32 KiB
	up := upstream(t, serve("image/png", body))
	fc := fetch.NewClient(fetch.ClientOptions{})
	h := New(Options{Secret: secret, Timeout: 300 * time.Millisecond, SlotHold: 600 * time.Millisecond, Wait: 3 * time.Second, PerHost: defaultConc,
		Transport: func(a, i bool) http.RoundTripper { return fc.Transport(a, i, false) }})
	t.Cleanup(h.Close)
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var wg sync.WaitGroup
	var aborted atomic.Int32
	for i := 0; i < defaultConc; i++ { // every global slot, each held by a client reading at about 300 KB/s
		wg.Add(1)
		go func() {
			defer wg.Done()
			sw := &slowWriter{delay: 100 * time.Millisecond}
			if serveSlow(h, Path(secret, FlagPrivateNet, up.URL+"/slow-"+strconv.Itoa(i)+".png"), sw) {
				aborted.Add(1)
			}
		}()
	}
	require.Eventually(t, func() bool { return len(h.sem) == defaultConc }, 5*time.Second, time.Millisecond)
	start := time.Now()
	resp, err := http.Get(srv.URL + Path(secret, FlagPrivateNet, up.URL+"/fast.png"))
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode, "a slot came free before the queue wait ran out")
	require.Equal(t, body, got)
	require.Less(t, time.Since(start), 2*time.Second)
	wg.Wait()
	require.EqualValues(t, defaultConc, aborted.Load(), "the slow responses were cut when their slots were taken back")
	require.Zero(t, len(h.sem))
}

// ---- test gaps ----

// A cache write that fails mid-body (here: past the per-object limit) hands
// the rest of the body to the client loop, which must not lose the chunk
// that failed to reach the file.
func TestMidBodyCacheWriteFailureServesTheWholeBody(t *testing.T) {
	body := randomPNG(1 << 20)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		for off := 0; off < len(body); off += 48 << 10 { // chunked: the cache cannot refuse it up front
			_, _ = w.Write(body[off:min(off+48<<10, len(body))])
			w.(http.Flusher).Flush()
		}
	})
	for _, slow := range []bool{false, true} {
		t.Run("slow client "+strconv.FormatBool(slow), func(t *testing.T) {
			fr := newFullRig(t, func(o *imgcache.Options) { o.MaxObject = 100 << 10 })
			orig := up.URL + "/handoff.png"
			if slow {
				sw := &slowWriter{delay: 5 * time.Millisecond}
				require.False(t, serveSlow(fr.h, Path(secret, FlagPrivateNet, orig), sw))
				require.Equal(t, body, sw.buf.Bytes(), "complete and uncorrupted")
			} else {
				require.Equal(t, body, read(t, fr.fetchOrig(orig, FlagPrivateNet)), "complete and uncorrupted")
			}
			_, ok := fr.entry(orig)
			require.False(t, ok, "nothing cached")
			require.Zero(t, fr.cache.Stats().Files)
		})
	}
}

// Followers of a fill are released as soon as it is committed, while the
// leader's slow client is still reading (the flight must not wait for it).
func TestFollowersAreReleasedAtTheCommit(t *testing.T) {
	body := randomPNG(1 << 20)
	half := make(chan struct{})
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		close(half)
		<-gate
		_, _ = w.Write(body[len(body)/2:])
	})
	t.Cleanup(release)
	fr := newFullRig(t, nil, func(o *Options) { o.Timeout = 5 * time.Second })
	orig := up.URL + "/followers.png"
	leaderDone := make(chan time.Time, 1)
	go func() {
		sw := &slowWriter{delay: 100 * time.Millisecond} // about 3.2 s for the whole body
		serveSlow(fr.h, Path(secret, FlagPrivateNet, orig), sw)
		leaderDone <- time.Now()
	}()
	select {
	case <-half:
	case <-time.After(10 * time.Second):
		t.Fatal("the leader never fetched")
	}
	followerDone := make(chan time.Time, 1)
	var got []byte
	go func() {
		resp, err := http.Get(fr.srv.URL + Path(secret, FlagPrivateNet, orig))
		if err == nil {
			got, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		followerDone <- time.Now()
	}()
	time.Sleep(150 * time.Millisecond) // the follower is waiting on the leader's flight
	opened := time.Now()
	release()
	var fDone time.Time
	select {
	case fDone = <-followerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the follower never finished")
	}
	lDone := <-leaderDone
	require.Equal(t, body, got)
	require.Less(t, fDone.Sub(opened), 1500*time.Millisecond, "released at the commit")
	require.True(t, fDone.Before(lDone), "while the leader's slow client was still reading")
}

func TestDecodeBudgetIsReturnedEvenAfterAPanic(t *testing.T) {
	free := func(b *budget) int64 {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.free
	}
	lim := testLimits()
	src := sampleJPEG(t)
	_, _, err := transcode(bytes.NewReader(src), int64(len(src)), "image/jpeg", lim)
	require.NoError(t, err)
	require.EqualValues(t, defaultDecodeBudget, free(lim.budget))

	setHook(t, &testHookBeforeDecode, func() { panic("the decoder blew up") })
	require.Panics(t, func() { _, _, _ = transcode(bytes.NewReader(src), int64(len(src)), "image/jpeg", lim) })
	require.EqualValues(t, defaultDecodeBudget, free(lim.budget), "released by the deferred release")

	// Through the handler: the worker recovers, remembers the refusal, and the budget is whole.
	tr := newThumbRig(t, "image/jpeg", src)
	require.Equal(t, src, read(t, tr.thumb("p.jpg")))
	require.EqualValues(t, defaultDecodeBudget, free(tr.h.lim.budget))
	te, ok := tr.thumbEntry("p.jpg")
	require.True(t, ok)
	require.False(t, te.OK)
}
