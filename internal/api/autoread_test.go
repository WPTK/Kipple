package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// arItem inserts an unread item crawled `age` before the harness clock.
func (h *harness) arItem(feed int64, age time.Duration) int64 {
	h.t.Helper()
	id := h.clk.Now().Add(-age).UnixMicro() + h.seeded.Add(1)
	h.exec(`INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, url, title, author)
		VALUES (?1, ?2, 1, 1, 'ar' || ?1, 'c', 't', 'https://x.example/a', 'old one', '')`, id, feed)
	return id
}

func (h *harness) isRead(id int64) bool {
	return h.count("SELECT read FROM items WHERE id = ?", id) == 1
}

func (h *harness) waitAutoReadIdle() {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return h.srv.autoReadStatus() == nil }, 5*time.Second, 10*time.Millisecond)
}

const arDay = 24 * time.Hour

func TestAutoReadRoutesRequireSessionAndOrigin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/library/auto-read/preview"}, {"POST", "/api/library/auto-read/run"},
		{"GET", "/api/saved-searches"}, {"POST", "/api/saved-searches"}, {"POST", "/api/saved-searches/reorder"},
		{"PATCH", "/api/saved-searches/x"}, {"DELETE", "/api/saved-searches/x"},
	} {
		require.Equal(t, http.StatusUnauthorized, h.do(r.method, r.path, "{}").Code, "%s %s", r.method, r.path)
		if r.method == "GET" {
			continue
		}
		rec := h.do(r.method, r.path, "{}", withCookie(c), func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "cross-site") })
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s", r.method, r.path)
	}
}

func TestAutoReadSettingAndFeedPatchValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)

	code, out, _ := h.api(c, "PATCH", "/api/settings", `{"library.auto_read_days":30}`)
	require.Equal(t, 200, code, out)
	require.EqualValues(t, 30, out["values"].(map[string]any)["library.auto_read_days"])
	for _, bad := range []string{`366`, `-1`, `1.5`, `"7"`, `true`} {
		code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.auto_read_days":`+bad+`}`)
		require.Equal(t, 400, code, bad)
		require.Equal(t, []any{"library.auto_read_days"}, out["keys"])
	}
	code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.auto_read_days":null}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, out["values"].(map[string]any)["library.auto_read_days"], "the default is off")
	def := settingDefByKey["library.auto_read_days"]
	require.Equal(t, "Mark old articles as read after…", def.Label)
	require.Equal(t, "days", def.Unit)
	require.Equal(t, groupLibrary, def.Group)
	require.Equal(t, surfaceSettings, def.Surface)
	require.NotEmpty(t, def.Description)

	for _, tc := range []struct {
		body string
		ok   bool
		want any
	}{
		{`{"auto_read_days":14}`, true, float64(14)},
		{`{"auto_read_days":0}`, true, float64(0)},
		{`{"auto_read_days":365}`, true, float64(365)},
		{`{"auto_read_days":null}`, true, nil},
		{`{"auto_read_days":366}`, false, nil},
		{`{"auto_read_days":-1}`, false, nil},
		{`{"auto_read_days":"7"}`, false, nil},
	} {
		code, _, _ = h.api(c, "PATCH", "/api/feeds/"+sid(feed), tc.body)
		if !tc.ok {
			require.Equal(t, 400, code, tc.body)
			continue
		}
		require.Equal(t, 200, code, tc.body)
		code, out, _ = h.api(c, "GET", "/api/feeds/"+sid(feed), "")
		require.Equal(t, 200, code)
		require.Equal(t, tc.want, out["auto_read_days"], tc.body)
	}
	h.exec("UPDATE feeds SET auto_read_days = 9 WHERE id = ?", feed)
	_, out, _ = h.api(c, "GET", "/api/bootstrap", "")
	fs := out["feeds"].([]any)
	require.EqualValues(t, 9, fs[0].(map[string]any)["auto_read_days"], "bootstrap feed objects report it")
}

func TestAutoReadPreviewAndRunWithConfirm(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	a := h.addFeed("A", 0)
	b := h.addFeed("B", 0)
	h.exec("UPDATE feeds SET auto_read_days = 10 WHERE id = ?", a)
	var aOld []int64
	for range 60 {
		aOld = append(aOld, h.arItem(a, 20*arDay))
	}
	for range 50 {
		h.arItem(b, 40*arDay)
	}
	young := h.arItem(a, 2*arDay)
	starred := h.arItem(a, 30*arDay)
	h.exec("UPDATE items SET starred = 1 WHERE id = ?", starred)

	// B has no threshold of its own and the global default is off: preview shows only A.
	code, out, _ := h.api(c, "POST", "/api/library/auto-read/preview", `{}`)
	require.Equal(t, 200, code, out)
	require.EqualValues(t, 60, out["total"])
	feeds := out["feeds"].([]any)
	require.Len(t, feeds, 1)
	require.Equal(t, map[string]any{"feed_id": sid(a), "title": "A", "days": float64(10), "count": float64(60)}, feeds[0])
	require.EqualValues(t, 0, out["global_days"])

	// "What if the default were 30 days": B's 50 join, and nothing is stored.
	_, out, _ = h.api(c, "POST", "/api/library/auto-read/preview", `{"days":30}`)
	require.EqualValues(t, 110, out["total"])
	require.Zero(t, h.count("SELECT count(*) FROM settings WHERE key = 'library.auto_read_days'"))
	_, out, _ = h.api(c, "POST", "/api/library/auto-read/preview", `{"feed_id":"`+sid(b)+`","days":30}`)
	require.EqualValues(t, 50, out["total"])
	code, _, _ = h.api(c, "POST", "/api/library/auto-read/preview", `{"feed_id":"99999"}`)
	require.Equal(t, 404, code)
	for _, bad := range []string{`{"days":366}`, `{"days":"3"}`, `{"feed_id":"x"}`, `{"nope":1}`, `{"confirm":true}`} {
		code, _, _ = h.api(c, "POST", "/api/library/auto-read/preview", bad)
		require.Equal(t, 400, code, bad)
	}

	// Over 100 needs confirm (a first enable never mass-marks silently), and nothing changed yet.
	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", `{"days":30}`)
	require.Equal(t, 409, code)
	require.Equal(t, "confirm_required", out["error"])
	require.EqualValues(t, 110, out["total"])
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE read = 1"))

	// At or under 100 it goes without: only A's 60.
	sub := h.hub.Subscribe(h.hub.LastID())
	defer sub.Close()
	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", `{}`)
	require.Equal(t, 202, code, out)
	require.Equal(t, "auto_read", out["kind"])
	require.EqualValues(t, 60, out["total"])
	evs := collect(t, sub, "run.done", 5*time.Second)
	start := ofType(evs, "run.start")
	require.Len(t, start, 1)
	require.Equal(t, "auto_read", start[0]["kind"])
	done := ofType(evs, "run.done")[0]
	require.EqualValues(t, 60, done["changed"])
	require.Equal(t, out["id"], done["run_id"])
	var marked []string
	for _, st := range ofType(evs, "items.state") {
		require.Equal(t, true, st["read"])
		require.Equal(t, "auto_read", st["source"])
		marked = append(marked, anyStrs(st["ids"])...)
	}
	require.ElementsMatch(t, strs(aOld...), marked)
	h.waitAutoReadIdle()

	require.False(t, h.isRead(young) || h.isRead(starred))
	require.Equal(t, 60, h.count("SELECT count(*) FROM items WHERE read = 1"))

	// An article the reader marks unread again is re-marked only by an explicit catch-up, which
	// the preview counts too.
	h.exec("UPDATE items SET read = 0 WHERE id = ?", aOld[0])
	_, out, _ = h.api(c, "POST", "/api/library/auto-read/preview", `{}`)
	require.EqualValues(t, 1, out["total"])

	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", `{"days":30,"confirm":true}`)
	require.Equal(t, 202, code, out)
	h.waitAutoReadIdle()
	require.Equal(t, 60+50, h.count("SELECT count(*) FROM items WHERE read = 1"))
}

func TestAutoReadRunIsOneAtATime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.exec("UPDATE feeds SET auto_read_days = 1 WHERE id = ?", feed)
	h.arItem(feed, 5*arDay)
	h.srv.autoRead.mu.Lock()
	h.srv.autoRead.run = &autoReadRun{ID: 5, Kind: runKindAutoRead, Total: 3}
	h.srv.autoRead.mu.Unlock()
	code, out, _ := h.api(c, "POST", "/api/library/auto-read/run", `{}`)
	require.Equal(t, 409, code)
	require.Equal(t, "busy", out["error"])
	_, out, _ = h.api(c, "GET", "/api/bootstrap", "")
	var found map[string]any
	for _, r := range out["runs"].([]any) {
		if m := r.(map[string]any); m["kind"] == "auto_read" {
			found = m
		}
	}
	require.NotNil(t, found, "bootstrap runs lists the active catch-up")
	require.Equal(t, "5", found["id"])
	h.srv.autoRead.mu.Lock()
	h.srv.autoRead.run = nil
	h.srv.autoRead.mu.Unlock()
	code, _, _ = h.api(c, "POST", "/api/library/auto-read/run", `{}`)
	require.Equal(t, 202, code)
	h.waitAutoReadIdle()
}

// expect_total guards a run against a library that moved on since the preview.
func TestAutoReadRunExpectTotal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.exec("UPDATE feeds SET auto_read_days = 1 WHERE id = ?", feed)
	for i := 0; i < 130; i++ {
		h.arItem(feed, 5*arDay)
	}
	body := func(expect any) string {
		b, _ := json.Marshal(map[string]any{"confirm": true, "expect_total": expect})
		return string(b)
	}
	// 130 recounted against 0 expected: over by more than max(100, 10%): nothing is marked.
	code, out, _ := h.api(c, "POST", "/api/library/auto-read/run", body(0))
	require.Equal(t, 409, code, out)
	require.Equal(t, "total_changed", out["error"])
	require.EqualValues(t, 130, out["total"])
	require.EqualValues(t, 0, out["expect_total"])
	require.NotEmpty(t, out["message"])
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE read = 1"))
	// 130 against 30: exactly 100 over is allowed; against 29 it is 101 over.
	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", body(29))
	require.Equal(t, 409, code, out)
	require.Equal(t, "total_changed", out["error"])
	// Bad values.
	for _, bad := range []string{`{"expect_total":-1}`, `{"expect_total":"x"}`} {
		code, _, _ = h.api(c, "POST", "/api/library/auto-read/run", bad)
		require.Equal(t, 400, code, bad)
	}
	code, _, _ = h.api(c, "POST", "/api/library/auto-read/preview", `{"expect_total":1}`)
	require.Equal(t, 400, code, "preview has no expect_total")
	// The confirm rule stays: 130 is over 100, so an unconfirmed run still needs it.
	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", `{"expect_total":130}`)
	require.Equal(t, 409, code)
	require.Equal(t, "confirm_required", out["error"])
	// Within the tolerance (and confirmed) it runs; null expects nothing.
	code, out, _ = h.api(c, "POST", "/api/library/auto-read/run", body(30))
	require.Equal(t, 202, code, out)
	h.waitAutoReadIdle()
	require.Equal(t, 130, h.count("SELECT count(*) FROM items WHERE read = 1"))
}
