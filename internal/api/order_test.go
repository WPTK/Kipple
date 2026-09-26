package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tiedFixture seeds a feed whose items share sort_at values in groups of three,
// so every page boundary can fall inside a tie.
func tiedFixture(h *harness, n int) (feed int64, ids []int64) {
	feed = h.addFeed("A", 0)
	for i := 0; i < n; i++ {
		ids = append(ids, h.addItem(feed, seedItem{SortAt: int64(1000 + i/3)}))
	}
	return feed, ids
}

// pageAll follows next_cursor and returns every id in served order.
func pageAll(t *testing.T, h *harness, c *http.Cookie, query string, limit int) []string {
	t.Helper()
	var got []string
	cursor := ""
	for pages := 0; pages < 200; pages++ {
		path := fmt.Sprintf("/api/items?%s&limit=%d", query, limit)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		code, body, _ := h.api(c, "GET", path, "")
		require.Equal(t, 200, code)
		got = append(got, itemIDs(t, body)...)
		next, _ := body["next_cursor"].(string)
		if next == "" {
			return got
		}
		cursor = next
	}
	t.Fatal("paging did not terminate")
	return nil
}

func TestListItemsOrderOldestPagination(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	_, ids := tiedFixture(h, 23)
	asc := strs(ids...)
	desc := slices.Clone(asc)
	slices.Reverse(desc)
	for _, limit := range []int{1, 2, 3, 4, 5, 7, 23, 50} {
		require.Equal(t, asc, pageAll(t, h, c, "view=all&order=oldest", limit), "oldest limit=%d", limit)
		require.Equal(t, desc, pageAll(t, h, c, "view=all&order=date", limit), "date limit=%d", limit)
		require.Equal(t, desc, pageAll(t, h, c, "view=all", limit), "default limit=%d", limit)
	}
}

func TestListItemsOldestSearch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	var want []int64
	for i := 0; i < 9; i++ {
		want = append(want, h.addItem(f, seedItem{Text: "zebra herd", SortAt: int64(500 + i/2)}))
	}
	h.addItem(f, seedItem{Text: "nothing here"})
	require.Equal(t, strs(want...), pageAll(t, h, c, "q=zebra&order=oldest", 2))
	slices.Reverse(want)
	require.Equal(t, strs(want...), pageAll(t, h, c, "q=zebra", 2))
}

func TestListItemsCursorOrderMismatch(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	for i := 0; i < 6; i++ {
		h.addItem(f, seedItem{Text: "zebra", SortAt: 700})
	}
	cursorOf := func(query string) string {
		code, body, _ := h.api(c, "GET", "/api/items?"+query+"&limit=2", "")
		require.Equal(t, 200, code)
		cur, _ := body["next_cursor"].(string)
		require.NotEmpty(t, cur)
		return cur
	}
	date, oldest, rank := cursorOf("view=all"), cursorOf("view=all&order=oldest"), cursorOf("q=zebra&order=rank")
	require.NotEqual(t, date, oldest)
	for _, tc := range []struct{ name, query, cursor string }{
		{"oldest cursor on date", "view=all", oldest},
		{"date cursor on oldest", "view=all&order=oldest", date},
		{"rank cursor on oldest", "q=zebra&order=oldest", rank},
		{"oldest cursor on rank", "q=zebra&order=rank", oldest},
		{"date cursor on rank", "q=zebra&order=rank", date},
		{"rank cursor on date", "q=zebra", rank},
	} {
		code, out, _ := h.api(c, "GET", "/api/items?"+tc.query+"&cursor="+tc.cursor, "")
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, "bad_cursor", out["error"], tc.name)
	}
	// An old untagged cursor still means date order.
	code, _, _ := h.api(c, "GET", "/api/items?view=all&cursor="+date, "")
	require.Equal(t, 200, code)
	code, _, _ = h.api(c, "GET", "/api/items?view=all&order=sideways", "")
	require.Equal(t, 400, code)
}

func TestListItemsReadingTime(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	h.addItem(f, seedItem{Words: 0, SortAt: 1}) // no extracted text: matches no reading-time filter
	short := h.addItem(f, seedItem{Words: 50, SortAt: 2})
	one := h.addItem(f, seedItem{Words: 230, SortAt: 3})
	two := h.addItem(f, seedItem{Words: 231, SortAt: 4})
	five := h.addItem(f, seedItem{Words: 1150, SortAt: 5})
	six := h.addItem(f, seedItem{Words: 1151, SortAt: 6})
	list := func(q string) []string {
		code, body, _ := h.api(c, "GET", "/api/items?view=all&order=oldest&"+q, "")
		require.Equal(t, 200, code)
		return itemIDs(t, body)
	}
	require.Equal(t, strs(short, one, two, five), list("max_minutes=5"))
	require.Equal(t, strs(two, five, six), list("min_minutes=2"))
	require.Equal(t, strs(two, five), list("min_minutes=2&max_minutes=5"))
	require.Equal(t, strs(short, one, two, five, six), list("min_minutes=1"))
	require.Equal(t, strs(five), list("min_minutes=5&max_minutes=5"))
	for _, bad := range []string{"min_minutes=x", "max_minutes=-1", "min_minutes=5&max_minutes=2", "min_minutes=-3"} {
		code, _, _ := h.api(c, "GET", "/api/items?"+bad, "")
		require.Equal(t, 400, code, bad)
	}
}

func TestMarkReadBoundTable(t *testing.T) {
	// The bound must equal "the rows the list shows before/after the anchor",
	// so the expectation is derived by listing in that order and slicing.
	for _, order := range []string{"date", "oldest"} {
		for _, side := range []string{"above", "below"} {
			for _, inclusive := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/inclusive=%v", order, side, inclusive)
				t.Run(name, func(t *testing.T) {
					h := newHarness(t)
					c := h.login()
					_, ids := tiedFixture(h, 12)
					for _, k := range []int{0, 1, 4, 5, 11} { // ends and tie-group middles
						h.exec("UPDATE items SET read = 0, read_at = NULL")
						code, body, _ := h.api(c, "GET", "/api/items?view=all&limit=100&order="+order, "")
						require.Equal(t, 200, code)
						shown := itemIDs(t, body)
						require.Len(t, shown, len(ids))
						anchor := body["items"].([]any)[k].(map[string]any)
						lo, hi := 0, k // above: rows before the anchor
						if side == "below" {
							lo, hi = k+1, len(shown)
						}
						if inclusive {
							if side == "above" {
								hi++
							} else {
								lo--
							}
						}
						want := slices.Clone(shown[lo:hi])
						slices.Sort(want)
						req := map[string]any{
							"scope": map[string]any{"all": true, "view": "all"}, "read": true,
							"bound": map[string]any{"order": order, "side": side, "inclusive": inclusive,
								"anchor": map[string]any{"sort_at": anchor["sort_at"], "id": anchor["id"]}},
						}
						code, out, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(req))
						require.Equal(t, 200, code)
						got := anyStrs(out["changed"])
						slices.Sort(got)
						require.Equal(t, want, got, "anchor index %d", k)
						require.Equal(t, len(want), h.count("SELECT count(*) FROM items WHERE read = 1"))
					}
				})
			}
		}
	}
}

func TestMarkReadBoundMaxIDGuard(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	old1 := h.addItem(f, seedItem{SortAt: 100})
	anchor := h.addItem(f, seedItem{SortAt: 200})
	old2 := h.addItem(f, seedItem{SortAt: 300})
	snapshot := old2
	late := h.addItem(f, seedItem{SortAt: 250}) // arrived after the list loaded, sorts inside the range
	req := func(side string) string {
		return jsonStr(map[string]any{
			"scope": map[string]any{"all": true}, "read": true, "max_id": sid(snapshot),
			"bound": map[string]any{"order": "date", "side": side,
				"anchor": map[string]any{"sort_at": 200, "id": sid(anchor)}},
		})
	}
	code, out, _ := h.api(c, "POST", "/api/items/mark-read", req("above"))
	require.Equal(t, 200, code)
	require.Equal(t, strs(old2), anyStrs(out["changed"]), "the late item sorts inside the range but is never swept")
	code, out, _ = h.api(c, "POST", "/api/items/mark-read", req("below"))
	require.Equal(t, 200, code)
	require.Equal(t, strs(old1), anyStrs(out["changed"]))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", late))
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", anchor), "the anchor is excluded")
}

func TestMarkReadBoundScopes(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	fo := h.addFolder("News")
	a, b := h.addFeed("A", 0), h.addFeed("B", fo)
	a1 := h.addItem(a, seedItem{SortAt: 10, Text: "zebra"})
	b1 := h.addItem(b, seedItem{SortAt: 20, Text: "zebra", Starred: true})
	a2 := h.addItem(a, seedItem{SortAt: 30, Text: "zebra", Starred: true})
	b2 := h.addItem(b, seedItem{SortAt: 40, Text: "plain"})
	a3 := h.addItem(a, seedItem{SortAt: 50, Text: "zebra"})
	ledger := h.addItem(a, seedItem{SortAt: 5})
	h.trim(ledger, false, h.clk.Now().Unix())
	bound := func(order, side string, sortAt int64, id int64) map[string]any {
		return map[string]any{"order": order, "side": side, "anchor": map[string]any{"sort_at": sortAt, "id": sid(id)}}
	}
	do := func(scope map[string]any, bd map[string]any) []string {
		h.exec("UPDATE items SET read = 0")
		req := map[string]any{"scope": scope, "read": true}
		if bd != nil {
			req["bound"] = bd
		}
		code, out, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(req))
		require.Equal(t, 200, code)
		got := anyStrs(out["changed"])
		slices.Sort(got)
		return got
	}
	// Newest first, so "above" the item at 30 is 40 and 50.
	require.Equal(t, strs(b2, a3), do(map[string]any{"all": true}, bound("date", "above", 30, a2)))
	require.Equal(t, strs(a1, b1), do(map[string]any{"all": true}, bound("date", "below", 30, a2)))
	require.Equal(t, strs(a3), do(map[string]any{"feed_id": sid(a)}, bound("date", "above", 30, a2)))
	require.Equal(t, strs(b1), do(map[string]any{"folder_id": fo}, bound("oldest", "above", 40, b2)))
	require.Equal(t, strs(a2), do(map[string]any{"all": true, "view": "starred"}, bound("date", "above", 20, b1)))
	require.Equal(t, strs(a3), do(map[string]any{"all": true, "q": "zebra"}, bound("date", "above", 30, a2)))
	require.Equal(t, strs(a1, b1), do(map[string]any{"all": true, "q": "zebra"}, bound("date", "below", 30, a2)))
	require.Equal(t, strs(a1, b1, a2), do(map[string]any{"all": true, "q": "zebra"}, bound("oldest", "above", 50, a3)))
	require.Equal(t, 0, h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"), "a bounded scope never touches the ledger")
	// A search with no hits marks nothing.
	require.Empty(t, do(map[string]any{"all": true, "q": "nonesuch"}, nil))
	// Unbounded search scope: every hit, still no ledger.
	require.Equal(t, strs(a1, b1, a2, a3), do(map[string]any{"all": true, "q": "zebra"}, nil))
	require.Equal(t, 0, h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
	// An unfiltered scope keeps the ledger update.
	do(map[string]any{"all": true}, nil)
	require.Equal(t, 1, h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
}

func TestMarkReadBoundReadingTime(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	a := h.addItem(f, seedItem{SortAt: 10, Words: 100})
	h.addItem(f, seedItem{SortAt: 20, Words: 2000})
	d := h.addItem(f, seedItem{SortAt: 30, Words: 200})
	e := h.addItem(f, seedItem{SortAt: 40, Words: 0})
	body := jsonStr(map[string]any{"read": true, "scope": map[string]any{"all": true, "max_minutes": 5},
		"bound": map[string]any{"order": "date", "side": "above", "anchor": map[string]any{"sort_at": 15, "id": sid(a)}}})
	code, out, _ := h.api(c, "POST", "/api/items/mark-read", body)
	require.Equal(t, 200, code)
	require.Equal(t, strs(d), anyStrs(out["changed"]), "long and text-less items are outside the filter")
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", e))
}

func TestMarkReadBoundValidation(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	anchor := map[string]any{"sort_at": 1, "id": sid(id)}
	ok := map[string]any{"order": "date", "side": "above", "anchor": anchor}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range ok {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	all := map[string]any{"all": true}
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"bound without scope", map[string]any{"ids": []string{sid(id)}, "read": true, "bound": ok}},
		{"rank order", map[string]any{"scope": all, "read": true, "bound": with("order", "rank")}},
		{"empty order", map[string]any{"scope": all, "read": true, "bound": with("order", "")}},
		{"bad side", map[string]any{"scope": all, "read": true, "bound": with("side", "beside")}},
		{"no anchor", map[string]any{"scope": all, "read": true, "bound": map[string]any{"order": "date", "side": "above"}}},
		{"anchor id missing", map[string]any{"scope": all, "read": true, "bound": with("anchor", map[string]any{"sort_at": 1})}},
		{"anchor sort_at missing", map[string]any{"scope": all, "read": true, "bound": with("anchor", map[string]any{"id": sid(id)})}},
		{"anchor bad id", map[string]any{"scope": all, "read": true, "bound": with("anchor", map[string]any{"sort_at": 1, "id": "x"})}},
		{"unread with bound", map[string]any{"scope": all, "read": false, "bound": ok}},
		{"unknown view", map[string]any{"scope": map[string]any{"all": true, "view": "archived"}, "read": true}},
		{"bad minutes", map[string]any{"scope": map[string]any{"all": true, "min_minutes": 9, "max_minutes": 2}, "read": true}},
		{"negative minutes", map[string]any{"scope": map[string]any{"all": true, "min_minutes": -1}, "read": true}},
	} {
		code, out, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(tc.body))
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, "bad_request", out["error"], tc.name)
	}
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", id))
}

func TestMarkReadBoundUndoAndEvents(t *testing.T) {
	rec := &countingRecorder{}
	h := newHarness(t, func(o *Options) { o.Stats = rec })
	c := h.login()
	_, ids := tiedFixture(h, 6)
	sub := h.hub.Subscribe(h.hub.LastID())
	defer sub.Close()
	body := jsonStr(map[string]any{"scope": map[string]any{"all": true}, "read": true, "reason": "bulk",
		"bound": map[string]any{"order": "date", "side": "below", "anchor": map[string]any{"sort_at": 1001, "id": sid(ids[3])}}})
	code, out, _ := h.api(c, "POST", "/api/items/mark-read", body)
	require.Equal(t, 200, code)
	changed := anyStrs(out["changed"])
	require.Len(t, changed, 3)
	require.Equal(t, float64(3), out["count"])
	require.Equal(t, true, out["undoable"])
	var types []string
	var stateIDs []string
	for _, ev := range drain(sub, "", 800*time.Millisecond) {
		types = append(types, ev.Type)
		if ev.Type == "items.state" {
			var d struct {
				IDs []string `json:"ids"`
			}
			require.NoError(t, json.Unmarshal(ev.Data, &d))
			stateIDs = d.IDs
		}
	}
	require.Contains(t, types, "items.state")
	require.Contains(t, types, "counts")
	require.ElementsMatch(t, changed, stateIDs)

	// Undo is the existing by-id mark-unread: it restores exactly those ids and writes no stats.
	code, out, _ = h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": changed, "read": false}))
	require.Equal(t, 200, code)
	require.ElementsMatch(t, changed, anyStrs(out["changed"]))
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE read_at IS NOT NULL"))
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
	require.Zero(t, rec.n.Load())
}

func TestMarkReadOverCapWithholdsIDs(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	h.exec(fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
		INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, title)
		SELECT 1790251200000000 + 1000000 * i, ?, 1, i, 'cap' || i, 'c', 't', 'x' FROM n`, maxMarkIDs+5), f)
	sub := h.hub.Subscribe(h.hub.LastID())
	defer sub.Close()
	code, out, _ := h.api(c, "POST", "/api/items/mark-read", `{"scope":{"all":true},"read":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, float64(maxMarkIDs+5), out["count"])
	require.Equal(t, false, out["undoable"])
	require.Empty(t, out["changed"])
	var types []string
	for _, ev := range drain(sub, "", 300*time.Millisecond) {
		types = append(types, ev.Type)
	}
	require.Contains(t, types, "resync")
}
