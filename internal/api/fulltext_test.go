package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/imgproxy"
)

const ftPara = "The quick brown fox jumps over the lazy dog while the reader keeps going through a long and detailed paragraph of article text. "

func ftArticle(extra string) string {
	body := extra
	for i := 0; i < 6; i++ {
		body += "<p>" + strings.Repeat(ftPara, 3) + "</p>"
	}
	return `<!DOCTYPE html><html><head><title>T</title></head><body><nav><a href="/x">nav</a></nav><article><h1>T</h1>` +
		body + `</article><script>alert(1)</script></body></html>`
}

// ftSite is an article server counting hits; fail switches it to HTTP 500.
type ftSite struct {
	*httptest.Server
	hits  atomic.Int32
	fail  atomic.Bool
	delay time.Duration
	extra string
}

func newFTSite(t *testing.T) *ftSite {
	s := &ftSite{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if s.delay > 0 {
			time.Sleep(s.delay)
		}
		if s.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(ftArticle(s.extra)))
	}))
	t.Cleanup(s.Close)
	return s
}

func (h *harness) ftItem(site *ftSite, feedFulltext bool, private bool) (feed, item int64) {
	h.t.Helper()
	feed = h.addFeed("FT", 0)
	h.exec("UPDATE feeds SET fulltext = ?, allow_private_net = ? WHERE id = ?", b2i(feedFulltext), b2i(private), feed)
	item = h.addItem(feed, seedItem{Text: "teaser"})
	h.exec("UPDATE items SET url = ? WHERE id = ?", site.URL+"/posts/a.html", item)
	return feed, item
}

func TestFulltextSetModeExtractsOnceAndStores(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	site.extra = `<p><img src="http://cdn.example/pic.png"></p>`
	feed, id := h.ftItem(site, false, true)
	url := "/api/items/" + sid(id) + "/fulltext"

	// Effective 0: nothing happens, nothing fetched.
	code, body, _ := h.api(c, "POST", url, "")
	require.Equal(t, 200, code)
	require.Equal(t, "skipped", body["status"])
	require.Nil(t, body["content_html"])
	require.EqualValues(t, 0, body["effective"])
	require.Nil(t, body["mode"])
	require.EqualValues(t, 0, site.hits.Load())

	code, body, _ = h.api(c, "POST", url, `{"mode":1}`)
	require.Equal(t, 200, code)
	require.Equal(t, "ok", body["status"])
	require.EqualValues(t, 1, body["mode"])
	require.EqualValues(t, 1, body["effective"])
	require.Nil(t, body["error"])
	require.Greater(t, body["word_count"].(float64), float64(200))
	htmlOut := body["content_html"].(string)
	require.Contains(t, htmlOut, "quick brown fox")
	require.NotContains(t, htmlOut, "<script")
	require.Contains(t, htmlOut, imgproxy.Path([]byte(testSecret), imgproxy.FlagPrivateNet, "http://cdn.example/pic.png"), "served through the image proxy")
	require.EqualValues(t, 1, site.hits.Load())

	// Stored unproxied, with the source and no error.
	var stored, src string
	var errCol *string
	require.NoError(t, h.db.Reader().QueryRow("SELECT content_html, source_url, error FROM item_fulltext WHERE item_id = ?", id).Scan(&stored, &src, &errCol))
	require.Contains(t, stored, `src="http://cdn.example/pic.png"`)
	require.Equal(t, site.URL+"/posts/a.html", src)
	require.Nil(t, errCol)

	// Repeat without refresh: served from the store.
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "ok", body["status"])
	require.EqualValues(t, 1, site.hits.Load())
	// The item detail now shows the extraction.
	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Contains(t, det["content_html"], "quick brown fox")
	require.Equal(t, true, det["fulltext"].(map[string]any)["available"])

	// refresh=1 fetches again.
	_, body, _ = h.api(c, "POST", url+"?refresh=1", "")
	require.Equal(t, "ok", body["status"])
	require.EqualValues(t, 2, site.hits.Load())

	// mode 0 switches it off (content kept, not served); null follows the feed.
	_, body, _ = h.api(c, "POST", url, `{"mode":0}`)
	require.Equal(t, "skipped", body["status"])
	require.EqualValues(t, 0, body["mode"])
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(id), "")
	require.NotContains(t, det["content_html"], "quick brown fox")
	_, body, _ = h.api(c, "POST", url, `{"mode":null}`)
	require.Nil(t, body["mode"])
	require.Equal(t, "skipped", body["status"]) // feed fulltext is off
	h.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", feed)
	_, body, _ = h.api(c, "POST", url, "{}") // omitted mode keeps null, feed says 1: stored extraction served
	require.Equal(t, "ok", body["status"])
	require.EqualValues(t, 2, site.hits.Load())
}

func TestFulltextFeedModeExtractsWithEmptyBody(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	_, id := h.ftItem(site, true, true)
	code, body, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/fulltext", "")
	require.Equal(t, 200, code)
	require.Equal(t, "ok", body["status"])
	require.Nil(t, body["mode"]) // the empty body did not change the mode
	require.EqualValues(t, 1, body["effective"])
}

func TestFulltextFailureStoredAndRetried(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	site.fail.Store(true)
	_, id := h.ftItem(site, true, true)
	url := "/api/items/" + sid(id) + "/fulltext"

	_, body, _ := h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.Contains(t, body["error"], "HTTP 500")
	require.Nil(t, body["content_html"])
	var content *string
	var stored string
	require.NoError(t, h.db.Reader().QueryRow("SELECT content_html, error FROM item_fulltext WHERE item_id = ?", id).Scan(&content, &stored))
	require.Nil(t, content)
	require.Contains(t, stored, "HTTP 500")

	_, det, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	ft := det["fulltext"].(map[string]any)
	require.Equal(t, false, ft["available"])
	require.Contains(t, ft["error"], "HTTP 500")
	require.Contains(t, det["content_html"], "teaser") // feed content while extraction failed

	// Opening again does not hammer the failing page; retry is explicit.
	_, body, _ = h.api(c, "POST", url, "")
	require.Equal(t, "error", body["status"])
	require.EqualValues(t, 1, site.hits.Load())

	site.fail.Store(false)
	_, body, _ = h.api(c, "POST", url+"?refresh=1", "")
	require.Equal(t, "ok", body["status"])
	require.Nil(t, body["error"])
	require.EqualValues(t, 2, site.hits.Load())
	var errCol *string
	require.NoError(t, h.db.Reader().QueryRow("SELECT error FROM item_fulltext WHERE item_id = ?", id).Scan(&errCol))
	require.Nil(t, errCol)

	// A failed refresh keeps the good extraction.
	site.fail.Store(true)
	_, body, _ = h.api(c, "POST", url+"?refresh=1", "")
	require.Equal(t, "error", body["status"])
	require.Contains(t, body["content_html"], "quick brown fox")
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Contains(t, det["content_html"], "quick brown fox")
	require.Equal(t, true, det["fulltext"].(map[string]any)["available"])
}

func TestFulltextGuardedAgainstPrivateNets(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t) // 127.0.0.1
	_, id := h.ftItem(site, true, false)
	_, body, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/fulltext", "")
	require.Equal(t, "error", body["status"])
	require.Contains(t, body["error"], "not allowed")
	require.Zero(t, site.hits.Load())

	// Other hostile item URLs never reach the network.
	for _, u := range []string{"", "file:///etc/passwd", "ftp://x/y", "http://169.254.169.254/latest/meta-data", "javascript:alert(1)"} {
		h.exec("UPDATE items SET url = ? WHERE id = ?", u, id)
		_, body, _ = h.api(c, "POST", "/api/items/"+sid(id)+"/fulltext?refresh=1", "")
		require.Equal(t, "error", body["status"], u)
	}
	require.Zero(t, site.hits.Load())
}

func TestFulltextConcurrentRequestsShareOneFetch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	site.delay = 300 * time.Millisecond
	_, id := h.ftItem(site, true, true)
	var wg sync.WaitGroup
	statuses := make([]string, 4)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, body, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/fulltext", "")
			statuses[i], _ = body["status"].(string)
		}()
	}
	wg.Wait()
	for _, s := range statuses {
		require.Equal(t, "ok", s)
	}
	require.EqualValues(t, 1, site.hits.Load())
}

func TestFulltextValidationAndAuth(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	site := newFTSite(t)
	_, id := h.ftItem(site, false, true)
	url := "/api/items/" + sid(id) + "/fulltext"
	for _, bad := range []string{`{"mode":2}`, `{"mode":"1"}`, `{"mode":true}`, `{"mode":-1}`, `{"mode":[1]}`, `not json`} {
		code, _, _ := h.api(c, "POST", url, bad)
		require.Equal(t, 400, code, bad)
	}
	code, _, _ := h.api(c, "POST", "/api/items/999/fulltext", "")
	require.Equal(t, 404, code)
	code, _, _ = h.api(c, "POST", "/api/items/999/fulltext", `{"mode":1}`)
	require.Equal(t, 404, code)
	code, _, _ = h.api(c, "POST", "/api/items/abc/fulltext", "")
	require.Equal(t, 404, code)
	require.Equal(t, 401, h.do("POST", url, "").Code)
	code, _, _ = h.api(c, "POST", url, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	require.Equal(t, 403, code)
}
