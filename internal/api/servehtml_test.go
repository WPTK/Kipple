package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/imgproxy"
)

func TestDetailIsTransformedAtServeTimeOnly(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	body := `<p>Read <a href="https://a.example/p?utm_source=rss&amp;id=1">it</a> now<a href="#fn1" id="r1">1</a>.</p>` +
		`<iframe src="https://www.youtube.com/embed/abc123" sandbox=""></iframe>` +
		`<video src="http://a.example/v.mp4" autoplay></video><ol><li id="fn1">note</li></ol>`
	id := h.addItem(f, seedItem{Text: "x"})
	h.exec("UPDATE item_content SET content_html = ? WHERE item_id = ?", body, id)
	h.exec("UPDATE items SET url = 'https://a.example/post?utm_campaign=z&fbclid=1&p=2' WHERE id = ?", id)

	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	html := det["content_html"].(string)
	require.Contains(t, html, `href="https://a.example/p?id=1" target="_blank" rel="noopener noreferrer"`)
	require.Contains(t, html, `href="#kp-fn1" id="kp-r1"`)
	require.Contains(t, html, `<li id="kp-fn1">`)
	require.Contains(t, html, `data-provider="youtube" data-id="abc123"`)
	require.NotContains(t, html, "<iframe")
	// http_only mode: the thumbnail is still proxied, so nothing reaches YouTube before the tap.
	require.Contains(t, html, `<img src="`+imgproxy.Path([]byte(testSecret), 0, "https://i.ytimg.com/vi/abc123/hqdefault.jpg")+`"`)
	require.Contains(t, html, "Open video")
	require.Equal(t, "https://a.example/post?p=2", det["url"])

	// The stored row and its URL are untouched.
	var stored, url string
	require.NoError(t, h.db.Reader().QueryRow("SELECT content_html FROM item_content WHERE item_id = ?", id).Scan(&stored))
	require.Equal(t, body, stored)
	require.NoError(t, h.db.Reader().QueryRow("SELECT url FROM items WHERE id = ?", id).Scan(&url))
	require.Equal(t, "https://a.example/post?utm_campaign=z&fbclid=1&p=2", url)

	// The card link is stripped too.
	_, list, _ := h.api(c, "GET", "/api/items?view=all", "")
	require.Equal(t, "https://a.example/post?p=2", list["items"].([]any)[0].(map[string]any)["url"])

	// links.strip_tracking off: parameters stay, links still open safely.
	code, _, _ := h.api(c, "PATCH", "/api/settings", `{"links.strip_tracking":false}`)
	require.Equal(t, 200, code)
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Contains(t, det["content_html"], `href="https://a.example/p?utm_source=rss&amp;id=1" target="_blank"`)
	require.Equal(t, "https://a.example/post?utm_campaign=z&fbclid=1&p=2", det["url"])
	_, list, _ = h.api(c, "GET", "/api/items?view=all", "")
	require.Equal(t, "https://a.example/post?utm_campaign=z&fbclid=1&p=2", list["items"].([]any)[0].(map[string]any)["url"])
}

func TestStripTrackingSettingMetadata(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, body, _ := h.api(c, "GET", "/api/settings", "")
	require.Equal(t, true, body["values"].(map[string]any)["links.strip_tracking"])
	var found bool
	for _, s := range body["settings"].([]any) {
		m := s.(map[string]any)
		if m["key"] == "links.strip_tracking" {
			found = true
			require.Equal(t, "Remove tracking from links", m["label"])
			require.Equal(t, "settings", m["surface"])
			require.Equal(t, "bool", m["kind"])
			require.NotEmpty(t, m["description"])
		}
	}
	require.True(t, found)
}
