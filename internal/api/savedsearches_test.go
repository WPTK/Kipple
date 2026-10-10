package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (h *harness) saved(c *http.Cookie, body string) map[string]any {
	h.t.Helper()
	code, out, _ := h.api(c, "POST", "/api/saved-searches", body)
	require.Equal(h.t, 201, code, "%v", out)
	return out
}

func savedList(t *testing.T, h *harness, c *http.Cookie, query string) []map[string]any {
	t.Helper()
	code, out, _ := h.api(c, "GET", "/api/saved-searches"+query, "")
	require.Equal(t, 200, code)
	var list []map[string]any
	for _, x := range out["saved_searches"].([]any) {
		list = append(list, x.(map[string]any))
	}
	return list
}

func TestSavedSearchesCRUDCountsAndBootstrap(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	a := h.addFeed("A", 0)
	b := h.addFeed("B", 0)
	h.addItem(a, seedItem{Title: "Kernel release notes"})
	h.addItem(a, seedItem{Title: "Another kernel patch", Read: true})
	h.addItem(b, seedItem{Title: "Kernel in feed B"})
	h.addItem(b, seedItem{Title: "Cooking", Starred: true})
	h.addItem(b, seedItem{Title: "Cooking again", Starred: true, Read: true})

	sub := h.events()
	defer sub.Close()
	all := h.saved(c, `{"name":"  Kernel  ","q":"kernel"}`)
	require.Equal(t, "Kernel", all["name"])
	require.Regexp(t, `^s[0-9a-f]{12}$`, all["id"], "the server makes the id")
	require.EqualValues(t, 2, all["unread"], "unread kernel articles in the whole library")
	require.Equal(t, false, all["unread_capped"])
	require.NotContains(t, all, "scope")
	inB := h.saved(c, `{"name":"B kernel","q":"kernel","scope":{"feed_id":"`+sid(b)+`"},"order":"rank"}`)
	require.EqualValues(t, 1, inB["unread"])
	require.Equal(t, "rank", inB["order"])
	starred := h.saved(c, `{"name":"Cooking","q":"cooking","scope":{"view":"starred"}}`)
	require.EqualValues(t, 1, starred["unread"], "the count is of unread articles even in the starred view")
	require.Len(t, ofType(collect(t, sub, "saved_searches.changed", 2*time.Second), "saved_searches.changed"), 1)

	list := savedList(t, h, c, "")
	require.Len(t, list, 3)
	require.Equal(t, []any{"Kernel", "B kernel", "Cooking"}, []any{list[0]["name"], list[1]["name"], list[2]["name"]})
	require.EqualValues(t, 2, list[0]["unread"])
	for _, l := range savedList(t, h, c, "?counts=0") {
		require.Nil(t, l["unread"], "?counts=0 skips the counts")
	}

	// Bootstrap carries the list (no counts) and the setting value.
	_, boot, _ := h.api(c, "GET", "/api/bootstrap", "")
	require.Len(t, boot["saved_searches"].([]any), 3)
	require.Len(t, boot["settings"].(map[string]any)["library.saved_searches"].([]any), 3)

	// PATCH: rename, change scope, clear the scope and the order.
	id := inB["id"].(string)
	code, out, _ := h.api(c, "PATCH", "/api/saved-searches/"+id, `{"name":"Renamed","scope":{"folder_id":"1"},"order":null}`)
	require.Equal(t, 200, code, out)
	require.Equal(t, "Renamed", out["name"])
	require.Equal(t, map[string]any{"folder_id": "1"}, out["scope"])
	require.NotContains(t, out, "order")
	code, out, _ = h.api(c, "PATCH", "/api/saved-searches/"+id, `{"scope":null,"q":"kernel two"}`)
	require.Equal(t, 200, code)
	require.NotContains(t, out, "scope")
	require.Equal(t, "Renamed", out["name"], "untouched fields stay")

	// reorder
	ids := []string{starred["id"].(string), id, all["id"].(string)}
	code, out, _ = h.api(c, "POST", "/api/saved-searches/reorder", jsonStr(map[string]any{"ids": ids}))
	require.Equal(t, 200, code, out)
	list = savedList(t, h, c, "?counts=0")
	require.Equal(t, ids, []string{list[0]["id"].(string), list[1]["id"].(string), list[2]["id"].(string)})
	for _, bad := range []any{ids[:2], append(ids[:2:2], ids[0]), append(ids[:2:2], "nope")} {
		code, _, _ = h.api(c, "POST", "/api/saved-searches/reorder", jsonStr(map[string]any{"ids": bad}))
		require.Equal(t, 400, code)
	}

	// delete
	code, _, rec := h.api(c, "DELETE", "/api/saved-searches/"+id, "")
	require.Equal(t, 204, code)
	require.Empty(t, rec.Body.String())
	code, _, _ = h.api(c, "DELETE", "/api/saved-searches/"+id, "")
	require.Equal(t, 404, code)
	code, _, _ = h.api(c, "PATCH", "/api/saved-searches/nope", `{"name":"x"}`)
	require.Equal(t, 404, code)
	require.Len(t, savedList(t, h, c, "?counts=0"), 2)
}

func TestSavedSearchValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	for _, tc := range []struct{ name, body, field string }{
		{"no name", `{"q":"x"}`, "name"},
		{"blank q", `{"name":"n","q":"  "}`, "q"},
		{"long name", `{"name":"` + strings.Repeat("n", 61) + `","q":"x"}`, "name"},
		{"newline", `{"name":"a\nb","q":"x"}`, "name"},
		{"order", `{"name":"n","q":"x","order":"best"}`, "order"},
		{"scope two", `{"name":"n","q":"x","scope":{"feed_id":"1","view":"all"}}`, "scope"},
		{"scope view", `{"name":"n","q":"x","scope":{"view":"muted"}}`, "scope.view"},
		{"scope feed id", `{"name":"n","q":"x","scope":{"feed_id":"abc"}}`, "scope.feed_id"},
		{"scope unknown feed", `{"name":"n","q":"x","scope":{"feed_id":"9999"}}`, "scope"},
		{"scope unknown folder", `{"name":"n","q":"x","scope":{"folder_id":"9999"}}`, "scope"},
	} {
		code, out, _ := h.api(c, "POST", "/api/saved-searches", tc.body)
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, "bad_saved_search", out["error"], tc.name)
		require.Equal(t, tc.field, out["field"], tc.name)
	}
	for _, body := range []string{`{"name":"n","q":"x","id":"mine"}`, `{"name":"n","q":"x","extra":1}`, `not json`} {
		code, _, _ := h.api(c, "POST", "/api/saved-searches", body)
		require.Equal(t, 400, code, body, "the client never picks an id, and unknown fields are refused")
	}
	require.Zero(t, h.count("SELECT count(*) FROM settings WHERE key = 'library.saved_searches'"))

	ok := h.saved(c, `{"name":"n","q":"x","scope":{"feed_id":"`+sid(feed)+`"}}`)
	for _, body := range []string{`{"q":" "}`, `{"name":5}`, `{"scope":{"feed_id":"9999"}}`, `{"scope":{"a":1}}`, `{"id":"z"}`} {
		code, _, _ := h.api(c, "PATCH", "/api/saved-searches/"+ok["id"].(string), body)
		require.Equal(t, 400, code, body)
	}
}

func TestSavedSearchLimitAndSettingsPatch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	var entries []map[string]any
	for i := range 100 {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("id%d", i), "name": "n", "q": fmt.Sprintf("x%d", i)})
	}
	code, out, _ := h.api(c, "PATCH", "/api/settings", jsonStr(map[string]any{"library.saved_searches": entries}))
	require.Equal(t, 200, code, out)
	code, out, _ = h.api(c, "POST", "/api/saved-searches", `{"name":"n","q":"another"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "too_many", out["error"])
	over := append(entries, map[string]any{"id": "id100", "name": "n", "q": "x100"})
	code, _, _ = h.api(c, "PATCH", "/api/settings", jsonStr(map[string]any{"library.saved_searches": over}))
	require.Equal(t, 400, code)

	for _, bad := range []any{
		"x", []any{"x"},
		[]any{map[string]any{"name": "n", "q": "x"}},                                                              // no id
		[]any{map[string]any{"id": "a", "name": "n", "q": "x"}, map[string]any{"id": "a", "name": "n", "q": "x"}}, // dup
		[]any{map[string]any{"id": "a", "name": "n", "q": "x", "extra": 1}},
		[]any{map[string]any{"id": "a b", "name": "n", "q": "x"}},
		[]any{map[string]any{"id": "a", "name": "n", "q": "x", "scope": map[string]any{"feed_id": "1", "folder_id": "1"}}},
	} {
		code, out, _ = h.api(c, "PATCH", "/api/settings", jsonStr(map[string]any{"library.saved_searches": bad}))
		require.Equal(t, 400, code, "%v", bad)
		require.Equal(t, []any{"library.saved_searches"}, out["keys"])
	}
	code, _, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"a","name":" n ","q":"x","scope":{"folder_id":"01"}}]}`)
	require.Equal(t, 200, code)
	l := savedList(t, h, c, "?counts=0")
	require.Equal(t, "n", l[0]["name"])
	require.Equal(t, map[string]any{"folder_id": "1"}, l[0]["scope"], "normalized")
	def := settingDefByKey["library.saved_searches"]
	require.Equal(t, surfaceHidden, def.Surface)
	require.Equal(t, groupLibrary, def.Group)
}

func TestSavedSearchSameSearchIsNotAddedTwice(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.saved(c, `{"name":"Rust","q":"rust"}`)
	// Same text, scope and order under another name: refused, naming the entry that already runs it.
	code, out, _ := h.api(c, "POST", "/api/saved-searches", `{"name":"Again","q":" rust ","order":"date"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "already_saved", out["error"])
	require.Contains(t, out["message"], "Rust")
	// Search ignores case and runs of whitespace, so those do not make a different search.
	code, out, _ = h.api(c, "POST", "/api/saved-searches", `{"name":"Again","q":"RUST"}`)
	require.Equal(t, 409, code, out)
	// A different scope or order is a different search.
	h.saved(c, `{"name":"Rust in A","q":"rust","scope":{"feed_id":"`+sid(feed)+`"}}`)
	h.saved(c, `{"name":"Rust oldest","q":"rust","order":"oldest"}`)
	require.Len(t, savedList(t, h, c, "?counts=0"), 3)

	// Editing into a copy is refused as well, over PATCH and over the settings list.
	code, out, _ = h.api(c, "POST", "/api/saved-searches", `{"name":"Go","q":"golang"}`)
	require.Equal(t, 201, code, out)
	code, out, _ = h.api(c, "PATCH", "/api/saved-searches/"+out["id"].(string), `{"q":"  RUST "}`)
	require.Equal(t, 409, code, out)
	require.Equal(t, "already_saved", out["error"])
	code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"a","name":"n","q":"Rust  now"},{"id":"b","name":"m","q":"rust now"}]}`)
	require.Equal(t, 400, code, out)
	require.Equal(t, []any{"library.saved_searches"}, out["keys"])
}

func TestSavedSearchOldDuplicatesNeverBlockOtherEdits(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	// A list that already holds two copies (stored before the rule) stays editable.
	h.exec(`INSERT INTO settings (key, value) VALUES ('library.saved_searches', '[{"id":"a","name":"one","q":"x"},{"id":"b","name":"two","q":"x"}]')`)
	code, out, _ := h.api(c, "PATCH", "/api/saved-searches/b", `{"name":"renamed"}`)
	require.Equal(t, 200, code, out)
	code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"a","name":"one","q":"x"},{"id":"b","name":"renamed again","q":"x"}]}`)
	require.Equal(t, 200, code, out)
	code, _, _ = h.api(c, "DELETE", "/api/saved-searches/a", "")
	require.Equal(t, 204, code)
}

func TestSavedSearchConcurrentCreatesLoseNothing(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, out, _ := h.api(c, "POST", "/api/saved-searches", fmt.Sprintf(`{"name":"s%d","q":"word%d"}`, i, i))
			require.Equal(t, 201, code, "%v", out)
		}()
	}
	wg.Wait()
	require.Len(t, savedList(t, h, c, "?counts=0"), 12)
}

func TestSavedSearchScopeIsDroppedWhenItsFeedOrFolderIsDeleted(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	fo := h.addFolder("News")
	byFeed := h.saved(c, `{"name":"f","q":"x","scope":{"feed_id":"`+sid(feed)+`"}}`)
	byFolder := h.saved(c, `{"name":"fo","q":"x","scope":{"folder_id":"`+sid(fo)+`"}}`)
	view := h.saved(c, `{"name":"v","q":"x","scope":{"view":"unread"}}`)

	code, _, _ := h.api(c, "DELETE", "/api/feeds/"+sid(feed), "")
	require.Equal(t, 204, code)
	code, _, _ = h.api(c, "DELETE", "/api/folders/"+sid(fo), "")
	require.Contains(t, []int{200, 204}, code)
	byID := map[string]map[string]any{}
	for _, l := range savedList(t, h, c, "?counts=0") {
		byID[l["id"].(string)] = l
	}
	require.Len(t, byID, 3, "the searches stay")
	require.NotContains(t, byID[byFeed["id"].(string)], "scope")
	require.NotContains(t, byID[byFolder["id"].(string)], "scope")
	require.Equal(t, map[string]any{"view": "unread"}, byID[view["id"].(string)]["scope"])
}

func TestSavedSearchCountIsCappedAt999(t *testing.T) {
	noBudget(t) // no real-time limit: the race detector is slow
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1100)
		INSERT INTO items (id, feed_id, read, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
		SELECT ?1 + i, ?2, 0, i, i, 0, 'cap:' || i, 'c', 't', 'https://x.example/' || i, 'capword ' || i, '' FROM n`, baseID+9_000_000_000, feed)
	h.exec(`INSERT INTO item_content (item_id, content_html, content_text) SELECT id, '<p>x</p>', 'x' FROM items WHERE uid LIKE 'cap:%'`)
	out := h.saved(c, `{"name":"cap","q":"capword"}`)
	require.EqualValues(t, 999, out["unread"])
	require.Equal(t, true, out["unread_capped"])
	l := savedList(t, h, c, "")
	require.EqualValues(t, 999, l[0]["unread"])
	require.Equal(t, true, l[0]["unread_capped"])

	h.exec("UPDATE items SET read = 1 WHERE uid LIKE 'cap:%' AND CAST(substr(uid, 5) AS INTEGER) > 40")
	l = savedList(t, h, c, "")
	require.EqualValues(t, 40, l[0]["unread"])
	require.Equal(t, false, l[0]["unread_capped"])
}

// noBudget removes the count time limits (a cancel-only context, a frozen clock).
func noBudget(t *testing.T) {
	t.Helper()
	cc, now := savedSearchCountCtx, savedSearchNow
	t.Cleanup(func() { savedSearchCountCtx, savedSearchNow = cc, now })
	savedSearchCountCtx = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		return context.WithCancel(ctx)
	}
	frozen := time.Unix(1_700_000_000, 0)
	savedSearchNow = func() time.Time { return frozen }
}

func TestSavedSearchCountThatRunsOutOfBudgetIsNull(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.addItem(feed, seedItem{Title: "budget word"})
	noBudget(t)
	// Per-search budget: the context is already past its deadline, so the count cannot finish.
	var seen []time.Duration
	savedSearchCountCtx = func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		seen = append(seen, d)
		return context.WithDeadline(ctx, time.Unix(0, 0))
	}
	out := h.saved(c, `{"name":"n","q":"budget"}`)
	require.Contains(t, out, "unread")
	require.Nil(t, out["unread"], "no count is better than a slow one")
	require.Equal(t, []time.Duration{savedSearchBudget}, seen, "the count ran under the per-search budget")
	require.Equal(t, false, out["unread_capped"])

	// Whole-list budget: the clock jumps past the deadline after it is set, so no count starts.
	noBudget(t)
	var calls int
	savedSearchCountCtx = func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
		calls++
		return context.WithCancel(ctx)
	}
	base := time.Unix(1_700_000_000, 0)
	var reads int
	savedSearchNow = func() time.Time {
		reads++
		if reads == 1 {
			return base // sets the deadline
		}
		return base.Add(savedSearchTotalBudget + time.Nanosecond)
	}
	require.Nil(t, savedList(t, h, c, "")[0]["unread"], "the whole-list budget is spent")
	require.Zero(t, calls, "no count may start after the list budget is spent")

	noBudget(t)
	require.EqualValues(t, 1, savedList(t, h, c, "")[0]["unread"])
}

// Replacing or resetting the list through PATCH /api/settings tells other tabs, like every other
// change to it, and a scope naming a feed or folder that does not exist is a field error.
func TestSettingsPatchOfSavedSearchesPublishesAndChecksScopes(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	sub := h.hub.Subscribe(h.hub.LastID())
	defer sub.Close()
	got := func() int {
		n := 0
		for {
			select {
			case ev := <-sub.C:
				if ev.Type == "saved_searches.changed" {
					n++
				}
			case <-time.After(150 * time.Millisecond):
				return n
			}
		}
	}

	code, out, _ := h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"a","name":"n","q":"x","scope":{"feed_id":"`+sid(feed)+`"}}]}`)
	require.Equal(t, 200, code, out)
	require.Equal(t, 1, got(), "a replaced list publishes saved_searches.changed")

	for _, scope := range []string{`{"feed_id":"9999"}`, `{"folder_id":"9999"}`} {
		code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"b","name":"n","q":"x","scope":`+scope+`}]}`)
		require.Equal(t, 400, code, "%v", out)
		require.Equal(t, "invalid_settings", out["error"])
		require.Equal(t, []any{"library.saved_searches"}, out["keys"])
		require.Contains(t, jsonStr(out["issues"]), "scope: no such feed or folder")
	}
	require.Zero(t, got(), "a refused change publishes nothing")
	l := savedList(t, h, c, "?counts=0")
	require.Len(t, l, 1, "and stores nothing")
	require.Equal(t, "a", l[0]["id"])

	// A scope left as it was is not rechecked: a list round-tripped from the client keeps working.
	h.exec("DELETE FROM feeds WHERE id = ?", feed) // leaves the stored scope dangling (raw delete)
	code, out, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":[{"id":"a","name":"renamed","q":"x","scope":{"feed_id":"`+sid(feed)+`"}}]}`)
	require.Equal(t, 200, code, out)
	require.Equal(t, 1, got())

	code, _, _ = h.api(c, "PATCH", "/api/settings", `{"library.saved_searches":null}`)
	require.Equal(t, 200, code)
	require.Equal(t, 1, got(), "a reset publishes too")
	require.Empty(t, savedList(t, h, c, "?counts=0"))

	// Other keys alone publish nothing.
	code, _, _ = h.api(c, "PATCH", "/api/settings", `{"library.auto_read_days":3}`)
	require.Equal(t, 200, code)
	require.Zero(t, got())
}

// The count stops at 999 only when there is more: exactly 999 is "999", not "999+".
func TestSavedSearchCapBoundary(t *testing.T) {
	noBudget(t)
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1001)
		INSERT INTO items (id, feed_id, read, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
		SELECT ?1 + i, ?2, 0, i, i, 0, 'cap:' || i, 'c', 't', 'https://x.example/' || i, 'capword ' || i, '' FROM n`, baseID+9_000_000_000, feed)
	h.exec(`INSERT INTO item_content (item_id, content_html, content_text) SELECT id, '<p>x</p>', 'x' FROM items WHERE uid LIKE 'cap:%'`)
	h.saved(c, `{"name":"cap","q":"capword"}`)
	for _, tc := range []struct {
		unread int
		want   int
		capped bool
	}{{1001, 999, true}, {1000, 999, true}, {999, 999, false}, {998, 998, false}} {
		h.exec("UPDATE items SET read = CASE WHEN CAST(substr(uid, 5) AS INTEGER) > ? THEN 1 ELSE 0 END WHERE uid LIKE 'cap:%'", tc.unread)
		l := savedList(t, h, c, "")
		require.EqualValues(t, tc.want, l[0]["unread"], "unread %d", tc.unread)
		require.Equal(t, tc.capped, l[0]["unread_capped"], "unread %d", tc.unread)
	}
}
