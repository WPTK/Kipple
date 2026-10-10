package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/httpx"
	"github.com/WPTK/kipple/internal/imgcache"
	"github.com/WPTK/kipple/internal/imgproxy"
)

// cacheHarness is a harness whose image proxy has a real cache in <data>/imgcache.
func cacheHarness(t *testing.T, maxMB int64) (*harness, *imgcache.Cache) {
	t.Helper()
	var c *imgcache.Cache
	h := newHarness(t, func(o *Options) {
		var err error
		c, err = imgcache.Open(imgcache.Options{
			Dir:      filepath.Join(filepath.Dir(o.DB.BackupDir()), "imgcache"),
			MaxBytes: maxMB << 20, NoBackground: true,
			DiskSpace: func(string) (uint64, uint64, error) { return 500 << 30, 800 << 30, nil },
		})
		require.NoError(t, err)
		o.ImgCache = c
	})
	t.Cleanup(func() { _ = c.Close() })
	return h, c
}

func pngUpstream(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	n := new(int)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*n++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(up.Close)
	return up, n
}

func TestImgcacheEndpointsNeedSessionAndOrigin(t *testing.T) {
	t.Parallel()
	h, _ := cacheHarness(t, 64)
	require.Equal(t, 401, h.do("GET", "/api/imgcache", "").Code)
	require.Equal(t, 401, h.do("POST", "/api/imgcache/clear", "").Code)
	c := h.login()
	rec := h.do("POST", "/api/imgcache/clear", "", withCookie(c), func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "https://evil.example")
	})
	require.Equal(t, 403, rec.Code)
	rec = h.do("POST", "/api/imgcache/clear", "", withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Equal(t, 403, rec.Code)
}

func TestImgcacheStatsClearAndProxyFill(t *testing.T) {
	t.Parallel()
	h, cache := cacheHarness(t, 64)
	c := h.login()
	up, upstreamHits := pngUpstream(t)

	code, st, _ := h.api(c, "GET", "/api/imgcache", "")
	require.Equal(t, 200, code)
	require.Equal(t, true, st["enabled"])
	require.Equal(t, "all", st["mode"])
	require.EqualValues(t, 1024, st["cache_mb"])
	require.EqualValues(t, 64<<20, st["max_bytes"])
	require.EqualValues(t, 0, st["used_bytes"])
	require.EqualValues(t, 0, st["entries"])
	require.Nil(t, st["oldest_access_at"])
	for _, k := range []string{"neg_entries", "hits", "misses", "evictions", "failures", "since", "disk_free_bytes", "disk_floor_bytes", "low_disk"} {
		require.Contains(t, st, k)
	}
	require.EqualValues(t, 500<<30, st["disk_free_bytes"])

	path := imgproxy.Path([]byte(testSecret), imgproxy.FlagPrivateNet, up.URL+"/a.png")
	for i := 0; i < 3; i++ {
		rec := h.do("GET", path, "", withCookie(c))
		require.Equal(t, 200, rec.Code)
		require.Equal(t, testPNG, rec.Body.Bytes())
	}
	require.Equal(t, 1, *upstreamHits, "the second and third requests came from the cache")
	_, st, _ = h.api(c, "GET", "/api/imgcache", "")
	require.EqualValues(t, len(testPNG), st["used_bytes"])
	require.EqualValues(t, 1, st["entries"])
	require.EqualValues(t, 2, st["hits"])
	require.EqualValues(t, 1, st["misses"])
	require.NotNil(t, st["oldest_access_at"])

	// Health reports the size of the cache directory (index included).
	_, health, _ := h.api(c, "GET", "/api/health/feeds", "")
	require.Greater(t, health["db"].(map[string]any)["imgcache_bytes"], float64(len(testPNG)))

	code, out, _ := h.api(c, "POST", "/api/imgcache/clear", "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 1, out["cleared"])
	require.Zero(t, cache.Stats().Files)
	require.Equal(t, 200, h.do("GET", path, "", withCookie(c)).Code)
	require.Equal(t, 2, *upstreamHits, "cleared, so fetched again")
}

func TestImgcacheSettingsValidateAndApply(t *testing.T) {
	t.Parallel()
	h, cache := cacheHarness(t, 1024)
	c := h.login()
	for _, bad := range []string{`"big"`, `63`, `20481`, `-1`, `100.5`} {
		code, out, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.cache_mb":`+bad+`}`)
		require.Equal(t, 400, code, bad)
		require.Equal(t, "invalid_settings", out["error"], bad)
	}
	for _, good := range []string{"64", "20480", "0", "2048"} {
		code, out, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.cache_mb":`+good+`}`)
		require.Equal(t, 200, code, good)
		require.EqualValues(t, mustNum(good), vals(out)["imgproxy.cache_mb"])
	}
	require.EqualValues(t, 2048<<20, cache.MaxBytes(), "the PATCH reached the running cache")

	up, upstreamHits := pngUpstream(t)
	path := imgproxy.Path([]byte(testSecret), imgproxy.FlagPrivateNet, up.URL+"/z.png")
	require.Equal(t, 200, h.do("GET", path, "", withCookie(c)).Code)
	require.EqualValues(t, 1, cache.Stats().Files)

	// Turning the cache off purges it and the proxy streams as before.
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.cache_mb":0}`)
	require.Equal(t, 200, code)
	require.Eventually(t, func() bool { return cache.Stats().Files == 0 }, 5*time.Second, 10*time.Millisecond)
	_, st, _ := h.api(c, "GET", "/api/imgcache", "")
	require.Equal(t, false, st["enabled"])
	require.Equal(t, 200, h.do("GET", path, "", withCookie(c)).Code)
	require.Equal(t, 200, h.do("GET", path, "", withCookie(c)).Code)
	require.Equal(t, 3, *upstreamHits, "uncached: every request reaches the source")
	require.Zero(t, cache.Stats().Files)

	// null resets to the 1024 MB default.
	code, out, _ := h.api(c, "PATCH", "/api/settings", `{"imgproxy.cache_mb":null}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 1024, vals(out)["imgproxy.cache_mb"])
	require.EqualValues(t, 1024<<20, cache.MaxBytes())
}

func mustNum(s string) float64 {
	var f float64
	for _, r := range s {
		f = f*10 + float64(r-'0')
	}
	return f
}

func TestImgcacheWithoutCacheReportsDisabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	code, st, _ := h.api(c, "GET", "/api/imgcache", "")
	require.Equal(t, 200, code)
	require.Equal(t, false, st["enabled"])
	code, out, _ := h.api(c, "POST", "/api/imgcache/clear", "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, out["cleared"])
}

func TestImageSettingsMetadata(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	_, body, _ := h.api(c, "GET", "/api/settings", "")
	byKey := map[string]map[string]any{}
	for _, s := range body["settings"].([]any) {
		m := s.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	size := byKey["imgproxy.cache_mb"]
	require.Equal(t, "Image cache size", size["label"])
	require.Equal(t, "images", size["group"])
	require.Equal(t, "settings", size["surface"])
	require.Equal(t, "MB", size["unit"])
	require.Equal(t, "int", size["kind"])
	require.EqualValues(t, 0, size["min"])
	require.EqualValues(t, 20480, size["max"])
	require.EqualValues(t, 1024, size["value"])
	require.EqualValues(t, 1024, size["default"])
	require.True(t, strings.HasPrefix(size["description"].(string),
		"Images are stored on the server so they load fast and sites can't track you. The oldest are removed when the cache is full."))
	mode := byKey["imgproxy.mode"]
	require.Equal(t, "Load images through Kipple", mode["label"])
	require.Equal(t, "images", mode["group"])
	require.Equal(t, "settings", mode["surface"])
	require.Equal(t, "all", mode["value"])
	require.Equal(t, "all", mode["default"])
}

func TestDefaultModeNeedsNoHTTPSInImgSrc(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	imgSrc := func() string {
		for _, d := range strings.Split(httpx.PageCSP(h.srv.ImgMode(), true), "; ") {
			if strings.HasPrefix(d, "img-src") {
				return d
			}
		}
		return ""
	}
	require.Equal(t, "img-src 'self' data: blob:", imgSrc(), "the default mode is all: every image is same-origin")
	// A deployment that keeps http_only still needs https: images from their hosts.
	h.api(c, "PATCH", "/api/settings", `{"imgproxy.mode":"http_only"}`)
	require.Contains(t, imgSrc(), "https:")
}

func TestHealthImgcacheBytesComeFromTheCacheNotAWalk(t *testing.T) {
	t.Parallel()
	h, cache := cacheHarness(t, 64)
	c := h.login()
	w, err := cache.Begin(imgcache.KeyOrig(0, "http://img.example/a.png"), "http://img.example/a.png", 0, int64(len(testPNG)))
	require.NoError(t, err)
	_, err = w.Write(testPNG)
	require.NoError(t, err)
	require.NoError(t, w.Commit(imgcache.Meta{ContentType: "image/png"}))
	// A file the cache does not know about (a stray in the directory) is not
	// counted: the size is the cache's own counter plus its index, not a walk.
	require.NoError(t, os.WriteFile(filepath.Join(cache.Dir(), "stray.bin"), make([]byte, 1<<20), 0o600))

	_, health, _ := h.api(c, "GET", "/api/health/feeds", "")
	got := int64(health["db"].(map[string]any)["imgcache_bytes"].(float64))
	require.Equal(t, cache.DiskBytes(), got)
	require.Greater(t, got, int64(len(testPNG)), "the index counts too")
	require.Less(t, got, int64(1<<20), "the stray file was not walked into the total")
}
