package imgproxy

import (
	"bytes"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/imgcache"
)

// stallingPNG answers a PNG whose declared length is never reached: it sends
// the first 600 bytes and then stalls until release is closed or the request ends.
func stallingPNG(n *atomic.Int32, release <-chan struct{}) http.HandlerFunc {
	body := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 592)...)
	return func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
}

// TestStalledBodyIsRemembered: a source that sends its first bytes and then
// stalls until the body budget runs out is remembered as a transient failure
// ("timed out", 10 minutes), so the next request replays a 502 instead of
// holding a fetch slot for the whole timeout again.
func TestStalledBodyIsRemembered(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.Timeout = 300 * time.Millisecond })
	var n atomic.Int32
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	up := upstream(t, stallingPNG(&n, release))
	orig := up.URL + "/a.png"

	tryGet(cr.rig, orig) // cut: before or after its headers, depending on buffering

	var e imgcache.Entry
	require.Eventually(t, func() bool {
		var ok bool
		e, ok = cr.entry(orig)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "the failure is recorded")
	require.False(t, e.OK)
	require.Equal(t, "timed out", e.NegReason)
	require.Equal(t, cr.clk.Now().Add(10*time.Minute).Unix(), e.FreshUntil.Unix(), "the transient back-off")

	resp := cr.fetchOrig(orig, FlagPrivateNet)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.EqualValues(t, 1, n.Load(), "the failure is replayed without contacting the source")
}

// TestStalledRevalidationKeepsStaleCopy: a stale copy whose revalidation gets
// a 200 that then stalls stays in the cache and its next revalidation is put
// off, instead of being retried (and a slot held) on every request.
func TestStalledRevalidationKeepsStaleCopy(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.Timeout = 300 * time.Millisecond })
	var n atomic.Int32
	var stall atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stalling := stallingPNG(&n, release)
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		if stall.Load() {
			stalling(w, r)
			return
		}
		n.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/a.png"
	require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)))
	cr.clk.Advance(8 * 24 * time.Hour) // past the default 7 days
	stall.Store(true)

	tryGet(cr.rig, orig)
	require.EqualValues(t, 2, n.Load())

	require.Eventually(t, func() bool {
		e, ok := cr.entry(orig)
		return ok && e.OK && e.NegCount == 1
	}, 2*time.Second, 10*time.Millisecond, "the revalidation is deferred")
	e, _ := cr.entry(orig)
	require.True(t, e.Fresh(cr.clk.Now()))

	require.Equal(t, pngBytes, read(t, cr.fetchOrig(orig, FlagPrivateNet)), "the stale copy is served")
	require.EqualValues(t, 2, n.Load(), "without contacting the source")
}

// tryGet requests orig and reads whatever comes back; a cut response (an
// error before or after the headers) is expected by the callers.
func tryGet(rg *rig, orig string) {
	req, err := http.NewRequest("GET", rg.srv.URL+Path(secret, FlagPrivateNet, orig), nil)
	require.NoError(rg.t, err)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
}

// TestReleasedExchangeRecordsNoFailure: a body read that fails because Kipple
// closed the exchange itself (here SlotHold running out while the request is
// still alive) is not the source's fault and records nothing.
func TestReleasedExchangeRecordsNoFailure(t *testing.T) {
	cr := newCacheRig(t, func(o *Options) { o.Timeout = 5 * time.Second; o.SlotHold = 200 * time.Millisecond })
	var n atomic.Int32
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	up := upstream(t, stallingPNG(&n, release))
	orig := up.URL + "/a.png"

	start := time.Now()
	tryGet(cr.rig, orig)
	require.Less(t, time.Since(start), 3*time.Second, "SlotHold cut the exchange, not the body budget")
	require.Never(t, func() bool { _, ok := cr.entry(orig); return ok }, 300*time.Millisecond, 20*time.Millisecond)
}

// TestCutBodyIsRemembered: a source that closes the connection mid-body is
// remembered as a transient failure.
func TestCutBodyIsRemembered(t *testing.T) {
	cr := newCacheRig(t)
	var n atomic.Int32
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 1000)...))
		w.(http.Flusher).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	orig := up.URL + "/a.png"
	tryGet(cr.rig, orig)
	require.Eventually(t, func() bool {
		e, ok := cr.entry(orig)
		return ok && !e.OK && e.NegReason == "the body was cut"
	}, 2*time.Second, 10*time.Millisecond)
	tryGet(cr.rig, orig)
	require.EqualValues(t, 1, n.Load())
}

// TestThumbURLWithoutCacheIsNotImmutable: with the cache off a thumbnail URL
// streams the original, and that answer must not be cached as immutable (it
// would pin the original under the thumbnail URL for 30 days); the plain
// original URL keeps its immutable header. A 304 at the thumbnail URL too.
func TestThumbURLWithoutCacheIsNotImmutable(t *testing.T) {
	rg := newRig(t) // no cache
	up := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	orig := up.URL + "/a.png"

	resp := rg.fetchOrig(orig, FlagPrivateNet|FlagThumb)
	_, _ = io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, noCache, resp.Header.Get("Cache-Control"))

	resp = rg.fetchOrig(orig, FlagPrivateNet|FlagThumb, "If-None-Match", `"v1"`)
	require.Equal(t, http.StatusNotModified, resp.StatusCode)
	require.Equal(t, noCache, resp.Header.Get("Cache-Control"))

	resp = rg.fetchOrig(orig, FlagPrivateNet)
	_, _ = io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, cacheControl, resp.Header.Get("Cache-Control"))
}
