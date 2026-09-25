package greader

// Contract tests: each replays a recorded client's request sequence (from
// docs/research: netnewswire.md, reeder-classic.md, design §10) over a real
// HTTP server and asserts the response shapes the client's decoder needs.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// client is a bare HTTP client that never follows redirects, so any 3xx a
// Reader path produces is visible.
type client struct {
	t    *testing.T
	base string // server URL + mount prefix, e.g. http://127.0.0.1:1234/api/greader.php
	ua   string
	auth string // Authorization header value
}

type resp struct {
	code   int
	header http.Header
	body   string
}

func newClient(t *testing.T, h *harness, prefix, ua string) *client {
	t.Helper()
	srv := httptest.NewServer(h.h)
	t.Cleanup(srv.Close)
	return &client{t: t, base: srv.URL + prefix, ua: ua}
}

func (c *client) do(method, path, body string, hdr map[string]string) resp {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	require.NoError(c.t, err)
	req.Header.Set("User-Agent", c.ua)
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	if method == http.MethodPost && body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	require.NoError(c.t, err)
	require.True(c.t, res.StatusCode < 300 || res.StatusCode == http.StatusNotModified, "%s %s: no redirects, no errors: %d %s", method, path, res.StatusCode, b)
	return resp{res.StatusCode, res.Header, string(b)}
}

// doAny is do without the status assertion.
func (c *client) doAny(method, path, body string, hdr map[string]string) resp {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	require.NoError(c.t, err)
	req.Header.Set("User-Agent", c.ua)
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	if method == http.MethodPost && body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	require.NoError(c.t, err)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, res.Header, string(b)}
}

func (c *client) get(path string) resp { c.t.Helper(); return c.do(http.MethodGet, path, "", nil) }
func (c *client) post(path, body string) resp {
	c.t.Helper()
	return c.do(http.MethodPost, path, body, nil)
}

// loginBody is ClientLogin's form, encoded like URLQueryItem does (%20 -> +).
func loginBody() string {
	return "Email=" + testUser + "&Passwd=" + url.QueryEscape(testPass)
}

// nnwLogin does NNW's ClientLogin and parses the response the way NNW does:
// every line split on every '=' and accepted only with exactly two parts.
func (c *client) nnwLogin() string {
	c.t.Helper()
	r := c.do(http.MethodPost, "/accounts/ClientLogin", loginBody(), nil)
	require.Equal(c.t, 200, r.code)
	require.Contains(c.t, r.header.Get("Content-Type"), "text/plain")
	auth := ""
	for _, line := range strings.Split(strings.TrimSpace(r.body), "\n") {
		parts := strings.Split(line, "=")
		require.Len(c.t, parts, 2, "NNW rejects a line with a stray '=': %q", line)
		if parts[0] == "Auth" {
			auth = parts[1]
		}
	}
	require.NotEmpty(c.t, auth)
	c.auth = "GoogleLogin auth=" + auth
	return auth
}

func decodeIDs(t *testing.T, body string) (ids []string, cont *string) {
	t.Helper()
	var v struct {
		ItemRefs []struct {
			ID *string `json:"id"` // NNW: String?, used verbatim
		} `json:"itemRefs"`
		Continuation *string `json:"continuation"` // must be a string when present
	}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&v), body)
	for _, r := range v.ItemRefs {
		require.NotNil(t, r.ID)
		_, err := strconv.ParseInt(*r.ID, 10, 64)
		require.NoError(t, err, "NNW/Reeder convert the decimal string with Int()")
		ids = append(ids, *r.ID)
	}
	return ids, v.Continuation
}

// nnwContents decodes a contents response with NNW's required keys.
func nnwContents(t *testing.T, body string) []itemJSON {
	t.Helper()
	var v struct {
		ID      *string `json:"id"`
		Updated *int64  `json:"updated"`
		Items   []struct {
			ID      *string `json:"id"`
			Summary *struct {
				Content *string `json:"content"`
			} `json:"summary"`
			Categories []string `json:"categories"`
			Origin     *struct {
				StreamID *string `json:"streamId"`
			} `json:"origin"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &v))
	require.NotNil(t, v.ID, "envelope id (String)")
	require.NotNil(t, v.Updated, "envelope updated (Int)")
	for i, it := range v.Items {
		require.NotNil(t, it.ID, "item %d id", i)
		require.NotNil(t, it.Summary, "item %d summary (object)", i)
		require.NotNil(t, it.Summary.Content)
		require.NotNil(t, it.Categories, "item %d categories ([String])", i)
		require.NotNil(t, it.Origin, "item %d origin (object)", i)
		require.NotNil(t, it.Origin.StreamID, "items without origin.streamId are dropped by NNW")
	}
	var full struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal([]byte(body), &full))
	return full.Items
}

func TestContractNetNewsWireSequence(t *testing.T) {
	h := newHarness(t)
	c := newClient(t, h, base, "NetNewsWire (RSS Reader; https://netnewswire.com/)")

	news := h.addFeed("https://news.example/feed.xml", "News", "News & Politics+")
	tech := h.addFeed("https://tech.example/feed.xml", "Tech", "Tech")
	other := h.addFeed("http://old.example/feed.xml", "Legacy", "")
	now := h.clk.Now().Unix()
	ot := now - 90*86400 // NNW: Date minus 3 months when it has no stored start time

	// 600 items crawled inside the window (one a minute), and two much older
	// ones whose content changed after ot.
	recent := make([]int64, 600)
	for i := range recent {
		feed := news
		if i%3 == 0 {
			feed = tech
		}
		recent[i] = h.addItem(feed, itemSeed{ID: (now - int64(600-i)*60) * 1_000_000, Title: fmt.Sprintf("recent %d", i)})
	}
	oldChanged1 := h.addItem(tech, itemSeed{ID: (ot - 30*86400) * 1_000_000, Title: "old changed 1", ChangedAt: now - 3600})
	oldChanged2 := h.addItem(news, itemSeed{ID: (ot - 40*86400) * 1_000_000, Title: "old changed 2", ChangedAt: now - 7200})
	oldQuiet := h.addItem(news, itemSeed{ID: (ot - 50*86400) * 1_000_000, Title: "old quiet"})
	starredID := h.addItem(tech, itemSeed{ID: (ot - 60*86400) * 1_000_000, Title: "starred old", Starred: true})
	_ = oldQuiet

	// 1. ClientLogin, 2. token.
	tok := c.nnwLogin()
	r := c.get("/reader/api/0/token")
	require.Equal(t, "GoogleLogin auth="+strings.TrimSuffix(r.body, "\n"), "GoogleLogin auth="+tok)
	require.True(t, strings.HasSuffix(r.body, "\n"), "NNW strips exactly one trailing newline")

	// 3-4. tag/list and subscription/list: 200 then 304 with the stored ETag.
	tags := c.get("/reader/api/0/tag/list?output=json")
	require.Equal(t, 200, tags.code)
	require.Equal(t, 304, c.do("GET", "/reader/api/0/tag/list?output=json", "", map[string]string{"If-None-Match": tags.header.Get("ETag")}).code)
	subs := c.get("/reader/api/0/subscription/list?output=json")
	subsETag := subs.header.Get("ETag")
	require.Equal(t, 304, c.do("GET", "/reader/api/0/subscription/list?output=json", "", map[string]string{"If-None-Match": subsETag}).code)
	var sl struct {
		Subscriptions []struct {
			ID         *string `json:"id"`
			Categories []struct {
				ID    string `json:"id"`
				Label string `json:"label"`
			} `json:"categories"`
		} `json:"subscriptions"`
	}
	require.NoError(t, json.Unmarshal([]byte(subs.body), &sl))
	require.Len(t, sl.Subscriptions, 3)
	for _, s := range sl.Subscriptions {
		require.NotNil(t, s.ID)
		require.Len(t, s.Categories, 1)
	}

	// 5. reading-list ids since ot with n=1000: the first page includes the
	// content-changed older items and there is no continuation.
	first := c.get(fmt.Sprintf("/reader/api/0/stream/items/ids?s=%s&ot=%d&n=1000&output=json", rl, ot))
	require.NotEmpty(t, first.header.Get("Date"), "NNW takes its next ot from the Date header")
	ids, cont := decodeIDs(t, first.body)
	require.Nil(t, cont)
	require.Len(t, ids, 602)
	require.Contains(t, ids, FormatDecimal(oldChanged1))
	require.Contains(t, ids, FormatDecimal(oldChanged2))
	require.NotContains(t, ids, FormatDecimal(oldQuiet))
	// With n=250 NNW loops on the continuation until it is absent, never seeing an empty page.
	var loop []string
	cc, pages := "", 0
	for {
		path := fmt.Sprintf("/reader/api/0/stream/items/ids?s=%s&ot=%d&n=250&output=json", rl, ot)
		if cc != "" {
			path += "&c=" + cc
		}
		page, next := decodeIDs(t, c.get(path).body)
		require.NotEmpty(t, page, "no empty trailing page")
		loop = append(loop, page...)
		pages++
		if next == nil {
			break
		}
		cc = *next
		require.Less(t, pages, 10)
	}
	require.Equal(t, ids, loop)
	require.Equal(t, 3, pages)

	// 6-7. unread ids (no ot) and starred ids.
	unread, _ := decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+rl+"&xt="+readSt+"&n=1000&output=json").body)
	require.Len(t, unread, 604)
	stars, _ := decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+starred+"&n=1000&output=json").body)
	require.Equal(t, []string{FormatDecimal(starredID)}, stars)
	feedOT, _ := decodeIDs(t, c.get(fmt.Sprintf("/reader/api/0/stream/items/ids?s=feed/%d&ot=%d&n=1000&output=json", news, ot)).body)
	require.NotEmpty(t, feedOT)

	// 8. POST contents: 150 long-form ids including 3 trimmed and 2 unknown.
	req150 := make([]int64, 145)
	copy(req150, recent[:145])
	trimmed := []int64{recent[10], recent[20], recent[30]}
	h.trim(trimmed[0], true)
	h.trim(trimmed[1], true)
	h.trim(trimmed[2], false)
	req150 = append(req150, 111, 222) // unknown
	var form strings.Builder
	form.WriteString("T=" + tok + "&output=json")
	for _, id := range req150 {
		form.WriteString("&i=" + FormatLongID(id)) // NNW leaves ':' ',' and '/' unencoded
	}
	got := c.post("/reader/api/0/stream/items/contents", form.String())
	items := nnwContents(t, got.body)
	require.Len(t, items, 145-3, "trimmed and unknown ids are omitted")

	// 9. edit-tag in four passes over 1000-style batches, including ledger and unknown ids.
	var edit strings.Builder
	edit.WriteString("T=" + tok)
	for _, id := range req150 {
		edit.WriteString("&i=" + FormatLongID(id))
	}
	for _, pass := range []string{"a=" + readSt, "r=" + readSt, "a=" + starred, "r=" + starred} {
		r := c.post("/reader/api/0/edit-tag", edit.String()+"&"+pass)
		require.Equal(t, "OK", r.body, pass)
	}
	before := q[int](h, "SELECT count(*) FROM items WHERE read = 1")
	c.post("/reader/api/0/edit-tag", edit.String()+"&r="+starred) // replay
	require.Equal(t, before, q[int](h, "SELECT count(*) FROM items WHERE read = 1"))

	// 10. star a trimmed id that still has a stub: restored and listed.
	fresh := recent[40]
	h.trim(fresh, true)
	c.post("/reader/api/0/edit-tag", "T="+tok+"&i="+FormatLongID(fresh)+"&a="+starred)
	stars, _ = decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+starred+"&n=1000&output=json").body)
	require.ElementsMatch(t, []string{FormatDecimal(starredID), FormatDecimal(fresh)}, stars)

	// 11. A stale T is 401 (NNW then re-fetches the token and retries).
	stale := c.doAny("POST", "/reader/api/0/edit-tag", "T=stale&i="+FormatLongID(recent[1])+"&a="+readSt, nil)
	require.Equal(t, 401, stale.code)
	tok2 := strings.TrimSuffix(c.get("/reader/api/0/token").body, "\n")
	require.Equal(t, "OK", c.post("/reader/api/0/edit-tag", "T="+tok2+"&i="+FormatLongID(recent[1])+"&a="+readSt).body)

	// 12. quickadd a new URL, then re-list with the OLD ETag: 200 with the new feed.
	qa := c.post("/reader/api/0/subscription/quickadd", "T="+tok+"&quickadd="+url.QueryEscape("https://brand-new.example/atom"))
	var qaRes struct {
		NumResults int     `json:"numResults"`
		StreamID   *string `json:"streamId"`
	}
	require.NoError(t, json.Unmarshal([]byte(qa.body), &qaRes))
	require.Equal(t, 1, qaRes.NumResults)
	require.NotNil(t, qaRes.StreamID)
	relist := c.do("GET", "/reader/api/0/subscription/list?output=json", "", map[string]string{"If-None-Match": subsETag})
	require.Equal(t, 200, relist.code)
	require.Contains(t, relist.body, `"id":"`+*qaRes.StreamID+`"`)

	// 13. quickadd of the https form of a feed stored as http: existing streamId.
	qa = c.post("/reader/api/0/subscription/quickadd", "T="+tok+"&quickadd="+url.QueryEscape("https://old.example/feed.xml"))
	require.NoError(t, json.Unmarshal([]byte(qa.body), &qaRes))
	require.Equal(t, 1, qaRes.NumResults)
	require.Equal(t, feedID(other), *qaRes.StreamID)

	// 14-15. Move with a=label (creates the folder); r= without a= goes to Uncategorized.
	c.post("/reader/api/0/subscription/edit", "T="+tok+"&ac=edit&s="+feedID(other)+"&t=Legacy;Feed&a=user/-/label/New")
	require.Equal(t, "New", q[string](h, "SELECT fo.name FROM feeds f JOIN folders fo ON fo.id = f.folder_id WHERE f.id = ?", other))
	require.Equal(t, "Legacy;Feed", q[string](h, "SELECT custom_title FROM feeds WHERE id = ?", other))
	c.post("/reader/api/0/subscription/edit", "T="+tok+"&ac=edit&s="+feedID(other)+"&r=user/-/label/New")
	require.Equal(t, int64(1), q[int64](h, "SELECT folder_id FROM feeds WHERE id = ?", other))

	// 16. rename-tag, then the merge case.
	c.post("/reader/api/0/rename-tag", "T="+tok+"&s=user/-/label/Tech&dest=user/-/label/Technology")
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Technology'"))
	c.post("/reader/api/0/rename-tag", "T="+tok+"&s=user/-/label/Technology&dest=user/-/label/New")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Technology'"))
	require.Equal(t, "New", q[string](h, "SELECT fo.name FROM feeds f JOIN folders fo ON fo.id = f.folder_id WHERE f.id = ?", tech))

	// 17. disable-tag with the raw, unencoded body.
	d := c.post("/reader/api/0/disable-tag", "T="+tok+"&s=user/-/label/News & Politics+")
	require.Equal(t, "OK", d.body)
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'News & Politics+'"))
	require.Equal(t, int64(1), q[int64](h, "SELECT folder_id FROM feeds WHERE id = ?", news))

	// 18-19. subscription/import with ';' and '&' in titles; a folder name with ';' survives.
	imp := c.do("POST", "/reader/api/0/subscription/import",
		`<opml version="2.0"><body><outline text="Q&amp;A; Misc"><outline type="rss" text="A &amp; B; C" xmlUrl="https://imp.example/rss"/></outline></body></opml>`,
		map[string]string{"Content-Type": "text/xml"})
	require.Equal(t, 200, imp.code)
	require.Contains(t, c.get("/reader/api/0/tag/list?output=json").body, `"user/-/label/Q&A; Misc"`)

	// 20. Unsubscribe a feed that has starred items: they stay in the starred list.
	c.post("/reader/api/0/subscription/edit", "T="+tok+"&ac=unsubscribe&s="+feedID(tech))
	stars, _ = decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+starred+"&n=1000&output=json").body)
	require.Contains(t, stars, FormatDecimal(starredID))
	require.Contains(t, stars, FormatDecimal(fresh))
	sub := c.get("/reader/api/0/subscription/list?output=json").body
	c2 := nnwContents(t, c.post("/reader/api/0/stream/items/contents", "T="+tok+"&i="+FormatLongID(starredID)).body)
	require.Len(t, c2, 1)
	require.Contains(t, sub, `"id":"`+c2[0].Origin.StreamID+`"`, "origin.streamId is the archive subscription")
	require.NotEqual(t, feedID(tech), c2[0].Origin.StreamID)
}

// reederSequence replays the reconstructed Reeder Classic sync against one mount.
func reederSequence(t *testing.T, prefix string) {
	h := newHarness(t)
	c := newClient(t, h, prefix, "Reeder/5.4 CFNetwork/1494 Darwin/23.4.0")
	f := h.addFeed("https://a.example/f", "Alpha", "Comics")
	f2 := h.addFeed("https://b.example/f", "", "") // no site title yet, no icon
	now := h.clk.Now().Unix()

	var all []int64
	for i := 0; i < 120; i++ {
		feed := f
		if i%4 == 0 {
			feed = f2
		}
		all = append(all, h.addItem(feed, itemSeed{ID: (now - int64(120-i)*600) * 1_000_000, Title: fmt.Sprintf("post %d", i), Starred: i%40 == 0, Read: i < 20}))
	}

	// 1. ClientLogin (Reeder only needs the Auth line), 2. user-info right after.
	login := c.do("POST", "/accounts/ClientLogin", loginBody(), nil)
	require.Equal(t, 200, login.code)
	var tok string
	for _, line := range strings.Split(login.body, "\n") {
		if v, ok := strings.CutPrefix(line, "Auth="); ok {
			tok = v
		}
	}
	require.NotEmpty(t, tok)
	c.auth = "GoogleLogin auth=" + tok
	ui := c.get("/reader/api/0/user-info?output=json")
	var uinfo map[string]any
	require.NoError(t, json.Unmarshal([]byte(ui.body), &uinfo))
	for _, k := range []string{"userId", "userName", "userProfileId", "userEmail"} {
		require.IsType(t, "", uinfo[k], k)
	}

	// 3. subscription/list: iconUrl and htmlUrl are strings on every subscription.
	subs := c.get("/reader/api/0/subscription/list?output=json")
	var sl struct {
		Subscriptions []map[string]any `json:"subscriptions"`
	}
	require.NoError(t, json.Unmarshal([]byte(subs.body), &sl))
	require.Len(t, sl.Subscriptions, 2)
	for _, s := range sl.Subscriptions {
		for _, k := range []string{"id", "title", "url", "htmlUrl", "iconUrl"} {
			require.IsType(t, "", s[k], k)
		}
		require.IsType(t, []any{}, s["categories"])
	}
	walkNoNull(t, decodeAny(t, []byte(subs.body)), "$", nil)

	// 4-6. The three id lists Reeder pulls with n=10000.
	unread, cont := decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+rl+"&xt="+readSt+"&output=json&n=10000").body)
	require.Nil(t, cont)
	require.Len(t, unread, 100)
	stars, _ := decodeIDs(t, c.get("/reader/api/0/stream/items/ids?s="+starred+"&output=json&n=10000").body)
	require.Len(t, stars, 3)
	recentRead, _ := decodeIDs(t, c.get(fmt.Sprintf("/reader/api/0/stream/items/ids?s=%s&ot=%d&output=json&n=10000", readSt, now-30*86400)).body)
	require.Len(t, recentRead, 20)

	// 7. token; 8. POST contents with 100 bare-hex ids in two batches.
	require.Equal(t, tok+"\n", c.get("/reader/api/0/token").body)
	seen := map[string]bool{}
	for batch := 0; batch < 2; batch++ {
		var b strings.Builder
		b.WriteString("output=json")
		for _, id := range all[batch*50 : batch*50+50] {
			b.WriteString("&i=" + FormatHex16(id))
		}
		for _, it := range nnwContents(t, c.post("/reader/api/0/stream/items/contents", b.String()).body) {
			seen[it.ID] = true
			require.Regexp(t, `^tag:google\.com,2005:reader/item/[0-9a-f]{16}$`, it.ID)
		}
	}
	require.Len(t, seen, 100)

	// 9-10. Single-id a=read requests (article opens and scroll marks): under the
	// default settings they write no stats rows at all.
	for i := 0; i < 5; i++ {
		id := all[30+i]
		require.Equal(t, "OK", c.post("/reader/api/0/edit-tag", "T="+tok+"&i="+FormatHex16(id)+"&a="+readSt).body)
		require.True(t, isRead(h, id))
	}
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events WHERE kind = 'open'"))

	// 11. r=starred; 12. T=x with a valid header is accepted.
	require.Equal(t, "OK", c.post("/reader/api/0/edit-tag", "T=x&i="+FormatHex16(all[40])+"&r="+starred).body)
	require.False(t, isStarred(h, all[40]))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events"))

	// 13. mark-all-as-read for one feed with ts in seconds, milliseconds and microseconds.
	cut := now - 30*600 // 30 items back from now
	for _, ts := range []string{strconv.FormatInt(cut, 10), strconv.FormatInt(cut*1000, 10), strconv.FormatInt(cut*1_000_000, 10)} {
		require.NoError(t, execSQL(h, "UPDATE items SET read = 0 WHERE feed_id = ?", f))
		require.Equal(t, "OK", c.post("/reader/api/0/mark-all-as-read", "T="+tok+"&s=feed/"+strconv.FormatInt(f, 10)+"&ts="+ts).body, ts)
		require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE feed_id = ? AND read = 0 AND id <= ?", f, cut*1_000_000), ts)
		require.Greater(t, q[int](h, "SELECT count(*) FROM items WHERE feed_id = ? AND read = 0 AND id > ?", f, cut*1_000_000), 0, "nothing newer is touched: %s", ts)
	}
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events"))
}

func TestContractReederSequence(t *testing.T) {
	t.Run("api prefix", func(t *testing.T) { reederSequence(t, base) })
	t.Run("root mount", func(t *testing.T) { reederSequence(t, "") })
	t.Run("doubled prefix", func(t *testing.T) { reederSequence(t, base+base) })
	t.Run("double slash", func(t *testing.T) { reederSequence(t, base+"/") })
}
