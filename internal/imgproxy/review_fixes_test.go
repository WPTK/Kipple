package imgproxy

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
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

// ---- stale revalidation (review items 5, 6) ----

func TestStaleEntryKeptWhenRevalidationIsRefused(t *testing.T) {
	cases := []struct {
		name string
		bad  func(w http.ResponseWriter)
	}{
		{"404", func(w http.ResponseWriter) { w.WriteHeader(404) }},
		{"403 through the whole hotlink ladder", func(w http.ResponseWriter) { w.WriteHeader(403) }},
		{"a soft 404 page", func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>not found</body></html>"))
		}},
		{"503", func(w http.ResponseWriter) { w.WriteHeader(503) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := newCacheRig(t)
			var bad atomic.Bool
			var n atomic.Int32
			up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				if bad.Load() {
					tc.bad(w)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBytes)
			})
			orig := up.URL + "/k.png"
			require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
			cr.clk.Advance(8 * 24 * time.Hour)
			bad.Store(true)

			before := n.Load()
			resp := cr.fetchOrig(orig, FlagPrivateNet)
			require.Equal(t, 200, resp.StatusCode)
			require.Equal(t, pngBytes, read(t, resp), "the stale copy is served")
			attempts := n.Load() - before
			e, ok := cr.entry(orig)
			require.True(t, ok)
			require.True(t, e.OK, "the good copy is kept, not replaced by a failure")
			require.EqualValues(t, 1, cr.cache.Stats().Files)
			require.Zero(t, cr.cache.Stats().Failures)
			require.Equal(t, 10*time.Minute, e.FreshUntil.Sub(cr.clk.Now()), "the next revalidation is put off")

			require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
			require.Equal(t, before+attempts, n.Load(), "not asked again within the back-off")

			cr.clk.Advance(11 * time.Minute)
			require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
			require.Equal(t, before+2*attempts, n.Load())
			e, _ = cr.entry(orig)
			require.Equal(t, 20*time.Minute, e.FreshUntil.Sub(cr.clk.Now()), "doubling")

			// The source recovers: a new copy, and the back-off is gone.
			bad.Store(false)
			cr.clk.Advance(21 * time.Minute)
			require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
			e, _ = cr.entry(orig)
			require.True(t, e.OK)
			require.Zero(t, e.NegCount)
			require.Equal(t, 7*24*time.Hour, e.FreshUntil.Sub(cr.clk.Now()))
		})
	}
}

func TestRevalidated304ButFileEvictedRefetchesOnce(t *testing.T) {
	cr := newCacheRig(t)
	var n, conditional atomic.Int32
	var key string
	dir := cr.cache.Dir()
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			// The LRU takes the stale file while the revalidation is in flight.
			_ = os.Remove(filepath.Join(dir, "v1", key[:2], key))
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/e.png"
	key = imgcache.KeyOrig(FlagPrivateNet, orig)
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	cr.clk.Advance(8 * 24 * time.Hour)

	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, resp.StatusCode, "not a 502")
	require.Equal(t, pngBytes, read(t, resp))
	require.EqualValues(t, 3, n.Load(), "the 304, then one unconditional fetch")
	require.EqualValues(t, 1, conditional.Load())
	e, ok := cr.entry(orig)
	require.True(t, ok)
	require.True(t, e.OK && e.Fresh(cr.clk.Now()), "cached again")
	require.Equal(t, 200, cr.fetchOrig(orig, FlagPrivateNet).StatusCode)
	require.EqualValues(t, 3, n.Load())
}

// ---- validators (review item 8) ----

func TestRelayedAndCachedResponsesShareValidators(t *testing.T) {
	cr := newCacheRig(t)
	const lm = "Wed, 01 Jan 2025 00:00:00 GMT"
	up := countingImg(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"up"`)
		w.Header().Set("Last-Modified", lm)
	})
	orig := up.URL + "/v.png"
	first := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, 200, first.StatusCode)
	require.Empty(t, first.Header.Get("ETag"), "the source's tag is never sent: hits could not match it")
	require.Equal(t, lm, first.Header.Get("Last-Modified"))

	e, _ := cr.entry(orig)
	hit := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, `"`+e.SHA256[:16]+`"`, hit.Header.Get("ETag"))
	require.Equal(t, lm, hit.Header.Get("Last-Modified"))

	// What the first response gave the browser revalidates against the cache.
	require.Equal(t, 304, cr.fetchOrig(orig, FlagPrivateNet, "If-Modified-Since", first.Header.Get("Last-Modified")).StatusCode)
	require.Equal(t, 304, cr.fetchOrig(orig, FlagPrivateNet, "If-None-Match", hit.Header.Get("ETag")).StatusCode)
	require.EqualValues(t, 1, up.n.Load())
}

// ---- slow clients (review item 3) ----

// slowWriter is a client that takes delay for every write, like a phone on
// a slow link (the kernel buffers of a loopback socket would hide that).
type slowWriter struct {
	h       http.Header
	code    int
	buf     bytes.Buffer
	delay   time.Duration
	writes  int
	onWrite func(n int)
}

func (s *slowWriter) Header() http.Header {
	if s.h == nil {
		s.h = http.Header{}
	}
	return s.h
}
func (s *slowWriter) WriteHeader(c int) {
	if s.code == 0 {
		s.code = c
	}
}
func (s *slowWriter) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = 200
	}
	time.Sleep(s.delay)
	s.writes++
	if s.onWrite != nil {
		s.onWrite(s.writes)
	}
	return s.buf.Write(p)
}

// serveSlow runs one request through h into a slow writer; an aborted
// response (the handler's ErrAbortHandler panic) is reported as aborted.
func serveSlow(h http.Handler, path string, sw *slowWriter) (aborted bool) {
	mux := http.NewServeMux()
	mux.Handle("GET /img/{sig}/{flags}/{u}", h)
	defer func() {
		if v := recover(); v != nil {
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				aborted = true
				return
			}
			panic(v)
		}
	}()
	mux.ServeHTTP(sw, httptest.NewRequest("GET", path, nil))
	return false
}

func randomPNG(n int) []byte {
	rng := rand.New(rand.NewPCG(5, 6))
	b := append([]byte{}, pngBytes[:8]...)
	for len(b) < n {
		b = append(b, byte(rng.Uint32()))
	}
	return b
}

func TestSlowClientDoesNotTimeOutAFastSource(t *testing.T) {
	body := randomPNG(1 << 20) // 32 writes of 32 KiB
	up := upstream(t, serve("image/png", body))
	const timeout = 300 * time.Millisecond
	const delay = 25 * time.Millisecond // 32 writes: about 0.8 s, well past the timeout

	t.Run("cached", func(t *testing.T) {
		cr := newCacheRig(t, func(o *Options) { o.Timeout = timeout })
		orig := up.URL + "/slow-client.png"
		var cachedEarly atomic.Bool
		sw := &slowWriter{delay: delay, onWrite: func(n int) {
			if n == 24 { // 0.6 s in, with 8 writes still to go
				if e, ok := cr.entry(orig); ok && e.OK {
					cachedEarly.Store(true)
				}
			}
		}}
		start := time.Now()
		require.False(t, serveSlow(cr.h, Path(secret, FlagPrivateNet, orig), sw), "the response was not cut")
		require.Greater(t, time.Since(start), timeout, "the client really was slower than the timeout")
		require.Equal(t, 200, sw.code)
		require.Equal(t, body, sw.buf.Bytes())
		e, ok := cr.entry(orig)
		require.True(t, ok && e.OK)
		require.Equal(t, int64(len(body)), e.Size)
		require.True(t, cachedEarly.Load(), "committed at the source's speed, while the client was still reading")
	})

	t.Run("cache off", func(t *testing.T) {
		fc := fetch.NewClient(fetch.ClientOptions{})
		h := New(Options{Secret: secret, Timeout: timeout,
			Transport: func(a, i bool) http.RoundTripper { return fc.Transport(a, i, false) }})
		sw := &slowWriter{delay: delay}
		require.False(t, serveSlow(h, Path(secret, FlagPrivateNet, up.URL+"/direct.png"), sw), "client time does not count against the source")
		require.Equal(t, body, sw.buf.Bytes())
	})
}

func TestTricklingSourceIsStillCut(t *testing.T) {
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(bigPNG)*20))
		for i := 0; i < 20; i++ {
			if _, err := w.Write(bigPNG); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(100 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	})
	for _, cached := range []bool{true, false} {
		t.Run(strconv.FormatBool(cached), func(t *testing.T) {
			cr := newCacheRig(t, func(o *Options) { o.Timeout = 300 * time.Millisecond })
			if !cached {
				cr.cache.SetCap(0)
				require.Eventually(t, func() bool { return !cr.cache.Enabled() }, 5*time.Second, 5*time.Millisecond)
			}
			orig := up.URL + "/trickle.png"
			start := time.Now()
			resp := cr.fetchOrig(orig, FlagPrivateNet)
			_, err := io.ReadAll(resp.Body)
			require.Error(t, err, "a source slower than the timeout is cut")
			require.Less(t, time.Since(start), 1500*time.Millisecond)
			_, ok := cr.entry(orig)
			require.False(t, ok)
		})
	}
}

// ---- thumbnails: one fetch when the original cannot be cached (review item 4) ----

type lowDiskThumbRig struct {
	*rig
	cache *imgcache.Cache
	h     *Handler
	free  *atomic.Uint64
}

func newLowDiskThumbRig(t *testing.T, tune ...func(*Options)) *lowDiskThumbRig {
	t.Helper()
	free := &atomic.Uint64{}
	free.Store(500 << 30)
	c, err := imgcache.Open(imgcache.Options{
		Dir: filepath.Join(t.TempDir(), "ic"), MaxBytes: 64 << 20, NoBackgound: true,
		DiskSpace: func(string) (uint64, uint64, error) { return free.Load(), 800 << 30, nil },
	})
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
	return &lowDiskThumbRig{rig: &rig{t: t, srv: srv}, cache: c, h: h, free: free}
}

func TestThumbLowDiskStreamsTheOriginalWithOneFetch(t *testing.T) {
	tr := newLowDiskThumbRig(t)
	tr.free.Store(1 << 30) // under the floor: nothing can be cached
	src := sampleJPEG(t)
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(src)
	})
	resp := tr.get(Path(secret, FlagPrivateNet|FlagThumb, up.URL+"/low.jpg"))
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, src, read(t, resp), "the original, streamed")
	require.EqualValues(t, 1, n.Load(), "fetched once, not once for the cache attempt and again for the answer")
	require.Zero(t, tr.cache.Stats().Files)
}

func TestThumbCommitFailureServesTheFetchedOriginal(t *testing.T) {
	tr := newLowDiskThumbRig(t)
	src := sampleJPEG(t)
	gate := make(chan struct{})
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(src)))
		_, _ = w.Write(src[:len(src)/2])
		w.(http.Flusher).Flush()
		<-gate
		_, _ = w.Write(src[len(src)/2:])
	})
	done := make(chan *http.Response, 1)
	go func() { done <- tr.get(Path(secret, FlagPrivateNet|FlagThumb, up.URL+"/cf.jpg")) }()
	require.Eventually(t, func() bool { return n.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	tr.cache.SetCap(0) // the commit at the end of the body will be refused
	require.Eventually(t, func() bool { return !tr.cache.Enabled() }, 5*time.Second, 5*time.Millisecond)
	close(gate)
	resp := <-done
	require.Equal(t, 200, resp.StatusCode)
	require.Equal(t, src, read(t, resp), "the bytes already in the download file go to the client")
	require.EqualValues(t, 1, n.Load())
}

func TestThumbNoFetchSlotWaitsOnce(t *testing.T) {
	const wait = 500 * time.Millisecond
	tr := newLowDiskThumbRig(t, func(o *Options) { o.Concurrency = 1; o.Wait = wait })
	hold := make(chan struct{})
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path == "/hold.png" {
			<-hold
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	t.Cleanup(func() { close(hold) }) // runs before the servers close, even when an assertion fails
	go func() {
		resp, err := http.Get(tr.srv.URL + Path(secret, FlagPrivateNet, up.URL+"/hold.png"))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	require.Eventually(t, func() bool { return n.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	start := time.Now()
	resp := tr.get(Path(secret, FlagPrivateNet|FlagThumb, up.URL+"/busy.jpg"))
	took := time.Since(start)
	require.Equal(t, 503, resp.StatusCode)
	require.Equal(t, "5", resp.Header.Get("Retry-After"))
	require.Less(t, took, 2*wait-100*time.Millisecond, "one queue wait, not two")
	require.EqualValues(t, 1, n.Load())
}

// ---- thumbnails: flight and crash safety (review items 1c, 9) ----

// setHook installs a test hook until the test ends.
func setHook(t *testing.T, p *atomic.Pointer[func()], f func()) {
	t.Helper()
	p.Store(&f)
	t.Cleanup(func() { p.Store(nil) })
}

func TestThumbLeaderPanicReleasesTheFlight(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t), func(o *Options) { o.ThumbWait = 20 * time.Second })
	var once atomic.Bool
	setHook(t, &testHookThumbLeader, func() {
		if once.CompareAndSwap(false, true) {
			panic("injected")
		}
	})
	req, _ := http.NewRequest("GET", tr.srv.URL+Path(secret, FlagPrivateNet|FlagThumb, tr.up.URL+"/p.jpg"), nil)
	if resp, err := http.DefaultTransport.RoundTrip(req); err == nil {
		_ = resp.Body.Close()
		require.NotEqual(t, 200, resp.StatusCode)
	}
	require.True(t, once.Load())

	body := read(t, tr.thumb("p.jpg"))
	cfg, _ := decodeCfg(t, body)
	require.Equal(t, ThumbWidth, cfg.Width, "the next request leads and makes the thumbnail (a leaked flight would make it wait out ThumbWait and get the original)")
}

func TestThumbInProgressMarkerDuringTranscode(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	setHook(t, &testHookBeforeDecode, func() {
		entered <- struct{}{}
		<-gate
	})
	bodies := make([][]byte, 2)
	var wg sync.WaitGroup
	get := func(i int) {
		defer wg.Done()
		req, _ := http.NewRequest("GET", tr.srv.URL+Path(secret, FlagPrivateNet|FlagThumb, tr.up.URL+"/m.jpg"), nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			bodies[i], _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
	}
	wg.Add(1)
	go get(0)
	<-entered
	te, ok := tr.thumbEntry("m.jpg")
	require.True(t, ok)
	require.False(t, te.OK)
	require.Equal(t, imgcache.InProgress, te.NegReason, "the marker is written before the decode")
	require.Zero(t, tr.cache.Stats().Failures, "a marker is not a failure")
	wg.Add(1)
	go get(1) // sees the marker, waits on the running flight
	time.Sleep(150 * time.Millisecond)
	close(gate)
	wg.Wait()
	for _, b := range bodies {
		cfg, _ := decodeCfg(t, b)
		require.Equal(t, ThumbWidth, cfg.Width)
	}
	te, _ = tr.thumbEntry("m.jpg")
	require.True(t, te.OK, "the thumbnail replaced the marker")
}

func TestThumbMarkerLeftByACrashIsHonored(t *testing.T) {
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t))
	orig := tr.up.URL + "/c.jpg"
	// What a process killed in the middle of the decode leaves behind.
	require.NoError(t, tr.cache.PutInProgress(imgcache.KeyThumb(FlagPrivateNet, orig), orig, FlagPrivateNet, imgcache.VariantThumb))
	var decodes atomic.Int32
	setHook(t, &testHookBeforeDecode, func() { decodes.Add(1) })

	require.Equal(t, tr.src, read(t, tr.thumb("c.jpg")), "the original, no decode")
	require.Equal(t, tr.src, read(t, tr.thumb("c.jpg")))
	require.Zero(t, decodes.Load())

	tr.clk.Advance(11 * time.Minute)
	cfg, _ := decodeCfg(t, read(t, tr.thumb("c.jpg")))
	require.Equal(t, ThumbWidth, cfg.Width, "tried again after the marker expired")
	require.EqualValues(t, 1, decodes.Load())
}

func TestThumbMarkerIsWrittenBeforeADecodeThatNeverEnds(t *testing.T) {
	// A decode that takes the process down never replaces its marker: the
	// next start (a fresh flight table) finds it and serves the original.
	tr := newThumbRig(t, "image/jpeg", sampleJPEG(t), func(o *Options) { o.ThumbWait = 200 * time.Millisecond })
	block := make(chan struct{})
	setHook(t, &testHookBeforeDecode, func() { <-block })
	t.Cleanup(func() { close(block) }) // runs first: the worker finishes before the handler closes
	require.Equal(t, tr.src, read(t, tr.thumb("k.jpg")), "the wait ends: the original")
	te, ok := tr.thumbEntry("k.jpg")
	require.True(t, ok)
	require.Equal(t, imgcache.InProgress, te.NegReason)
	require.Equal(t, 10*time.Minute, te.FreshUntil.Sub(tr.clk.Now()))
}
