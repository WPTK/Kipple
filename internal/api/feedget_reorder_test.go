package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetFeedReturnsTheDetailPatchReturns(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id)
	code, _, _ := h.api(c, "PATCH", path, `{"custom_title":"Mine","http_auth":"bob:s3cret","user_agent":"Foo/1","retention":500,"fulltext":true}`)
	require.Equal(t, 200, code)

	code, got, rec := h.api(c, "GET", path, "")
	require.Equal(t, 200, code)
	require.NotContains(t, rec.Body.String(), "s3cret")
	require.NotContains(t, got, "http_auth")
	require.Equal(t, true, got["has_http_auth"])
	require.Equal(t, "Mine", got["custom_title"])
	require.Equal(t, "Foo/1", got["user_agent"])
	require.Equal(t, sid(id), got["id"])
	for _, k := range []string{"url", "url_original", "position", "dedup_mode", "next_fetch_at", "starred_count", "retention", "fulltext", "interval_minutes", "enabled"} {
		require.Contains(t, got, k)
	}
	// identical to what a PATCH answers
	_, patched, _ := h.api(c, "PATCH", path, `{}`)
	require.Equal(t, patched, got)
}

func TestGetFeedErrorsAndAuth(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	require.Equal(t, http.StatusUnauthorized, h.do("GET", "/api/feeds/1", "").Code)
	for _, p := range []string{"/api/feeds/999", "/api/feeds/abc", "/api/feeds/0"} {
		code, _, _ := h.api(c, "GET", p, "")
		require.Equal(t, 404, code, p)
	}
	h.exec(`INSERT INTO feeds (folder_id, url, url_key, host, enabled, disabled_reason, retention) VALUES (1,'kipple:archive','kipple:archive','kipple.invalid',0,'archive',0)`)
	var arch int64
	require.NoError(t, h.db.Reader().QueryRow("SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&arch))
	code, body, _ := h.api(c, "GET", "/api/feeds/"+sid(arch), "")
	require.Equal(t, 409, code)
	require.Equal(t, "archive_feed", body["error"])
}

func positions(h *harness, table string) map[int64][2]int64 {
	h.t.Helper()
	q := "SELECT id, position, ifnull(parent_id, 0) FROM folders"
	if table == "feeds" {
		q = "SELECT id, position, folder_id FROM feeds"
	}
	rows, err := h.db.Reader().Query(q)
	require.NoError(h.t, err)
	defer rows.Close()
	out := map[int64][2]int64{}
	for rows.Next() {
		var id, p, f int64
		require.NoError(h.t, rows.Scan(&id, &p, &f))
		out[id] = [2]int64{p, f}
	}
	return out
}

func TestReorderSetsFolderAndFeedPositionsAtomically(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f2, f3 := h.addFolder("Two"), h.addFolder("Three")
	a := h.storeFeed("https://a.example/f")
	b := h.storeFeed("https://b.example/f")
	d := h.storeFeed("https://d.example/f")
	sub := h.events()

	body := `{"folders":["` + sid(f3) + `",` + sid(1) + `,"` + sid(f2) + `"],
		"feeds":[{"folder_id":"` + sid(f2) + `","ids":["` + sid(d) + `","` + sid(a) + `"]},{"folder_id":` + sid(f3) + `,"ids":["` + sid(b) + `"]}]}`
	code, resp, _ := h.api(c, "POST", "/api/reorder", body)
	require.Equal(t, 200, code, resp)

	fp := positions(h, "folders")
	require.Equal(t, int64(0), fp[f3][0])
	require.Equal(t, int64(1), fp[1][0])
	require.Equal(t, int64(2), fp[f2][0])
	ep := positions(h, "feeds")
	require.Equal(t, [2]int64{0, f2}, ep[d])
	require.Equal(t, [2]int64{1, f2}, ep[a])
	require.Equal(t, [2]int64{0, f3}, ep[b])
	require.ElementsMatch(t, []any{sid(a), sid(b), sid(d)}, resp["changed_feeds"])
	require.Len(t, feedChanged(t, sub), 3)

	// repeating it changes nothing and announces nothing
	code, resp, _ = h.api(c, "POST", "/api/reorder", body)
	require.Equal(t, 200, code)
	require.Empty(t, resp["changed_feeds"])
	require.Empty(t, resp["changed_folders"])
	require.Empty(t, feedChanged(t, sub))
}

func TestReorderInvalidInputWritesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f2 := h.addFolder("Two")
	a := h.storeFeed("https://a.example/f")
	b := h.storeFeed("https://b.example/f")
	h.exec(`INSERT INTO feeds (folder_id, url, url_key, host, enabled, disabled_reason, retention) VALUES (1,'kipple:archive','kipple:archive','kipple.invalid',0,'archive',0)`)
	var arch int64
	require.NoError(t, h.db.Reader().QueryRow("SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&arch))
	beforeF, beforeE := positions(h, "folders"), positions(h, "feeds")
	sub := h.events()

	good := `"folders":[` + sid(f2) + `,` + sid(1) + `],"feeds":[{"folder_id":` + sid(f2) + `,"ids":[` + sid(b) + `,` + sid(a) + `]},`
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"empty":           {`{}`, 400},
		"unknown field":   {`{"x":1}`, 400},
		"dup folder":      {`{"folders":[1,1]}`, 400},
		"unknown folder":  {`{"folders":[` + sid(f2) + `,999]}`, 400},
		"dup feed":        {`{` + good + `{"folder_id":1,"ids":[` + sid(a) + `]}]}`, 400},
		"unknown feed":    {`{` + good + `{"folder_id":1,"ids":[999]}]}`, 400},
		"unknown target":  {`{` + good + `{"folder_id":999,"ids":[]}]}`, 400},
		"bad id":          {`{"folders":["x"]}`, 400},
		"archive feed":    {`{` + good + `{"folder_id":1,"ids":[` + sid(arch) + `]}]}`, 409},
		"same list twice": {`{"feeds":[{"folder_id":1,"ids":[` + sid(a) + `]},{"folder_id":1,"ids":[` + sid(b) + `]}]}`, 400},
	} {
		code, _, _ := h.api(c, "POST", "/api/reorder", tc.body)
		require.Equal(t, tc.code, code, name)
		require.Equal(t, beforeF, positions(h, "folders"), name)
		require.Equal(t, beforeE, positions(h, "feeds"), name)
	}
	require.Empty(t, feedChanged(t, sub))
	require.Equal(t, http.StatusUnauthorized, h.do("POST", "/api/reorder", "{}").Code)
	rec := h.do("POST", "/api/reorder", "{}", withCookie(c), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestSSEHeartbeatEventHasNoIDAndDoesNotAdvanceLastEventID(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	ts := sseServer(t, h)
	br, closeBody := openStream(t, ts, c, "")
	defer closeBody()
	readUntil(t, br, ": connected", 2*time.Second)
	last := h.hub.LastID()
	for i := 0; i < 2; i++ {
		require.Equal(t, "event: heartbeat\n", readUntil(t, br, "event:", 2*time.Second))
		next, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Regexp(t, `^data: \{"t":\d+\}\n$`, next, "no id line between event and data")
	}
	require.Equal(t, last, h.hub.LastID())
}
