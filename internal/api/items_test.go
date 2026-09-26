package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/imgproxy"
	"github.com/WPTK/kipple/internal/stats"
)

// ---- seeding and request helpers ----

const baseID = int64(1790251200) * 1_000_000

type seedItem struct {
	Title   string
	Text    string
	Words   int
	SortAt  int64
	Read    bool
	Starred bool
	Image   string
	Enc     string
	FT      string // extracted full text html (item_fulltext), "" = none
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (h *harness) count(q string, args ...any) int {
	h.t.Helper()
	var n int
	require.NoError(h.t, h.db.Reader().QueryRow(q, args...).Scan(&n))
	return n
}

func (h *harness) addFolder(name string) int64 {
	h.t.Helper()
	var id int64
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "INSERT INTO folders (name, position) VALUES (?, 5)", name)
		if err != nil {
			return err
		}
		id, err = r.LastInsertId()
		return err
	}))
	return id
}

func (h *harness) addFeed(title string, folder int64) int64 {
	h.t.Helper()
	if folder == 0 {
		folder = 1
	}
	var id int64
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		u := "https://" + strings.ToLower(strings.ReplaceAll(title, " ", "")) + ".example/feed"
		r, err := tx.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, site_url, next_fetch_at)
			VALUES (?, ?, ?, 'x.example', ?, 'https://x.example/', 4102444800)`, folder, u, u, title)
		if err != nil {
			return err
		}
		id, err = r.LastInsertId()
		return err
	}))
	return id
}

var seedN int64

func (h *harness) addItem(feed int64, s seedItem) int64 {
	h.t.Helper()
	seedN++
	id := baseID + seedN*1000
	if s.SortAt == 0 {
		s.SortAt = id / 1_000_000
	}
	if s.Title == "" {
		s.Title = fmt.Sprintf("Item %d", seedN)
	}
	if s.Text == "" {
		s.Text = "body of " + s.Title
	}
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		var img, enc any
		if s.Image != "" {
			img = s.Image
		}
		if s.Enc != "" {
			enc = s.Enc
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash,
			url, title, author, image_url) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, feed, b2i(s.Read), b2i(s.Starred), s.SortAt, s.SortAt, s.Words, fmt.Sprintf("g:%d", id), "c", "t",
			fmt.Sprintf("https://x.example/%d", id), s.Title, "Ann", img); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text, enclosures_json) VALUES (?,?,?,?)`,
			id, "<p>"+s.Text+"</p>", s.Text, enc); err != nil {
			return err
		}
		if s.FT != "" {
			_, err := tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, content_html, content_text, extracted_at) VALUES (?,?,?,1)`, id, s.FT, s.FT)
			return err
		}
		return nil
	}))
	return id
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// trim moves an item into the ledger, with a restore stub when withStub, trimmed at trimmedAt.
func (h *harness) trim(id int64, withStub bool, trimmedAt int64) {
	h.t.Helper()
	require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
			SELECT id, feed_id, uid, read, ?, ? FROM items WHERE id = ?`, trimmedAt, trimmedAt, id); err != nil {
			return err
		}
		if withStub {
			if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
				url, title, author, image_url, origin_title, fulltext_mode, content_html, content_text, enclosures_json)
				SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash, i.url, i.title, i.author,
				  i.image_url, i.origin_title, i.fulltext_mode, c.content_html, c.content_text, c.enclosures_json
				FROM items i JOIN item_content c ON c.item_id = i.id WHERE i.id = ?`, id); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM items WHERE id = ?", id)
		return err
	}))
}

// api sends an authenticated same-origin request and decodes the JSON body.
func (h *harness) api(c *http.Cookie, method, path, body string, mod ...func(*http.Request)) (int, map[string]any, *httptest.ResponseRecorder) {
	h.t.Helper()
	rec := h.do(method, path, body, append([]func(*http.Request){withCookie(c)}, mod...)...)
	var out map[string]any
	if rec.Body.Len() > 0 && strings.Contains(rec.Header().Get("Content-Type"), "json") {
		require.NoError(h.t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec.Code, out, rec
}

func sid(id int64) string { return strconv.FormatInt(id, 10) }

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func itemIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	for _, it := range body["items"].([]any) {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func strs(ids ...int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = sid(id)
	}
	return out
}

func anyStrs(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// drain collects the events already published to sub.
func drain(sub *events.Sub, typ string, wait time.Duration) []events.Event {
	var out []events.Event
	deadline := time.After(wait)
	for {
		select {
		case ev := <-sub.C:
			if typ == "" || ev.Type == typ {
				out = append(out, ev)
			}
		case <-deadline:
			return out
		}
	}
}

// ---- auth and origin rules ----

func TestNewRoutesRequireSession(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/bootstrap"}, {"GET", "/api/items"}, {"GET", "/api/items/5"}, {"POST", "/api/items/5/open"},
		{"PUT", "/api/items/5/star"}, {"POST", "/api/items/mark-read"}, {"POST", "/api/stats/events"},
	} {
		rec := h.do(tc.method, tc.path, "{}")
		require.Equal(t, http.StatusUnauthorized, rec.Code, tc.method+" "+tc.path)
		require.JSONEq(t, `{"error":"auth"}`, rec.Body.String())
	}
}

func TestNewRoutesOriginRules(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	noClient := func(r *http.Request) { r.Header.Del("X-Kipple-Client") }
	crossSite := func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }
	badOrigin := func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "https://evil.example")
	}
	goodOrigin := func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "http://"+r.Host)
	}

	type route struct{ method, path, body string }
	writes := []route{
		{"POST", "/api/items/" + sid(id) + "/open", `{"via":"tap"}`},
		{"PUT", "/api/items/" + sid(id) + "/star", `{"starred":true}`},
		{"POST", "/api/items/mark-read", `{"ids":["` + sid(id) + `"],"read":true}`},
	}
	for _, rt := range writes {
		name := rt.method + " " + rt.path
		for label, mod := range map[string]func(*http.Request){"no client header": noClient, "cross-site": crossSite, "bad origin": badOrigin} {
			code, body, _ := h.api(c, rt.method, rt.path, rt.body, mod)
			require.Equal(t, http.StatusForbidden, code, name+" "+label)
			require.Equal(t, "origin", body["error"])
		}
		code, _, _ := h.api(c, rt.method, rt.path, rt.body, goodOrigin)
		require.Equal(t, http.StatusOK, code, name+" Origin match still needs the client header, which the default has")
		code, _, _ = h.api(c, rt.method, rt.path, rt.body, goodOrigin, noClient)
		require.Equal(t, http.StatusForbidden, code, name+" Origin without header")
	}

	// stats/events is exempt from the header only (sendBeacon), never from rule 1 or 2.
	ev := `{"events":[]}`
	code, _, _ := h.api(c, "POST", "/api/stats/events", ev, noClient)
	require.Equal(t, http.StatusNoContent, code)
	code, _, _ = h.api(c, "POST", "/api/stats/events", ev, goodOrigin, noClient)
	require.Equal(t, http.StatusNoContent, code, "beacon: Origin, no header")
	for label, mod := range map[string]func(*http.Request){"cross-site": crossSite, "bad origin": badOrigin} {
		code, body, _ := h.api(c, "POST", "/api/stats/events", ev, mod, noClient)
		require.Equal(t, http.StatusForbidden, code, label)
		require.Equal(t, "origin", body["error"])
	}
	code, _, _ = h.api(c, "POST", "/api/stats/events", ev, func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site") // neither Sec-Fetch-Site nor Origin
		r.Header.Del("X-Kipple-Client")
	})
	require.Equal(t, http.StatusForbidden, code)

	// The CSV export gets the same guard as the OPML download: the origin rule alone,
	// so a cross-site request is refused before routing (the route itself is phase 4).
	code, _, _ = h.api(c, "GET", "/api/stats/export.csv", "", crossSite)
	require.Equal(t, http.StatusForbidden, code)
}

// ---- GET /api/items ----

func TestListItems(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	fo := h.addFolder("News")
	a, b := h.addFeed("A", 0), h.addFeed("B", fo)
	// sort order (newest first): a3, b2, a2(read), b1(starred+read), a1
	a1 := h.addItem(a, seedItem{SortAt: 1000})
	b1 := h.addItem(b, seedItem{SortAt: 2000, Read: true, Starred: true})
	a2 := h.addItem(a, seedItem{SortAt: 3000, Read: true})
	b2 := h.addItem(b, seedItem{SortAt: 4000})
	a3 := h.addItem(a, seedItem{SortAt: 5000})

	tests := []struct {
		name  string
		query string
		want  []int64
	}{
		{"default is unread", "", []int64{a3, b2, a1}},
		{"unread", "?view=unread", []int64{a3, b2, a1}},
		{"all", "?view=all", []int64{a3, b2, a2, b1, a1}},
		{"starred", "?view=starred", []int64{b1}},
		{"feed", "?view=all&feed=" + sid(b), []int64{b2, b1}},
		{"folder", "?view=all&folder=" + sid(fo), []int64{b2, b1}},
		{"feed unread", "?feed=" + sid(a), []int64{a3, a1}},
		{"order=date", "?view=all&order=date&limit=2", []int64{a3, b2}},
		{"ids ignore view and order", "?ids=" + sid(a1) + "," + sid(b1) + ",999", []int64{b1, a1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body, _ := h.api(c, "GET", "/api/items"+tc.query, "")
			require.Equal(t, 200, code)
			require.Equal(t, strs(tc.want...), itemIDs(t, body))
		})
	}

	t.Run("keyset paging", func(t *testing.T) {
		var got []string
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			code, body, _ := h.api(c, "GET", "/api/items?view=all&limit=2&cursor="+cursor, "")
			require.Equal(t, 200, code)
			got = append(got, itemIDs(t, body)...)
			if body["next_cursor"] == nil {
				break
			}
			cursor = body["next_cursor"].(string)
			require.NotEmpty(t, cursor)
		}
		require.Equal(t, strs(a3, b2, a2, b1, a1), got)
	})

	t.Run("ties on sort_at break by id", func(t *testing.T) {
		h2 := newHarness(t)
		c2 := h2.login()
		f := h2.addFeed("T", 0)
		x := h2.addItem(f, seedItem{SortAt: 500})
		y := h2.addItem(f, seedItem{SortAt: 500})
		z := h2.addItem(f, seedItem{SortAt: 500})
		_, page1, _ := h2.api(c2, "GET", "/api/items?limit=2", "")
		require.Equal(t, strs(z, y), itemIDs(t, page1))
		_, page2, _ := h2.api(c2, "GET", "/api/items?limit=2&cursor="+page1["next_cursor"].(string), "")
		require.Equal(t, strs(x), itemIDs(t, page2))
		require.Nil(t, page2["next_cursor"])
	})

	t.Run("limit is clamped to 100", func(t *testing.T) {
		h2 := newHarness(t)
		c2 := h2.login()
		f := h2.addFeed("L", 0)
		for i := 0; i < 103; i++ {
			h2.addItem(f, seedItem{})
		}
		_, body, _ := h2.api(c2, "GET", "/api/items?limit=500", "")
		require.Len(t, body["items"], 100)
		require.NotNil(t, body["next_cursor"])
	})

	for _, bad := range []string{
		"?view=nope", "?feed=x", "?folder=-", "?feed=1&folder=1", "?limit=abc", "?limit=-1",
		"?order=asc", "?ids=1,x", "?ids=0",
	} {
		code, body, _ := h.api(c, "GET", "/api/items"+bad, "")
		require.Equal(t, 400, code, bad)
		require.Equal(t, "bad_request", body["error"], bad)
	}
	// a cursor that cannot be used (garbage, old relevance form, another ordering) is bad_cursor
	old := base64.RawURLEncoding.EncodeToString([]byte("r1.5|42"))
	other := base64.RawURLEncoding.EncodeToString([]byte("s2|1.5|42"))
	for _, bad := range []string{"?cursor=@@@", "?cursor=bm90LWEtY3Vyc29y", "?cursor=" + old, "?order=oldest&cursor=" + other, "?q=x&order=rank&cursor=" + base64.RawURLEncoding.EncodeToString([]byte("100.5"))} {
		code, body, _ := h.api(c, "GET", "/api/items"+bad, "")
		require.Equal(t, 400, code, bad)
		require.Equal(t, "bad_cursor", body["error"], bad)
		require.NotEmpty(t, body["message"], bad)
	}
	code0, _, _ := h.api(c, "GET", "/api/items?q=hello", "")
	require.Equal(t, 200, code0)
	code0, _, _ = h.api(c, "GET", "/api/items?order=rank", "")
	require.Equal(t, 400, code0, "rank needs a query")
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "1"
	}
	code, _, _ := h.api(c, "GET", "/api/items?ids="+strings.Join(tooMany, ","), "")
	require.Equal(t, 400, code)
}

func TestCardShape(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	long := strings.Repeat("word ", 200) // 1000 chars
	id := h.addItem(f, seedItem{Title: "Long", Text: "  spaced\n\n out  " + long, Words: 231, Image: "https://x.example/i.jpg", SortAt: 1234})
	short := h.addItem(f, seedItem{Title: "Short", Words: 99})
	edge := h.addItem(f, seedItem{Title: "Edge", Words: 100})
	exact := h.addItem(f, seedItem{Title: "Exact", Words: 230})

	_, body, _ := h.api(c, "GET", "/api/items?ids="+sid(id)+","+sid(short)+","+sid(edge)+","+sid(exact), "")
	cards := map[string]map[string]any{}
	for _, it := range body["items"].([]any) {
		m := it.(map[string]any)
		cards[m["title"].(string)] = m
	}
	lc := cards["Long"]
	require.Equal(t, sid(id), lc["id"])
	require.Equal(t, sid(f), lc["feed_id"])
	require.Equal(t, "Ann", lc["author"])
	require.Equal(t, fmt.Sprintf("https://x.example/%d", id), lc["url"])
	require.Equal(t, imgproxy.Path([]byte(testSecret), 0, "https://x.example/i.jpg"), lc["image"], "the default mode proxies every image")
	require.EqualValues(t, 1234, lc["published_at"])
	require.EqualValues(t, 1234, lc["sort_at"])
	require.Equal(t, false, lc["read"])
	require.Equal(t, false, lc["starred"])
	require.EqualValues(t, 231, lc["word_count"])
	require.EqualValues(t, 2, lc["reading_minutes"], "ceil(231/230)")
	ex := lc["excerpt"].(string)
	require.True(t, strings.HasPrefix(ex, "spaced out word word"), ex)
	require.LessOrEqual(t, len([]rune(ex)), 280)
	require.Len(t, []rune(ex), 280)
	require.NotContains(t, lc, "snippet")

	require.Nil(t, cards["Short"]["reading_minutes"], "under 100 words")
	require.EqualValues(t, 1, cards["Edge"]["reading_minutes"])
	require.EqualValues(t, 1, cards["Exact"]["reading_minutes"])
	require.Nil(t, cards["Short"]["image"])
}

func TestListItemsEmptyIsArray(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, body, rec := h.api(c, "GET", "/api/items", "")
	require.JSONEq(t, `{"items":[],"next_cursor":null,"as_of":"0","fallback":false}`, rec.Body.String())
	require.NotNil(t, body)
}

// ---- GET /api/items/{id} ----

func TestGetItem(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("Feed A", 0)
	id := h.addItem(f, seedItem{Title: "Hello", Text: "plain", Words: 300, Enc: `[{"url":"https://x.example/a.mp3","type":"audio/mpeg"}]`})
	ft := h.addItem(f, seedItem{Title: "Extracted", FT: "<p>full</p>"})
	h.exec("UPDATE items SET fulltext_mode = 1 WHERE id = ?", ft)
	h.exec(`INSERT INTO item_fulltext (item_id, content_html, error, extracted_at) SELECT ?, NULL, 'boom', 1`, h.addItem(f, seedItem{Title: "Failed"}))

	code, body, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
	require.Equal(t, 200, code)
	require.Equal(t, "Hello", body["title"])
	require.Equal(t, "<p>plain</p>", body["content_html"])
	require.Equal(t, false, body["trimmed"])
	require.Equal(t, sid(id), body["id"])
	require.EqualValues(t, 2, body["reading_minutes"])
	require.Equal(t, []any{map[string]any{"url": "https://x.example/a.mp3", "type": "audio/mpeg"}}, body["enclosures"])
	require.Equal(t, map[string]any{"id": sid(f), "title": "Feed A", "site_url": "https://x.example/"}, body["feed"])
	require.Equal(t, map[string]any{"mode": nil, "effective": float64(0), "available": false, "error": nil}, body["fulltext"])

	_, body, _ = h.api(c, "GET", "/api/items/"+sid(ft), "")
	require.Equal(t, map[string]any{"mode": float64(1), "effective": float64(1), "available": true, "error": nil}, body["fulltext"])
	require.Equal(t, "<p>full</p>", body["content_html"], "extracted text is served when the effective mode is 1")

	var failedID int64
	require.NoError(t, h.db.Reader().QueryRow("SELECT item_id FROM item_fulltext WHERE error = 'boom'").Scan(&failedID))
	_, body, _ = h.api(c, "GET", "/api/items/"+sid(failedID), "")
	require.Equal(t, map[string]any{"mode": nil, "effective": float64(0), "available": false, "error": "boom"}, body["fulltext"])
	require.Equal(t, "<p>body of Failed</p>", body["content_html"])

	// no enclosures: an empty array, never null
	_, body, _ = h.api(c, "GET", "/api/items/"+sid(h.addItem(f, seedItem{})), "")
	require.Equal(t, []any{}, body["enclosures"])

	for _, p := range []string{"999", "abc", "0"} {
		code, body, _ := h.api(c, "GET", "/api/items/"+p, "")
		require.Equal(t, 404, code, p)
		require.Equal(t, "not_found", body["error"])
	}
	// prefetch-safe: not a read, not a stat
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", id))
}

func TestGetItemLedgerStub(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	now := h.clk.Now().Unix()
	fresh := h.addItem(f, seedItem{Title: "Fresh stub", Read: true})
	old := h.addItem(f, seedItem{Title: "Stale stub"})
	bare := h.addItem(f, seedItem{Title: "No stub"})
	h.trim(fresh, true, now-86400)
	h.trim(old, true, now-91*86400) // past retention.restore_days (90)
	h.trim(bare, false, now-86400)

	code, body, _ := h.api(c, "GET", "/api/items/"+sid(fresh), "")
	require.Equal(t, 200, code)
	require.Equal(t, true, body["trimmed"])
	require.Equal(t, "Fresh stub", body["title"])
	require.Equal(t, "<p>body of Fresh stub</p>", body["content_html"])
	require.Equal(t, true, body["read"])
	require.Equal(t, sid(f), body["feed"].(map[string]any)["id"])
	require.Equal(t, false, body["starred"])

	for _, id := range []int64{old, bare} {
		code, _, _ := h.api(c, "GET", "/api/items/"+sid(id), "")
		require.Equal(t, 404, code)
	}
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
}

// ---- POST /api/items/{id}/open ----

func TestOpenItem(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("Feed A", 0)
	id := h.addItem(f, seedItem{Title: "Hello"})

	code, body, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
	require.Equal(t, 200, code)
	key, _ := body["session_key"].(string)
	require.Len(t, key, 32)
	item := body["item"].(map[string]any)
	require.Equal(t, sid(id), item["id"])
	require.Equal(t, true, item["read"], "the returned item is the post-open state")
	require.Equal(t, 1, h.count("SELECT read FROM items WHERE id = ?", id))
	require.Equal(t, h.clk.Now().Unix(), int64(h.count("SELECT read_at FROM items WHERE id = ?", id)))

	var kind, cl, feedTitle, itemTitle, sk string
	var inferred int
	require.NoError(t, h.db.Reader().QueryRow("SELECT kind, client, inferred, feed_title, item_title, session_key FROM stats_events").
		Scan(&kind, &cl, &inferred, &feedTitle, &itemTitle, &sk))
	require.Equal(t, []any{"open", "web", int64(0), "Feed A", "Hello", key}, []any{kind, cl, int64(inferred), feedTitle, itemTitle, sk})

	evs := drain(sub, "items.state", 50*time.Millisecond)
	require.Len(t, evs, 1)
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d"],"read":true,"source":"web"}`, id), string(evs[0].Data))

	// A second open of an already-read item is still an open (new session), but no state event.
	h.clk.Advance(time.Minute)
	_, body2, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"key"}`, func(r *http.Request) { r.Header.Set("X-Kipple-Client", "pwa") })
	require.NotEqual(t, key, body2["session_key"])
	require.Equal(t, 2, h.count("SELECT count(*) FROM stats_events WHERE kind = 'open'"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE client = 'pwa'"))
	require.Empty(t, drain(sub, "items.state", 50*time.Millisecond))

	// empty body is fine
	code, _, _ = h.api(c, "POST", "/api/items/"+sid(id)+"/open", "")
	require.Equal(t, 200, code)
}

func TestOpenItemErrors(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	for _, tc := range []struct {
		name, path, body string
		code             int
	}{
		{"unknown", "/api/items/999/open", `{"via":"tap"}`, 404},
		{"not a number", "/api/items/abc/open", `{"via":"tap"}`, 404},
		{"bad via", "/api/items/" + sid(id) + "/open", `{"via":"swipe"}`, 400},
		{"bad json", "/api/items/" + sid(id) + "/open", `{`, 400},
	} {
		code, _, _ := h.api(c, "POST", tc.path, tc.body)
		require.Equal(t, tc.code, code, tc.name)
	}
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", id))
}

func TestOpenLedgerStub(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{Title: "Stubbed"})
	h.trim(id, true, h.clk.Now().Unix()-3600)
	code, body, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"nav"}`)
	require.Equal(t, 200, code)
	require.Equal(t, true, body["item"].(map[string]any)["trimmed"])
	require.Equal(t, 1, h.count("SELECT read FROM trimmed_items WHERE id = ?", id), "the ledger flag follows")
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE id = ?", id), "opening does not restore")
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'open' AND item_title = 'Stubbed'"))
}

// ---- PUT /api/items/{id}/star ----

func TestStarItem(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{Title: "S"})

	put := func(body string) (int, map[string]any) {
		code, b, _ := h.api(c, "PUT", "/api/items/"+sid(id)+"/star", body)
		return code, b
	}
	code, body := put(`{"starred":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, map[string]any{"starred": true, "restored": false}, body)
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'star' AND client = 'web' AND item_id = ?", id))
	require.Equal(t, 1, h.count("SELECT starred FROM items WHERE id = ?", id))

	put(`{"starred":true}`) // no change: no row
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'star'"))
	code, body = put(`{"starred":false}`)
	require.Equal(t, 200, code)
	require.Equal(t, map[string]any{"starred": false, "restored": false}, body)
	put(`{"starred":false}`)
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'unstar'"))
	require.Equal(t, 2, h.count("SELECT count(*) FROM stats_events"))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", id), "starring is not a read")

	evs := drain(sub, "items.state", 50*time.Millisecond)
	require.Len(t, evs, 2, "no event for the no-op replays")
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d"],"starred":true,"source":"web"}`, id), string(evs[0].Data))
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d"],"starred":false,"source":"web"}`, id), string(evs[1].Data))

	for _, bad := range []string{`{}`, `{"starred":"yes"}`, `nope`, ``} {
		code, _ := put(bad)
		require.Equal(t, 400, code, bad)
	}
	code, _, _ = h.api(c, "PUT", "/api/items/999/star", `{"starred":true}`)
	require.Equal(t, 404, code)
}

func TestStarRestoresLedgerItem(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	now := h.clk.Now().Unix()
	stub := h.addItem(f, seedItem{Title: "Stubbed", Read: true})
	old := h.addItem(f, seedItem{Title: "Old"})
	bare := h.addItem(f, seedItem{Title: "Bare"})
	h.trim(stub, true, now-86400)
	h.trim(old, true, now-100*86400)
	h.trim(bare, false, now-86400)

	code, body, _ := h.api(c, "PUT", "/api/items/"+sid(stub)+"/star", `{"starred":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, map[string]any{"starred": true, "restored": true}, body)
	require.Equal(t, 1, h.count("SELECT starred FROM items WHERE id = ?", stub))
	require.Equal(t, 1, h.count("SELECT read FROM items WHERE id = ?", stub), "keeps its read state")
	require.Equal(t, 0, h.count("SELECT count(*) FROM trimmed_items WHERE id = ?", stub))
	require.Equal(t, 1, h.count("SELECT count(*) FROM item_content WHERE item_id = ?", stub))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'star' AND item_id = ?", stub), "a restore by star records the star")
	evs := drain(sub, "items.state", 50*time.Millisecond)
	require.Len(t, evs, 1)
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d"],"starred":true,"restored":["%d"],"source":"web"}`, stub, stub), string(evs[0].Data))

	// A stub past restore_days, and a ledger id with no stub, cannot be starred.
	for _, id := range []int64{old, bare} {
		code, _, _ := h.api(c, "PUT", "/api/items/"+sid(id)+"/star", `{"starred":true}`)
		require.Equal(t, 404, code)
	}
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events"))
}

// ---- POST /api/items/mark-read ----

func TestMarkReadByIDs(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	a, b, cc := h.addItem(f, seedItem{}), h.addItem(f, seedItem{}), h.addItem(f, seedItem{Read: true})

	mark := func(body string) (int, map[string]any) {
		code, out, _ := h.api(c, "POST", "/api/items/mark-read", body)
		return code, out
	}
	code, out := mark(jsonStr(map[string]any{"ids": strs(a, b, cc, 999), "read": true, "reason": "swipe"}))
	require.Equal(t, 200, code)
	require.Equal(t, strs(a, b), anyStrs(out["changed"]))
	require.Equal(t, []string{}, anyStrs(out["restored"]))
	require.Equal(t, 3, h.count("SELECT count(*) FROM items WHERE read = 1"))
	evs := drain(sub, "items.state", 50*time.Millisecond)
	require.Len(t, evs, 1)
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d","%d"],"read":true,"source":"web"}`, a, b), string(evs[0].Data))

	// numeric ids are accepted too; replay changes nothing and sends no event
	code, out = mark(fmt.Sprintf(`{"ids":[%d],"read":true,"reason":"key"}`, a))
	require.Equal(t, 200, code)
	require.Equal(t, []string{}, anyStrs(out["changed"]))
	require.Empty(t, drain(sub, "items.state", 50*time.Millisecond))

	code, out = mark(jsonStr(map[string]any{"ids": strs(a), "read": false, "reason": "key"}))
	require.Equal(t, 200, code)
	require.Equal(t, strs(a), anyStrs(out["changed"]))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", a))

	code, out = mark(`{"ids":[],"read":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, []string{}, anyStrs(out["changed"]))

	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"), "mark-read has no stats path")
}

func TestMarkReadUnreadRestoresLedger(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	now := h.clk.Now().Unix()
	stub := h.addItem(f, seedItem{Title: "Stubbed", Read: true})
	bare := h.addItem(f, seedItem{Title: "Bare", Read: true})
	h.trim(stub, true, now-3600)
	h.trim(bare, false, now-3600)

	code, out, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(stub, bare), "read": false, "reason": "key"}))
	require.Equal(t, 200, code)
	require.Equal(t, strs(stub), anyStrs(out["restored"]))
	require.Equal(t, strs(stub), anyStrs(out["changed"]))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", stub))
	require.Equal(t, now+7*86400, int64(h.count("SELECT retain_until FROM items WHERE id = ?", stub)))
	require.Equal(t, 0, h.count("SELECT read FROM trimmed_items WHERE id = ?", bare), "ledger-only id: the flag flips")
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"), "a restore by mark-unread is not a stat")
	evs := drain(sub, "items.state", 50*time.Millisecond)
	require.Len(t, evs, 1)
	require.JSONEq(t, fmt.Sprintf(`{"ids":["%d"],"read":false,"restored":["%d"],"source":"web"}`, stub, stub), string(evs[0].Data))
}

func TestMarkReadScope(t *testing.T) {
	type fixture struct {
		h                  *harness
		c                  *http.Cookie
		a, b               int64
		fo                 int64
		a1, a2, a3, b1, b2 int64
		ledgerA, ledgerB   int64
	}
	setup := func(t *testing.T) fixture {
		h := newHarness(t)
		fx := fixture{h: h, c: h.login()}
		fx.fo = h.addFolder("News")
		fx.a, fx.b = h.addFeed("A", 0), h.addFeed("B", fx.fo)
		fx.a1 = h.addItem(fx.a, seedItem{})
		fx.a2 = h.addItem(fx.a, seedItem{Starred: true})
		fx.b1 = h.addItem(fx.b, seedItem{})
		fx.b2 = h.addItem(fx.b, seedItem{Starred: true})
		fx.a3 = h.addItem(fx.a, seedItem{})
		fx.ledgerA = h.addItem(fx.a, seedItem{})
		fx.ledgerB = h.addItem(fx.b, seedItem{})
		h.trim(fx.ledgerA, false, h.clk.Now().Unix())
		h.trim(fx.ledgerB, false, h.clk.Now().Unix())
		return fx
	}
	unreadIDs := func(fx fixture) []string {
		rows, err := fx.h.db.Reader().Query("SELECT id FROM items WHERE read = 0 ORDER BY id")
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			out = append(out, sid(id))
		}
		return out
	}
	post := func(fx fixture, body string) (int, map[string]any) {
		code, out, _ := fx.h.api(fx.c, "POST", "/api/items/mark-read", body)
		return code, out
	}

	t.Run("all with max_id bound", func(t *testing.T) {
		fx := setup(t)
		code, out := post(fx, fmt.Sprintf(`{"scope":{"all":true,"view":"unread"},"max_id":"%d","read":true,"reason":"bulk"}`, fx.b1))
		require.Equal(t, 200, code)
		require.Equal(t, strs(fx.a1, fx.a2, fx.b1), anyStrs(out["changed"]))
		require.Equal(t, strs(fx.b2, fx.a3), unreadIDs(fx))
		require.Equal(t, 0, fx.h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"), "ledger ids above max_id are untouched")
	})
	t.Run("all without max_id uses the committed max", func(t *testing.T) {
		fx := setup(t)
		code, out := post(fx, `{"scope":{"all":true},"read":true,"reason":"bulk"}`)
		require.Equal(t, 200, code)
		require.Len(t, out["changed"], 5)
		require.Empty(t, unreadIDs(fx))
		require.Equal(t, 2, fx.h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
	})
	t.Run("feed", func(t *testing.T) {
		fx := setup(t)
		code, out := post(fx, fmt.Sprintf(`{"scope":{"feed_id":"%d","view":"all"},"read":true,"reason":"bulk"}`, fx.a))
		require.Equal(t, 200, code)
		require.Equal(t, strs(fx.a1, fx.a2, fx.a3), anyStrs(out["changed"]))
		require.Equal(t, strs(fx.b1, fx.b2), unreadIDs(fx))
		require.Equal(t, 1, fx.h.count("SELECT read FROM trimmed_items WHERE id = ?", fx.ledgerA))
		require.Equal(t, 0, fx.h.count("SELECT read FROM trimmed_items WHERE id = ?", fx.ledgerB))
	})
	t.Run("folder", func(t *testing.T) {
		fx := setup(t)
		code, out := post(fx, fmt.Sprintf(`{"scope":{"folder_id":%d},"read":true,"reason":"bulk"}`, fx.fo))
		require.Equal(t, 200, code)
		require.Equal(t, strs(fx.b1, fx.b2), anyStrs(out["changed"]))
		require.Equal(t, 1, fx.h.count("SELECT read FROM trimmed_items WHERE id = ?", fx.ledgerB))
	})
	t.Run("starred view", func(t *testing.T) {
		fx := setup(t)
		code, out := post(fx, `{"scope":{"all":true,"view":"starred"},"read":true,"reason":"bulk"}`)
		require.Equal(t, 200, code)
		require.Equal(t, strs(fx.a2, fx.b2), anyStrs(out["changed"]))
		require.Equal(t, 0, fx.h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
	})
	t.Run("feed and starred combine", func(t *testing.T) {
		fx := setup(t)
		_, out := post(fx, fmt.Sprintf(`{"scope":{"feed_id":"%d","view":"starred"},"read":true}`, fx.a))
		require.Equal(t, strs(fx.a2), anyStrs(out["changed"]))
	})
	t.Run("no stats", func(t *testing.T) {
		fx := setup(t)
		post(fx, `{"scope":{"all":true},"read":true,"reason":"scroll"}`)
		require.Equal(t, 0, fx.h.count("SELECT count(*) FROM stats_events"))
	})
	t.Run("big change becomes resync", func(t *testing.T) {
		fx := setup(t)
		sub := fx.h.hub.Subscribe(0)
		defer sub.Close()
		fx.h.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 600)
			INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
			SELECT 1790251200000000 + 1000000 * i, ?, 1, 1, 'bulk' || i, 'c', 't', 'x' FROM n`, fx.a)
		post(fx, `{"scope":{"all":true},"read":true,"reason":"bulk"}`)
		var types []string
		for _, ev := range drain(sub, "", 50*time.Millisecond) {
			types = append(types, ev.Type)
		}
		require.Contains(t, types, "resync")
		require.NotContains(t, types, "items.state")
	})
}

func TestMarkReadValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	for _, tc := range []struct{ name, body string }{
		{"not json", `x`},
		{"empty object", `{}`},
		{"read missing", `{"ids":["1"]}`},
		{"neither ids nor scope", `{"read":true}`},
		{"both ids and scope", `{"ids":["1"],"scope":{"all":true},"read":true}`},
		{"bad reason", `{"ids":["1"],"read":true,"reason":"whim"}`},
		{"bad id", `{"ids":["abc"],"read":true}`},
		{"zero id", `{"ids":["0"],"read":true}`},
		{"scope without target", `{"scope":{},"read":true}`},
		{"scope with two targets", `{"scope":{"all":true,"feed_id":"1"},"read":true}`},
		{"scope all:false", `{"scope":{"all":false},"read":true}`},
		{"scope bad feed", `{"scope":{"feed_id":"x"},"read":true}`},
		{"scope bad view", `{"scope":{"all":true,"view":"nope"},"read":true}`},
		{"scope bad max_id", `{"scope":{"all":true},"max_id":"zzz","read":true}`},
		{"scope mark unread", `{"scope":{"all":true},"read":false}`},
	} {
		code, out, _ := h.api(c, "POST", "/api/items/mark-read", tc.body)
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, "bad_request", out["error"], tc.name)
	}
	tooMany := make([]string, maxMarkIDs+1)
	for i := range tooMany {
		tooMany[i] = "1"
	}
	code, _, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": tooMany, "read": true}))
	require.Equal(t, 400, code)
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", id), "nothing changed by any rejected request")
}

// The structural rule (design §7.2, §8): mark-read has no path to the Recorder.
func TestMarkReadNeverTouchesRecorder(t *testing.T) {
	rec := &countingRecorder{}
	h := newHarness(t, func(o *Options) { o.Stats = rec })
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(id), "read": true, "reason": "swipe"}))
	h.api(c, "POST", "/api/items/mark-read", `{"scope":{"all":true},"read":true,"reason":"bulk"}`)
	h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(id), "read": false, "reason": "key"}))
	require.Zero(t, rec.n.Load())
	h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
	require.EqualValues(t, 1, rec.n.Load(), "open does record")
}

// ---- POST /api/stats/events ----

func TestStatsEvents(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	other := h.addItem(f, seedItem{})
	_, opened, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
	key := opened["session_key"].(string)
	h.clk.Advance(40 * time.Second)

	post := func(body string, mod ...func(*http.Request)) {
		code, _, rec := h.api(c, "POST", "/api/stats/events", body, mod...)
		require.Equal(t, 204, code, body)
		require.Empty(t, rec.Body.String())
	}
	ev := func(kind string, item int64, key string, value any) map[string]any {
		m := map[string]any{"kind": kind, "item_id": sid(item)}
		if key != "" {
			m["session_key"] = key
		}
		if value != nil {
			m["value"] = value
		}
		return m
	}

	post(jsonStr(map[string]any{"events": []any{
		ev("read_time", id, key, 15),
		ev("read_time", id, key, 15),
		ev("scroll", id, key, 40),
		ev("scroll", id, key, 80),
		ev("scroll", id, key, 60),
		ev("open_original", id, "", nil),
		ev("share", id, "", nil),
	}}), func(r *http.Request) { r.Header.Set("X-Kipple-Client", "pwa") })
	require.Equal(t, 2, h.count("SELECT count(*) FROM stats_events WHERE kind = 'read_time' AND session_key = ? AND client = 'pwa'", key))
	require.Equal(t, 30, h.count("SELECT SUM(value) FROM stats_events WHERE kind = 'read_time'"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'scroll'"))
	require.Equal(t, 80, h.count("SELECT value FROM stats_events WHERE kind = 'scroll'"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'open_original'"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'share'"))
	before := h.count("SELECT count(*) FROM stats_events")

	// Everything invalid is dropped silently, and the valid events around it still land.
	post(jsonStr(map[string]any{"events": []any{
		ev("read_time", id, key, 0),                       // out of range
		ev("read_time", id, key, 61),                      // out of range
		ev("read_time", id, key, 16),                      // 46 s > 40 s elapsed + 5
		ev("read_time", other, key, 5),                    // key belongs to another item
		ev("read_time", id, "nope", 5),                    // unknown session
		ev("read_time", id, "", 5),                        // no session
		ev("read_time", id, key, nil),                     // no value
		ev("scroll", other, key, 5),                       // wrong item
		ev("open", id, key, nil),                          // opens have their own endpoint
		ev("star", id, "", nil),                           // as do stars
		ev("unstar", id, "", nil),                         //
		ev("read", id, "", nil),                           // no such kind
		ev("share", 424242, "", nil),                      // unknown item
		map[string]any{"kind": "share", "item_id": "abc"}, // bad id
		map[string]any{"kind": "share"},                   // no id
		ev("read_time", id, key, 10),                      // valid: 40 s total
	}}))
	_ = before
	require.Equal(t, 40, h.count("SELECT SUM(value) FROM stats_events WHERE kind = 'read_time'"))
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events WHERE kind IN ('star','unstar') OR (kind = 'open' AND session_key IS NOT ?)", key))
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'open'"))
}

func TestStatsEventsMalformedBodiesAre204(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	for _, body := range []string{``, `nope`, `{"events":"x"}`, `[]`, `{"events":[1,2]}`, `{"events":[{"kind":7}]}`, `{"events":null}`, strings.Repeat("x", statsBodyMax+10)} {
		code, _, _ := h.api(c, "POST", "/api/stats/events", body)
		require.Equal(t, 204, code, "%.20q", body)
	}
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
}

func TestStatsEventsBatchCap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	events := make([]any, maxStatsBatch+50)
	for i := range events {
		events[i] = map[string]any{"kind": "share", "item_id": sid(id)}
	}
	code, _, _ := h.api(c, "POST", "/api/stats/events", jsonStr(map[string]any{"events": events}))
	require.Equal(t, 204, code)
	require.Equal(t, maxStatsBatch, h.count("SELECT count(*) FROM stats_events"))
}

func TestStatsEventsBeaconWithBlobBody(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	rec := h.do("POST", "/api/stats/events", jsonStr(map[string]any{"events": []any{map[string]any{"kind": "share", "item_id": sid(id)}}}),
		withCookie(c), func(r *http.Request) {
			r.Header.Del("X-Kipple-Client")
			r.Header.Set("Content-Type", "text/plain;charset=UTF-8") // what sendBeacon sends for a string
		})
	require.Equal(t, 204, rec.Code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'share' AND client = 'web'"))
}

// ---- GET /api/bootstrap ----

func TestBootstrap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	fo := h.addFolder("News")
	a, b := h.addFeed("A", 0), h.addFeed("B", fo)
	h.exec("UPDATE feeds SET custom_title = 'Bee', fulltext = 1, retention = 100, interval_minutes = 60, consecutive_failures = 2 WHERE id = ?", b)
	h.exec("INSERT INTO feed_icons (feed_id, data, content_type, hash, fetched_at) VALUES (?, x'00', 'image/png', 'abc123', 1)", a)
	h.addItem(a, seedItem{})
	h.addItem(a, seedItem{Starred: true, Read: true})
	h.addItem(b, seedItem{})
	h.addItem(b, seedItem{})
	h.exec("INSERT INTO settings (key, value) VALUES ('retention.default', '500'), ('sys.id_high_water', '5'), ('bogus.key', '1')")

	code, body, _ := h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, 200, code)
	require.Equal(t, map[string]any{"username": testUser, "api_enabled": false}, body["user"])
	require.EqualValues(t, h.clk.Now().Unix(), body["server_time"])
	require.Equal(t, "", body["version"])
	require.Equal(t, map[string]any{"unread": float64(3), "starred": float64(1), "muted": float64(0)}, body["counts"])
	require.Equal(t, []any{}, body["highlights"])
	require.Equal(t, []any{}, body["warnings"])

	set := body["settings"].(map[string]any)
	require.EqualValues(t, 500, set["retention.default"], "override wins")
	require.EqualValues(t, 30, set["refresh.interval_minutes"], "default fills in")
	require.EqualValues(t, 90, set["retention.restore_days"])
	require.Equal(t, "America/New_York", set["tz"])
	require.Equal(t, "all", set["imgproxy.mode"])
	require.Equal(t, false, set["stats.api_single_read_is_open"])
	require.NotContains(t, set, "sys.id_high_water")
	require.NotContains(t, set, "bogus.key")

	folders := body["folders"].([]any)
	require.Len(t, folders, 2)
	f0, f1 := folders[0].(map[string]any), folders[1].(map[string]any)
	require.Equal(t, map[string]any{"id": "1", "name": "Uncategorized", "position": float64(0), "is_default": true, "unread": float64(1)}, f0)
	require.Equal(t, map[string]any{"id": sid(fo), "name": "News", "position": float64(5), "is_default": false, "unread": float64(2)}, f1)

	feeds := body["feeds"].([]any)
	require.Len(t, feeds, 2)
	fa, fb := feeds[0].(map[string]any), feeds[1].(map[string]any)
	require.Equal(t, map[string]any{
		"id": sid(a), "folder_id": "1", "title": "A", "site_url": "https://x.example/", "icon": "/api/feeds/" + sid(a) + "/icon?h=abc123",
		"unread": float64(1), "status": "ok", "fulltext": false, "fulltext_effective": false, "retention": nil, "interval_minutes": nil, "auto_read_days": nil, "is_archive": false, "starred_count": float64(1),
	}, fa)
	require.Equal(t, map[string]any{
		"id": sid(b), "folder_id": sid(fo), "title": "Bee", "site_url": "https://x.example/", "icon": nil,
		"unread": float64(2), "status": "erroring", "fulltext": true, "fulltext_effective": true, "retention": float64(100), "interval_minutes": float64(60), "auto_read_days": nil, "is_archive": false, "starred_count": float64(0),
	}, fb)

	require.Equal(t, []any{map[string]any{"id": "42", "kind": "manual", "done": float64(1), "total": float64(3), "new_items": float64(0), "errors": float64(0)}}, body["runs"])
}

func TestBootstrapWarningsAndArchive(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Version = "v1.2.3" })
	c := h.login()
	// archive feed: hidden while empty, listed (is_archive) once it holds items
	h.exec(`INSERT INTO feeds (id, url, url_key, host, title, enabled, disabled_reason, retention, next_fetch_at)
		VALUES (900, 'kipple:archive', 'kipple:archive', '', 'Archive', 0, 'archive', 0, 0)`)
	_, body, _ := h.api(c, "GET", "/api/bootstrap", "")
	require.Empty(t, body["feeds"])
	require.Equal(t, "v1.2.3", body["version"])
	h.addItem(900, seedItem{})
	_, body, _ = h.api(c, "GET", "/api/bootstrap", "")
	feeds := body["feeds"].([]any)
	require.Len(t, feeds, 1)
	require.Equal(t, true, feeds[0].(map[string]any)["is_archive"])
	require.Equal(t, "archive", feeds[0].(map[string]any)["status"])

	codes := func() []string {
		_, body, _ := h.api(c, "GET", "/api/bootstrap", "")
		var out []string
		for _, w := range body["warnings"].([]any) {
			require.NotEmpty(t, w.(map[string]any)["message"])
			out = append(out, w.(map[string]any)["code"].(string))
		}
		return out
	}
	require.Empty(t, codes())
	h.exec("INSERT INTO settings (key, value) VALUES ('sys.last_snapshot_at', ?)", strconv.FormatInt(h.clk.Now().Unix()-49*3600, 10))
	require.Equal(t, []string{"snapshot"}, codes(), "snapshot older than 48 h")
	h.exec("UPDATE settings SET value = ? WHERE key = 'sys.last_snapshot_at'", strconv.FormatInt(h.clk.Now().Unix()-3600, 10))
	require.Empty(t, codes())
	h.exec("INSERT INTO settings (key, value) VALUES ('sys.last_snapshot_error', '\"disk full\"')")
	require.Equal(t, []string{"snapshot"}, codes())
	h.exec("DELETE FROM settings WHERE key = 'sys.last_snapshot_error'")

	h.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 10001)
		INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
		SELECT 1790251200000000 + 1000000 * i, 900, 1, 1, 'u' || i, 'c', 't', 'x' FROM n`)
	require.Equal(t, []string{"unread_cap"}, codes())
}

// ---- SSE counts ----

func TestCountsAreCoalesced(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.CountsInterval = 150 * time.Millisecond })
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	g := h.addFeed("G", 0)
	var ids []int64
	for i := 0; i < 6; i++ {
		ids = append(ids, h.addItem(f, seedItem{}))
	}
	h.addItem(g, seedItem{})

	for _, id := range ids[:5] { // five quick writes
		code, _, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(id), "read": true}))
		require.Equal(t, 200, code)
	}
	evs := drain(sub, "counts", 450*time.Millisecond)
	require.Len(t, evs, 2, "one at once, one trailing event for the rest")
	var first, last struct {
		Unread int64            `json:"unread_total"`
		Feeds  map[string]int64 `json:"feeds"`
	}
	require.NoError(t, json.Unmarshal(evs[0].Data, &first))
	require.NoError(t, json.Unmarshal(evs[1].Data, &last))
	require.Greater(t, first.Unread, last.Unread-1)
	require.EqualValues(t, 2, last.Unread, "the trailing event carries the final counts")
	require.Equal(t, map[string]int64{sid(f): 1, sid(g): 1}, last.Feeds)

	// after quiet, the next write is immediate again
	time.Sleep(200 * time.Millisecond)
	h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(ids[5]), "read": true}))
	evs = drain(sub, "counts", 50*time.Millisecond)
	require.Len(t, evs, 1)
	require.NoError(t, json.Unmarshal(evs[0].Data, &last))
	require.Equal(t, map[string]int64{sid(f): 0, sid(g): 1}, last.Feeds, "an emptied feed is reported as zero")
}

func TestNoCountsEventWithoutChange(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.CountsInterval = 20 * time.Millisecond })
	c := h.login()
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{Read: true})
	h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": strs(id), "read": true}))
	h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
	require.Empty(t, drain(sub, "counts", 100*time.Millisecond))
	// star changes the starred count only, but still goes out through items.state and counts
	h.api(c, "PUT", "/api/items/"+sid(id)+"/star", `{"starred":true}`)
	require.Len(t, drain(sub, "counts", 100*time.Millisecond), 1)
}

type countingRecorder struct{ n atomic.Int64 }

func (c *countingRecorder) Record(*sql.Tx, stats.Event) error {
	c.n.Add(1)
	return nil
}

func (c *countingRecorder) RecordStars(_ *sql.Tx, _, _ string, ids []int64) error {
	c.n.Add(int64(len(ids)))
	return nil
}

// Counts events cannot go out of order: publishCounts takes pubMu around query+publish, so a
// publish cannot start while another one holds it.
func TestPublishCountsIsSerialized(t *testing.T) {
	h := newHarness(t)
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	h.srv.pubMu.Lock()
	done := make(chan struct{})
	go func() { h.srv.publishCounts(); close(done) }()
	require.Empty(t, drain(sub, "counts", 100*time.Millisecond), "blocked while another publish is in progress")
	h.srv.pubMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishCounts never finished")
	}
	require.Len(t, drain(sub, "counts", 100*time.Millisecond), 1)
}
