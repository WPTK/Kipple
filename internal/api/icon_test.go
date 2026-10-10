package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeedIcon(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	noIcon := h.addFeed("Two", 0)
	png := []byte("\x89PNG\r\n\x1a\n....")
	h.exec(`INSERT INTO feed_icons (feed_id, data, content_type, hash, fetched_at) VALUES (?, ?, 'image/png', 'abc123', 1)`, f, png)

	code, _, rec := h.api(c, "GET", "/api/feeds/"+sid(f)+"/icon?h=abc123", "")
	require.Equal(t, 200, code)
	require.Equal(t, png, rec.Body.Bytes())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, "private, max-age=604800", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")

	code, _, _ = h.api(c, "GET", "/api/feeds/"+sid(f)+"/icon", "")
	require.Equal(t, 200, code) // the hash is only a cache-buster
	for _, p := range []string{sid(noIcon), "999999", "0", "x", "-1"} {
		code, body, _ := h.api(c, "GET", "/api/feeds/"+p+"/icon", "")
		require.Equal(t, 404, code, p)
		require.Equal(t, "not_found", body["error"])
	}
	require.Equal(t, 401, h.do("GET", "/api/feeds/"+sid(f)+"/icon", "").Code)

	// Bootstrap points the feed at the icon URL only when one exists.
	_, body, _ := h.api(c, "GET", "/api/bootstrap", "")
	for _, fd := range body["feeds"].([]any) {
		m := fd.(map[string]any)
		switch m["id"] {
		case sid(f):
			require.Equal(t, "/api/feeds/"+sid(f)+"/icon?h=abc123", m["icon"])
		case sid(noIcon):
			require.Nil(t, m["icon"])
		}
	}

	// A non-image content type is sniffed, never echoed.
	h.exec(`UPDATE feed_icons SET content_type = 'text/html'`)
	_, _, rec = h.api(c, "GET", "/api/feeds/"+sid(f)+"/icon", "")
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
}
