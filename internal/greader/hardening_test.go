package greader

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func allocDelta(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestPairFloodRejectedCheaply(t *testing.T) {
	h := newHarness(t)
	flood := strings.Repeat("a&", 2<<20) // 4 MiB

	// Unauthenticated: 401 without reading the large body.
	var w *httptest.ResponseRecorder
	d := allocDelta(func() {
		w = h.do(http.MethodPost, base+rd+"edit-tag", flood, map[string]string{"Authorization": ""})
	})
	require.Contains(t, []int{400, 401}, w.Code)
	require.Less(t, d, uint64(4<<20))

	// Authenticated: 400 before any pair is allocated.
	d = allocDelta(func() { w = h.post(rd+"edit-tag", flood) })
	require.Equal(t, 400, w.Code)
	require.Less(t, d, uint64(24<<20), "no per-pair allocation beyond the body itself")

	// ClientLogin body is capped at 64 KiB: the flood is truncated, then the
	// pair count check (or a failed login) answers cheaply.
	d = allocDelta(func() {
		w = h.do(http.MethodPost, base+"/accounts/ClientLogin", flood, map[string]string{"Authorization": ""})
	})
	require.Contains(t, []int{400, 401}, w.Code)
	require.Less(t, d, uint64(2<<20))

	// Over the pair cap in a small body is a 400 too.
	w = h.post(rd+"edit-tag", strings.Repeat("a&", maxPairs+1))
	require.Equal(t, 400, w.Code)
}

func TestEditTagManyIDsStillWorks(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	var ids []string
	first := h.addItem(f, itemSeed{})
	for i := 0; i < 999; i++ {
		ids = append(ids, "i=9999999")
	}
	ids = append(ids, "i="+FormatDecimal(first))
	w := h.post(rd+"edit-tag", "T="+h.tok+"&a=user/-/state/com.google/read&"+strings.Join(ids, "&"))
	require.Equal(t, 200, w.Code)
	require.True(t, isRead(h, first))

	// More than 10000 ids is refused.
	w = h.post(rd+"edit-tag", "a=user/-/state/com.google/read&"+strings.Repeat("i=1&", maxEditIDs+1))
	require.Equal(t, 400, w.Code)
}

func TestIconOnlyServesSafeTypes(t *testing.T) {
	h := newHarness(t)
	require.NoError(t, execSQL(h, "INSERT INTO settings (key, value) VALUES ('greader.icon_urls', 'true')"))
	noAuth := map[string]string{"Authorization": ""}
	cases := map[string]bool{
		"image/svg+xml": false, "text/html": false, "image/png": true, "image/vnd.microsoft.icon": true,
		"image/webp": true, "image/jpeg": true,
	}
	n := 0
	for ct, served := range cases {
		n++
		f := h.addFeed("https://x"+FormatDecimal(int64(n))+".example/f", "F", "")
		require.NoError(t, execSQL(h, "INSERT INTO feed_icons (feed_id, data, content_type, hash, fetched_at) VALUES (?, x'89504e47', ?, 'hh', 1)", f, ct))
		w := h.do(http.MethodGet, base+"/icon/"+FormatDecimal(f)+"-hh", "", noAuth)
		if !served {
			require.Equal(t, 404, w.Code, ct)
			continue
		}
		require.Equal(t, 200, w.Code, ct)
		require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
		require.Equal(t, "default-src 'none'; sandbox", w.Header().Get("Content-Security-Policy"))
	}
}

func TestMountPrefixNonReaderPathNeverReachesWebMux(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/api/greader.php/other", "/api/greader.php/api/greader.php/x", "//api//greader.php//other"} {
		w := h.do(http.MethodGet, p, "", nil)
		require.Equal(t, 404, w.Code, p)
		require.NotContains(t, w.Body.String(), "fallthrough")
	}
	require.Equal(t, http.StatusTeapot, h.do(http.MethodGet, "/other", "", nil).Code)
}

func TestUnencodedPlusLabelInStreamsAndMarkAll(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "Tech+News")
	it := h.addItem(f, itemSeed{})
	w := h.get(rd + "stream/items/ids?output=json&s=user/-/label/Tech+News")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), FormatDecimal(it))
	w = h.get(rd + "stream/contents?output=json&s=user/-/label/Tech+News")
	require.Contains(t, w.Body.String(), "Tech+News")
	require.False(t, isRead(h, it))
	h.post(rd+"mark-all-as-read", "T="+h.tok+"&s=user/-/label/Tech+News")
	require.True(t, isRead(h, it))
}
