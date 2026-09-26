package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/imgproxy"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

func TestImageRewriteAtServeTimeOnly(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	// A deployment that chose http_only keeps it: a stored row beats the new default.
	h.exec(`INSERT INTO settings (key, value) VALUES ('imgproxy.mode', '"http_only"')`)
	f := h.addFeed("A", 0)
	h.exec("UPDATE feeds SET allow_private_net = 1, allow_insecure_tls = 1 WHERE id = ?", f)
	plain := h.addFeed("B", 0)
	body := `<p><img src="http://a.example/1.png" srcset="http://a.example/1.png 1x, https://b.example/2.png 2x">` +
		`<img src="https://s.example/secure.png"><video poster="http://a.example/p.jpg"></video></p>`
	id := h.addItem(f, seedItem{Text: "x", Image: "http://a.example/lead.jpg"})
	h.exec("UPDATE item_content SET content_html = ? WHERE item_id = ?", body, id)
	idPlain := h.addItem(plain, seedItem{Text: "y", Image: "http://a.example/lead.jpg"})

	pathFor := func(flags int, u string) string { return imgproxy.Path([]byte(testSecret), flags, u) }

	_, list, _ := h.api(c, "GET", "/api/items?view=all", "")
	got := map[string]string{}
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		got[m["id"].(string)] = m["image"].(string)
	}
	require.Equal(t, pathFor(3, "http://a.example/lead.jpg"), got[sid(id)]) // flags from the item's feed
	require.Equal(t, pathFor(0, "http://a.example/lead.jpg"), got[sid(idPlain)])

	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	html := det["content_html"].(string)
	require.Contains(t, html, `src="`+pathFor(3, "http://a.example/1.png")+`"`)
	require.Contains(t, html, pathFor(3, "http://a.example/1.png")+" 1x")
	require.Contains(t, html, "https://b.example/2.png 2x", "http_only leaves https alone")
	require.Contains(t, html, `src="https://s.example/secure.png"`)
	require.Contains(t, html, `poster="`+pathFor(3, "http://a.example/p.jpg")+`"`)
	require.Equal(t, pathFor(3, "http://a.example/lead.jpg"), det["image"])

	// open returns the same rewritten item.
	_, open, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
	require.Contains(t, open["item"].(map[string]any)["content_html"], "/img/")

	// Stored HTML and the stored lead image are untouched.
	var stored, storedImg string
	require.NoError(t, h.db.Reader().QueryRow("SELECT content_html FROM item_content WHERE item_id = ?", id).Scan(&stored))
	require.Equal(t, body, stored)
	require.NoError(t, h.db.Reader().QueryRow("SELECT image_url FROM items WHERE id = ?", id).Scan(&storedImg))
	require.Equal(t, "http://a.example/lead.jpg", storedImg)

	// imgproxy.mode = all (the default, so no row) proxies https too.
	h.exec(`DELETE FROM settings WHERE key = 'imgproxy.mode'`)
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(id), "")
	// s.example is not the feed's host (a.example): no private-network grant.
	require.Contains(t, det["content_html"], `src="`+pathFor(2, "https://s.example/secure.png")+`"`)
	require.NotContains(t, det["content_html"], `"https://s.example`)
}

// TestPrivateNetGrantedOnlyToFeedHost: a feed's allow_private_net is signed
// into image URLs on the feed's own host only (any case, any port); a
// third-party image in the same item, card or extracted page is signed
// without it, so the guard still blocks it if it points at a private address.
func TestPrivateNetGrantedOnlyToFeedHost(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("Nas", 0)
	h.exec("UPDATE feeds SET allow_private_net = 1, url = 'http://nas.example:8080/rss' WHERE id = ?", f)
	body := `<p><img src="http://NAS.example/a.png"><img src="http://other.example/b.png"><img src="http://10.0.0.5/c.png"></p>`
	id := h.addItem(f, seedItem{Text: "x", Image: "http://nas.example/lead.jpg"})
	h.exec("UPDATE item_content SET content_html = ? WHERE item_id = ?", body, id)
	other := h.addItem(f, seedItem{Text: "y", Image: "http://10.0.0.5/lead.jpg"})
	pathFor := func(flags int, u string) string { return imgproxy.Path([]byte(testSecret), flags, u) }

	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Equal(t, pathFor(1, "http://nas.example/lead.jpg"), det["image"])
	html := det["content_html"].(string)
	require.Contains(t, html, `src="`+pathFor(1, "http://NAS.example/a.png")+`"`, "the feed's own host keeps the grant")
	require.Contains(t, html, `src="`+pathFor(0, "http://other.example/b.png")+`"`, "a third-party host does not")
	require.Contains(t, html, `src="`+pathFor(0, "http://10.0.0.5/c.png")+`"`, "nor another private address")

	_, list, _ := h.api(c, "GET", "/api/items?view=all", "")
	got := map[string]string{}
	for _, it := range list["items"].([]any) {
		m := it.(map[string]any)
		got[m["id"].(string)] = m["image"].(string)
	}
	require.Equal(t, pathFor(1, "http://nas.example/lead.jpg"), got[sid(id)])
	require.Equal(t, pathFor(0, "http://10.0.0.5/lead.jpg"), got[sid(other)], "cards too")
}

func TestImageRouteNeedsSessionAndSignature(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(up.Close)
	orig := up.URL + "/a.png" // 127.0.0.1: private

	path := imgproxy.Path([]byte(testSecret), imgproxy.FlagPrivateNet, orig)
	// No session: 401 JSON, upstream never contacted.
	rec := h.do("GET", path, "")
	require.Equal(t, 401, rec.Code)
	// A session but private net not allowed by the flags: guarded, 502.
	rec = h.do("GET", imgproxy.Path([]byte(testSecret), 0, orig), "", withCookie(c))
	require.Equal(t, 502, rec.Code)
	// Signed with the account secret and flagged: served.
	rec = h.do("GET", path, "", withCookie(c))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, testPNG, rec.Body.Bytes())
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	// Signed with another key, or flags edited: 403.
	rec = h.do("GET", imgproxy.Path([]byte("wrong-secret"), 1, orig), "", withCookie(c))
	require.Equal(t, 403, rec.Code)
	parts := strings.Split(path, "/")
	parts[3] = "0"
	rec = h.do("GET", strings.Join(parts, "/"), "", withCookie(c))
	require.Equal(t, 403, rec.Code)
	// A GET needs no X-Kipple-Client (<img> cannot send it) and is not origin-checked.
	rec = h.do("GET", path, "", withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client"); r.Header.Del("Sec-Fetch-Site") })
	require.Equal(t, 200, rec.Code)
	// Unknown /img paths are not swallowed as images.
	rec = h.do("GET", "/img/foo.png", "", withCookie(c))
	require.Equal(t, 404, rec.Code)
}

// The proxy route beats the SPA's catch-all on the shared mux, and only three-segment
// /img/ paths are claimed.
func TestImageRouteClaimedBeforeSPA(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("SPA")) })
	h.mux.Handle("/", spa)
	rec := h.do("GET", "/img/foo.png", "", withCookie(c))
	require.Equal(t, "SPA", rec.Body.String())
	rec = h.do("GET", "/img/sig/0/aHR0cDovL3guZXhhbXBsZS9h", "", withCookie(c))
	require.Equal(t, 403, rec.Code) // reached the proxy, bad signature
	rec = h.do("GET", "/img/sig/0/aHR0cDovL3guZXhhbXBsZS9h", "")
	require.Equal(t, 401, rec.Code)
}

// `kipple password` rotates account.secret from another process; the running
// server must stop honoring old signed URLs and sign new ones with the new key.
func TestImageSecretRotationIsSeenByARunningServer(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNG)
	}))
	t.Cleanup(up.Close)
	orig := up.URL + "/a.png"
	f := h.addFeed("A", 0)
	// The feed lives on the private host its images come from.
	h.exec("UPDATE feeds SET allow_private_net = 1, url = ? WHERE id = ?", up.URL+"/feed", f)
	id := h.addItem(f, seedItem{Image: orig})

	oldPath := imgproxy.Path([]byte(testSecret), imgproxy.FlagPrivateNet, orig)
	require.Equal(t, 200, h.do("GET", oldPath, "", withCookie(c)).Code)
	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Equal(t, oldPath, det["image"])

	newSecret := strings.Repeat("f", len(testSecret))
	h.exec("UPDATE account SET secret = ? WHERE id = 1", newSecret)

	// The secret is cached for a second (a list would otherwise read the account
	// row once per image); the rotation shows up once that has passed.
	require.Equal(t, 200, h.do("GET", oldPath, "", withCookie(c)).Code, "still cached within the TTL")
	h.clk.Advance(imageSecretTTL + time.Millisecond)

	require.Equal(t, 403, h.do("GET", oldPath, "", withCookie(c)).Code, "old signed URLs stop verifying")
	newPath := imgproxy.Path([]byte(newSecret), imgproxy.FlagPrivateNet, orig)
	require.Equal(t, 200, h.do("GET", newPath, "", withCookie(c)).Code)
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Equal(t, newPath, det["image"], "new lists are signed with the new secret")
}

func TestCardImagesUseThumbnailsWhenCacheIsOn(t *testing.T) {
	h, _ := cacheHarness(t, 64)
	c := h.login()
	f := h.addFeed("A", 0)
	h.exec("UPDATE feeds SET allow_private_net = 1 WHERE id = ?", f)
	id := h.addItem(f, seedItem{Text: "x", Image: "http://a.example/lead.jpg"})
	h.exec("UPDATE item_content SET content_html = ? WHERE item_id = ?", `<p><img src="http://a.example/1.png"></p>`, id)
	pathFor := func(flags int, u string) string { return imgproxy.Path([]byte(testSecret), flags, u) }

	// List cards carry the thumbnail variant (the signed thumb bit on top of the feed's flags)...
	_, list, _ := h.api(c, "GET", "/api/items?view=all", "")
	card := list["items"].([]any)[0].(map[string]any)
	require.Equal(t, pathFor(1|imgproxy.FlagThumb, "http://a.example/lead.jpg"), card["image"])
	// ...while the open article, its lead image and its body keep the originals.
	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Equal(t, pathFor(1, "http://a.example/lead.jpg"), det["image"])
	require.Contains(t, det["content_html"], `src="`+pathFor(1, "http://a.example/1.png")+`"`)

	_, st, _ := h.api(c, "GET", "/api/imgcache", "")
	require.EqualValues(t, 0, st["thumbnails"])
}

// TestSecretRotationKeepsOneImageHandler: the fetch slots, host limiter,
// thumbnail pool and decode budget are process-wide, so a rotation re-keys the
// one handler instead of building a second (with its own pool and budget)
// beside the draining old one.
func TestSecretRotationKeepsOneImageHandler(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old, ok := h.srv.imageHandler(ctx)
	require.True(t, ok)
	h.exec("UPDATE account SET secret = ? WHERE id = 1", strings.Repeat("f", 64))
	h.srv.imgMu.Lock()
	h.srv.imgSecretAt = time.Time{} // let the TTL lapse so the rotation is seen
	h.srv.imgMu.Unlock()
	fresh, ok := h.srv.imageHandler(ctx)
	require.True(t, ok)
	require.Same(t, old, fresh, "one handler per process")
	require.False(t, fresh.Closed())
	// Rotated through imageSecret alone (a list render) as well.
	h.exec("UPDATE account SET secret = ? WHERE id = 1", strings.Repeat("e", 64))
	h.srv.imgMu.Lock()
	h.srv.imgSecretAt = time.Time{}
	h.srv.imgMu.Unlock()
	secret, ok := h.srv.imageSecret(ctx)
	require.True(t, ok)
	require.Equal(t, strings.Repeat("e", 64), string(secret))
	again, _ := h.srv.imageHandler(ctx)
	require.Same(t, old, again)
}
