package imgproxy

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

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
