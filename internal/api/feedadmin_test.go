package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

const rssBody = `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>https://ex.com/</link>
<item><guid>a</guid><title>A</title><link>https://ex.com/a</link></item></channel></rss>`

// openGuard lets the loopback test servers through; SSRF tests use the real guard.
func openGuard(bool, bool, bool) http.RoundTripper { return http.DefaultTransport }

func shortWaits(t *testing.T) {
	t.Helper()
	a, r := addWait, refreshWait
	addWait, refreshWait = 150*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { addWait, refreshWait = a, r })
}

// site serves the pages discovery is tested against.
func site(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssBody))
	})
	mux.HandleFunc("/one", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" title="Only" href="/feed.xml"></head><body></body></html>`))
	})
	mux.HandleFunc("/multi", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head>
			<link rel="alternate" type="application/rss+xml" title="RSS" href="/feed.xml">
			<link rel="alternate" type="application/atom+xml" title="Atom" href="atom.xml">
			<link rel="alternate" type="application/rss+xml" title="dup" href="/feed.xml#x">
			<link rel="alternate" type="application/rss+xml" title="private" href="http://10.0.0.1/f">
			<link rel="stylesheet" href="/a.css">
			</head><body></body></html>`))
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>no feeds</title></head><body><link rel="alternate" type="application/rss+xml" href="/late.xml"></body></html>`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), &hits
}

func (h *harness) feedRow(id int64, col string) (v sql.NullString) {
	h.t.Helper()
	require.NoError(h.t, h.db.Reader().QueryRow("SELECT CAST("+col+" AS TEXT) FROM feeds WHERE id = ?", id).Scan(&v))
	return v
}

func (h *harness) feedByURL(u string) int64 {
	h.t.Helper()
	id, found, err := h.db.FindFeedID(context.Background(), u)
	require.NoError(h.t, err)
	require.True(h.t, found, u)
	return id
}

// storeFeed creates a properly keyed feed through the store.
func (h *harness) storeFeed(url string, mod ...func(*store.NewFeed)) int64 {
	h.t.Helper()
	nf := store.NewFeed{URL: url}
	for _, m := range mod {
		m(&nf)
	}
	id, err := h.db.AddFeed(context.Background(), nf)
	require.NoError(h.t, err)
	return id
}

func (h *harness) events() *events.Sub { return h.hub.Subscribe(h.hub.LastID()) }

func feedChanged(t *testing.T, sub *events.Sub) []string {
	t.Helper()
	var out []string
	for _, ev := range drain(sub, "feed.changed", 50*time.Millisecond) {
		out = append(out, string(ev.Data))
	}
	return out
}

// ---- auth ----

func TestFeedAdminRoutesRequireSessionAndOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	routes := []struct{ method, path string }{
		{"POST", "/api/feeds"}, {"POST", "/api/reorder"}, {"GET", "/api/feeds/1"}, {"PATCH", "/api/feeds/1"}, {"DELETE", "/api/feeds/1"},
		{"POST", "/api/feeds/1/refresh"}, {"POST", "/api/feeds/1/mark-fetch-read"},
		{"POST", "/api/feeds/1/trimmed-unread/reset"}, {"POST", "/api/archive/purge-unstarred"},
		{"POST", "/api/folders"}, {"PATCH", "/api/folders/1"}, {"DELETE", "/api/folders/1"},
		{"GET", "/api/health/feeds/1/log"},
	}
	for _, tc := range routes {
		rec := h.do(tc.method, tc.path, "{}")
		require.Equal(t, http.StatusUnauthorized, rec.Code, tc.method+" "+tc.path)
		if tc.method == "GET" {
			continue
		}
		rec = h.do(tc.method, tc.path, "{}", withCookie(c), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
		require.Equal(t, http.StatusForbidden, rec.Code, tc.method+" "+tc.path)
		rec = h.do(tc.method, tc.path, "{}", withCookie(c), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
		require.Equal(t, http.StatusForbidden, rec.Code, tc.method+" "+tc.path)
	}
}

// ---- POST /api/feeds ----

func TestAddFeedCreatesAndWaitsForFirstFetch(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	folder := h.addFolder("News")
	h.sched.reply = sched.Reply{Outcome: fetch.OutcomeOK, NewItems: 3}
	sub := h.events()

	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/feed.xml", "folder_id": sid(folder), "title": "  My Feed "}))
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok", body["status"])
	feed := body["feed"].(map[string]any)
	id := h.feedByURL(srv + "/feed.xml")
	require.Equal(t, sid(id), feed["id"])
	require.Equal(t, sid(folder), feed["folder_id"])
	require.Equal(t, "My Feed", feed["custom_title"])
	require.Equal(t, srv+"/feed.xml", feed["url"])
	fo := body["fetch"].(map[string]any)
	require.Equal(t, "ok", fo["outcome"])
	require.EqualValues(t, 3, fo["new_items"])

	subs := h.sched.submitted()
	require.Len(t, subs, 1)
	require.Equal(t, sched.Priority{FeedID: id, Full: true, Trigger: fetch.TriggerSubscribe}, subs[0])
	require.Equal(t, []string{fmt.Sprintf(`{"feed_id":"%d"}`, id)}, feedChanged(t, sub))
}

func TestAddFeedDiscovery(t *testing.T) {
	srv, hits := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()

	t.Run("one advertised feed is used", func(t *testing.T) {
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/one"}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "ok", body["status"])
		require.Equal(t, srv+"/feed.xml", body["feed"].(map[string]any)["url"])
	})
	t.Run("the same page again reports the existing feed without a new one", func(t *testing.T) {
		before := h.count("SELECT count(*) FROM feeds")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/one"}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "exists", body["status"])
		require.Equal(t, before, h.count("SELECT count(*) FROM feeds"))
		require.Len(t, h.sched.submitted(), 1)
	})
	t.Run("several feeds ask the user to choose", func(t *testing.T) {
		h.exec("DELETE FROM feeds")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/multi"}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "choose", body["status"])
		require.Equal(t, []any{
			map[string]any{"url": srv + "/feed.xml", "title": "RSS", "type": "rss"},
			map[string]any{"url": srv + "/atom.xml", "title": "Atom", "type": "atom"},
		}, body["candidates"], "duplicates and private-address links are dropped")
		require.Zero(t, h.count("SELECT count(*) FROM feeds"), "nothing is created until the user picks")
	})
	t.Run("a directly typed feed URL that already exists (http vs https, no fetch)", func(t *testing.T) {
		id := h.storeFeed("http://" + strings.TrimPrefix(srv, "http://") + "/feed.xml")
		n := hits.Load()
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "https://" + strings.TrimPrefix(srv, "http://") + "/feed.xml"}))
		require.Equal(t, 200, code, body)
		require.Equal(t, "exists", body["status"])
		require.Equal(t, sid(id), body["feed"].(map[string]any)["id"])
		require.Equal(t, n, hits.Load(), "FindFeedByURL runs before any discovery request")
	})
	t.Run("a pre-migration URL still matches", func(t *testing.T) {
		id := h.storeFeed("https://moved.example/new")
		h.exec("UPDATE feeds SET url_original = 'http://old.example/rss', url_original_key = 'old.example/rss' WHERE id = ?", id)
		_, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": "http://old.example/rss"}))
		require.Equal(t, "exists", body["status"])
	})
	t.Run("a page linking to an already subscribed feed reports it", func(t *testing.T) {
		h.exec("DELETE FROM feeds")
		id := h.storeFeed(srv + "/feed.xml")
		_, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/one"}))
		require.Equal(t, "exists", body["status"])
		require.Equal(t, sid(id), body["feed"].(map[string]any)["id"])
	})
	t.Run("pages without usable feeds", func(t *testing.T) {
		for path, kind := range map[string]string{"/plain": "no_feed", "/missing": "unreachable"} {
			code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + path}))
			require.Equal(t, 422, code, path)
			require.Equal(t, kind, body["error"], path)
		}
	})
}

func TestAddFeedValidation(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	for _, tc := range []struct {
		name, body, kind string
	}{
		{"empty body", ``, "bad_request"},
		{"not json", `{`, "bad_request"},
		{"not an object", `[1]`, "bad_request"},
		{"null", `null`, "bad_request"},
		{"unknown field", `{"url":"https://a.example/f","bogus":1}`, "unknown_field"},
		{"missing url", `{}`, "invalid_url"},
		{"url not a string", `{"url":5}`, "invalid_url"},
		{"blank url", `{"url":"  "}`, "invalid_url"},
		{"ftp", `{"url":"ftp://a.example/f"}`, "invalid_url"},
		{"javascript", `{"url":"javascript:alert(1)"}`, "invalid_url"},
		{"no host", `{"url":"http:///f"}`, "invalid_url"},
		{"file", `{"url":"file:///etc/passwd"}`, "invalid_url"},
		{"loopback literal", `{"url":"http://127.0.0.1/f"}`, "private_address"},
		{"loopback with port", `{"url":"http://127.0.0.1:8080/f"}`, "private_address"},
		{"private literal", `{"url":"http://192.168.1.5/f"}`, "private_address"},
		{"private literal typed without a scheme", `{"url":"192.168.1.5/f"}`, "private_address"},
		{"10/8 literal", `{"url":"http://10.1.2.3/f"}`, "private_address"},
		{"link-local metadata", `{"url":"http://169.254.169.254/latest/meta-data"}`, "private_address"},
		{"ipv6 loopback", `{"url":"http://[::1]/f"}`, "private_address"},
		{"ipv4-mapped ipv6", `{"url":"http://[::ffff:127.0.0.1]/f"}`, "private_address"},
		{"unspecified", `{"url":"http://0.0.0.0/f"}`, "private_address"},
		{"lone word", `{"url":"feeds"}`, "invalid_url"},
		{"allow_private_net not a boolean", `{"url":"https://a.example/f","allow_private_net":"yes"}`, "bad_request"},
		{"folder not a number", `{"url":"https://a.example/f","folder_id":"x"}`, "bad_request"},
		{"folder missing", `{"url":"https://a.example/f","folder_id":"99"}`, "folder_not_found"},
		{"title too long", `{"url":"https://a.example/f","title":"` + strings.Repeat("x", 201) + `"}`, "bad_request"},
		{"title not a string", `{"url":"https://a.example/f","title":4}`, "bad_request"},
	} {
		code, body, _ := h.api(c, "POST", "/api/feeds", tc.body)
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, tc.kind, body["error"], tc.name)
	}
	require.Zero(t, h.count("SELECT count(*) FROM feeds"))
	require.Empty(t, h.sched.submitted())
}

// A name that resolves to a blocked address is stopped by the real dial guard.
func TestAddFeedSSRFThroughHostnameIsBlocked(t *testing.T) {
	srv, hits := site(t)
	h := newHarness(t) // default Guard: fetch.Client's dial-time check
	c := h.login()
	u := srv + "/feed.xml"
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": u}))
	require.Equal(t, 422, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Contains(t, body["message"], "private network")
	require.Zero(t, hits.Load(), "the server was never reached")
	require.Zero(t, h.count("SELECT count(*) FROM feeds"))
}

// The add dialog's "Allow addresses on my own network" adds a feed on a private address: discovery goes through
// the guard with the exception on, and the feed is created with it.
func TestAddFeedAllowPrivateNet(t *testing.T) {
	srv, hits := site(t)
	h := newHarness(t) // the real dial guard
	c := h.login()
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/one", "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok", body["status"])
	feed := body["feed"].(map[string]any)
	require.Equal(t, srv+"/feed.xml", feed["url"])
	require.Equal(t, true, feed["allow_private_net"])
	require.EqualValues(t, 1, hits.Load(), "the page was reached through the guard")

	// A literal private address is accepted with the exception, too.
	lit := strings.Replace(srv, "localhost", "127.0.0.1", 1) + "/one"
	h.exec("DELETE FROM feeds")
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": lit}))
	require.Equal(t, 400, code, body)
	require.Equal(t, "private_address", body["error"])
	require.Contains(t, body["message"], "Allow addresses on my own network")
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": lit, "allow_private_net": true}))
	require.Equal(t, 200, code, body)
	require.Equal(t, true, body["feed"].(map[string]any)["allow_private_net"])
}

// Whatever form the address is typed or pasted in, the dialog adds the feed it names.
func TestAddFeedTypedAddressForms(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	hostPort := strings.TrimPrefix(srv, "http://")
	for _, typed := range []string{
		"  " + srv + "/feed.xml\n",
		"feed:" + srv + "/feed.xml",
		srv + "/feed.xml#latest",
		"HTTP://" + strings.ToUpper(hostPort) + "/feed.xml",
	} {
		h.exec("DELETE FROM feeds")
		code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": typed}))
		require.Equal(t, 200, code, "%q: %v", typed, body)
		require.Equal(t, srv+"/feed.xml", body["feed"].(map[string]any)["url"], typed)
	}
}

func TestAddFeedFirstFetchPendingAndSchedulerDown(t *testing.T) {
	srv, _ := site(t)
	shortWaits(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()

	h.sched.hang = true
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/feed.xml"}))
	require.Equal(t, 200, code, body)
	require.Equal(t, "ok", body["status"])
	require.Equal(t, true, body["fetch"].(map[string]any)["pending"])
	require.EqualValues(t, 1, h.count("SELECT count(*) FROM feeds WHERE next_fetch_at <= ?", h.clk.Now().Unix()), "the feed stays due")

	h.exec("DELETE FROM feeds")
	h.sched.hang, h.sched.submitErr = false, sched.ErrStopped
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/feed.xml"}))
	require.Equal(t, 200, code, body)
	require.Equal(t, true, body["fetch"].(map[string]any)["pending"])
	require.Equal(t, 1, h.sched.wakes)
}

func TestAddFeedReportsFailedFirstFetch(t *testing.T) {
	srv, _ := site(t)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()
	h.sched.reply = sched.Reply{Outcome: fetch.OutcomeError, ErrClass: "http", ErrMsg: "HTTP 500"}
	_, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": srv + "/feed.xml"}))
	require.Equal(t, "ok", body["status"], "the feed exists; the error is reported beside it")
	fo := body["fetch"].(map[string]any)
	require.Equal(t, "http", fo["error_class"])
	require.Equal(t, "HTTP 500", fo["error"])
}

// Web discovery follows fetch.user_agent_mode: a site that refuses Kipple's
// User-Agent is retried once as a browser; "default" never retries.
func TestAddFeedDiscoveryRetriesWithBrowserUserAgent(t *testing.T) {
	var uas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas = append(uas, r.UserAgent())
		if !strings.Contains(r.UserAgent(), "Chrome") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssBody))
	}))
	t.Cleanup(srv.Close)
	base := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	h := newHarness(t, func(o *Options) { o.Guard = openGuard })
	c := h.login()

	h.exec(`INSERT INTO settings (key, value) VALUES ('fetch.user_agent_mode', '"default"')`)
	code, body, _ := h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": base + "/feed.xml"}))
	require.Equal(t, 422, code, body)
	require.Equal(t, "unreachable", body["error"])
	require.Contains(t, body["message"], "HTTP 403 Forbidden")
	require.Len(t, uas, 1)

	uas = nil
	h.exec(`UPDATE settings SET value = '"browser_on_failure"' WHERE key = 'fetch.user_agent_mode'`)
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": base + "/feed.xml"}))
	require.Equal(t, 200, code, body)
	require.Len(t, uas, 2)

	h.exec("DELETE FROM feeds")
	uas = nil
	h.exec(`UPDATE settings SET value = '"browser_always"' WHERE key = 'fetch.user_agent_mode'`)
	code, body, _ = h.api(c, "POST", "/api/feeds", jsonStr(map[string]any{"url": base + "/feed.xml"}))
	require.Equal(t, 200, code, body)
	require.Len(t, uas, 1, "browser_always leads with the browser string")
}

// One control-character rule for titles, folder names and header values: tab is
// fine, everything else below 0x20 and DEL is not.
func TestHasControlRule(t *testing.T) {
	require.False(t, hasControl("a\tb ünï"))
	for _, s := range []string{"a\nb", "a\rb", "a\x00b", "a\x1fb", "a\x7fb"} {
		require.True(t, hasControl(s), "%q", s)
	}
}
