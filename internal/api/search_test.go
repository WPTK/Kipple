package api

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func searchURL(q string, extra ...string) string {
	u := "/api/items?q=" + url.QueryEscape(q)
	for _, e := range extra {
		u += "&" + e
	}
	return u
}

func TestSearchBasicFiltersAndSnippet(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f1 := h.addFeed("One", 0)
	fo := h.addFolder("Sci")
	f2 := h.addFeed("Two", fo)
	a := h.addItem(f1, seedItem{Title: "Zebra crossing", Text: "stripes on the road <script>alert(1)</script> and a zebra", SortAt: 100})
	b := h.addItem(f2, seedItem{Title: "Other", Text: "a zebra ate the grass", SortAt: 200, Read: true})
	h.addItem(f2, seedItem{Title: "Nothing", Text: "unrelated words", SortAt: 300})
	st := h.addItem(f1, seedItem{Title: "Starred", Text: "zebra herd", SortAt: 50, Starred: true})

	code, body, _ := h.api(c, "GET", searchURL("zebra"), "")
	require.Equal(t, 200, code)
	require.Equal(t, strs(b, a, st), itemIDs(t, body)) // default view all, date order

	first := body["items"].([]any)[1].(map[string]any)
	snip := first["snippet"].(string)
	require.Contains(t, snip, "<mark>zebra</mark>")
	require.NotContains(t, snip, "<script>")
	require.Contains(t, snip, "&lt;script&gt;")

	_, body, _ = h.api(c, "GET", searchURL("zebra", "view=unread"), "")
	require.Equal(t, strs(a, st), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("zebra", "view=starred"), "")
	require.Equal(t, strs(st), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("zebra", "feed="+sid(f2)), "")
	require.Equal(t, strs(b), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("zebra", "folder="+sid(fo)), "")
	require.Equal(t, strs(b), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("zeb*"), "")
	require.Len(t, itemIDs(t, body), 3)
	_, body, _ = h.api(c, "GET", searchURL("zebra grass"), "")
	require.Equal(t, strs(b), itemIDs(t, body))             // implicit AND
	_, body, _ = h.api(c, "GET", searchURL("crossing"), "") // title match
	require.Equal(t, strs(a), itemIDs(t, body))
}

func TestSearchMaliciousInputNeverErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	id := h.addItem(f, seedItem{Title: "Plain", Text: "plain body"})
	for _, q := range []string{
		`title:plain`, `{title author}: plain`, `"`, `""`, `"plain`, `plain"`, `NEAR(plain body)`, `plain OR`, `AND`, `NOT plain`,
		`(`, `)`, `*`, `**`, `^plain`, `-plain`, `plain -body`, `col:`, `:`, "a\x00b", `%`, `'; DROP TABLE items; --`,
		strings.Repeat("a ", 400), strings.Repeat("(", 900), `plain*x*`, `"plain" *`, `+ - ~`, `rank:1`, `plain:plain`,
	} {
		for _, extra := range []string{"", "order=rank", "view=all&order=date"} {
			code, _, rec := h.api(c, "GET", searchURL(q, extra), "")
			require.Contains(t, []int{200, 400}, code, "q=%q %s: %s", q, extra, rec.Body.String())
		}
	}
	_, body, _ := h.api(c, "GET", searchURL("title:plain "), "")
	require.Equal(t, strs(id), itemIDs(t, body)) // title: is a column filter
	_, body, _ = h.api(c, "GET", searchURL("body:plain "), "")
	require.Empty(t, itemIDs(t, body)) // any other "x:" is literal text, never a filter
	_, body, _ = h.api(c, "GET", searchURL("plain -nope "), "")
	require.Equal(t, strs(id), itemIDs(t, body)) // exclusion of a word that is absent
	_, body, _ = h.api(c, "GET", searchURL("plain -body "), "")
	require.Empty(t, itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("plain"), "")
	require.Equal(t, strs(id), itemIDs(t, body))
	code, body, _ := h.api(c, "GET", searchURL("- + ( )"), "")
	require.Equal(t, 200, code)
	require.Empty(t, itemIDs(t, body))
	require.Nil(t, body["next_cursor"])
	// Over-long queries are refused, not truncated silently.
	code, _, _ = h.api(c, "GET", searchURL(strings.Repeat("a", 1001)), "")
	require.Equal(t, 400, code)
	// The tables are intact.
	require.Equal(t, 1, h.count("SELECT count(*) FROM items"))
}

func TestSearchPaginationBothOrders(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	// Varying term density gives distinct ranks; equal-text items tie and fall back to id.
	texts := []string{
		"kiwi", "kiwi kiwi kiwi kiwi", "kiwi kiwi", "kiwi and a long tail of other words here to dilute the score somewhat",
		"kiwi", "kiwi kiwi kiwi", "kiwi kiwi", "kiwi",
	}
	all := map[string]bool{}
	for i, tx := range texts {
		all[sid(h.addItem(f, seedItem{Title: "t", Text: tx, SortAt: int64(1000 + i%3)}))] = true // ties on sort_at too
	}
	for _, order := range []string{"date", "rank"} {
		_, whole, _ := h.api(c, "GET", searchURL("kiwi", "order="+order, "limit=100"), "")
		want := itemIDs(t, whole)
		require.Len(t, want, len(texts))
		require.Nil(t, whole["next_cursor"])

		var got []string
		cursor := ""
		pages := 0
		for {
			u := searchURL("kiwi", "order="+order, "limit=3")
			if cursor != "" {
				u += "&cursor=" + url.QueryEscape(cursor)
			}
			code, body, _ := h.api(c, "GET", u, "")
			require.Equal(t, 200, code)
			got = append(got, itemIDs(t, body)...)
			pages++
			require.Less(t, pages, 10)
			if body["next_cursor"] == nil {
				break
			}
			cursor = body["next_cursor"].(string)
		}
		require.Equal(t, want, got, order)
		require.Equal(t, 3, pages)
		seen := map[string]bool{}
		for _, id := range got {
			require.False(t, seen[id])
			seen[id] = true
		}
		require.Equal(t, all, seen)
	}
	// Relevance really orders by score: the four-kiwi item leads.
	_, body, _ := h.api(c, "GET", searchURL("kiwi", "order=rank"), "")
	first := body["items"].([]any)[0].(map[string]any)
	require.Contains(t, first["excerpt"], "kiwi kiwi kiwi kiwi")
}

func TestSearchParamValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	h.addItem(f, seedItem{Text: "kiwi one"})
	h.addItem(f, seedItem{Text: "kiwi two"})
	code, _, _ := h.api(c, "GET", "/api/items?order=rank", "")
	require.Equal(t, 400, code)
	code, _, _ = h.api(c, "GET", "/api/items?order=bogus", "")
	require.Equal(t, 400, code)
	// A cursor from the other ordering is refused.
	_, body, _ := h.api(c, "GET", searchURL("kiwi", "order=rank", "limit=1"), "")
	rc := body["next_cursor"].(string)
	code, _, _ = h.api(c, "GET", searchURL("kiwi", "order=date", "cursor="+url.QueryEscape(rc)), "")
	require.Equal(t, 400, code)
	_, body, _ = h.api(c, "GET", searchURL("kiwi", "order=date", "limit=1"), "")
	dc := body["next_cursor"].(string)
	code, _, _ = h.api(c, "GET", searchURL("kiwi", "order=rank", "cursor="+url.QueryEscape(dc)), "")
	require.Equal(t, 400, code)
	code, _, _ = h.api(c, "GET", "/api/items?view=all&cursor="+url.QueryEscape(rc), "")
	require.Equal(t, 400, code)
	// Unauthenticated search is refused.
	rec := h.do("GET", searchURL("kiwi"), "")
	require.Equal(t, 401, rec.Code)
}

func TestFTSRebuild(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	id := h.addItem(f, seedItem{Text: "mango"})
	// Corrupt the index by deleting its rows behind the triggers' back, then repair.
	h.exec(`INSERT INTO items_fts(items_fts) VALUES('delete-all')`)
	_, body, _ := h.api(c, "GET", searchURL("mango"), "")
	require.Empty(t, itemIDs(t, body))
	code, _, _ := h.api(c, "POST", "/api/maintenance/fts-rebuild", "")
	require.Equal(t, 204, code)
	_, body, _ = h.api(c, "GET", searchURL("mango"), "")
	require.Equal(t, strs(id), itemIDs(t, body))
	require.Equal(t, 401, h.do("POST", "/api/maintenance/fts-rebuild", "").Code)
	code, _, _ = h.api(c, "POST", "/api/maintenance/fts-rebuild", "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	require.Equal(t, 403, code)
}

func TestSearchStemmingPhraseAndFallbackFlag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	a := h.addItem(f, seedItem{Title: "Morning run", Text: "she runs every day", SortAt: 10})
	b := h.addItem(f, seedItem{Title: "Pastry", Text: "a pie needs pastry", SortAt: 20})
	h.addItem(f, seedItem{Title: "Other", Text: "nothing here", SortAt: 30})

	_, body, _ := h.api(c, "GET", searchURL("running "), "")
	require.Equal(t, strs(a), itemIDs(t, body)) // running finds run and runs
	require.Equal(t, false, body["fallback"])
	_, body, _ = h.api(c, "GET", searchURL(`"pie needs" `), "")
	require.Equal(t, strs(b), itemIDs(t, body))

	// nothing matches both: partial matches and the flag
	_, body, _ = h.api(c, "GET", searchURL("runs pastry "), "")
	require.ElementsMatch(t, strs(a, b), itemIDs(t, body))
	require.Equal(t, true, body["fallback"])
	// no match at all: no flag
	_, body, _ = h.api(c, "GET", searchURL("zzzz "), "")
	require.Empty(t, itemIDs(t, body))
	require.Equal(t, false, body["fallback"])

	// scope.q for mark-read picks the same partial matches the list showed
	code, out, rec := h.api(c, "POST", "/api/items/mark-read", `{"scope":{"all":true,"view":"all","q":"runs pastry "},"max_id":"9223372036854775000","read":true}`)
	require.Equal(t, 200, code, rec.Body.String())
	require.EqualValues(t, 2, out["count"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE read = 0"))
}

// Relevance cursors from before schema 5 and cursors from another ordering are refused, not misread.
func TestSearchOldRankCursorIs400(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	for i := 0; i < 4; i++ {
		h.addItem(f, seedItem{Title: "t", Text: "kiwi", SortAt: int64(10 + i)})
	}
	old := base64.RawURLEncoding.EncodeToString([]byte("r-1.5|123"))
	code, _, _ := h.api(c, "GET", searchURL("kiwi", "order=rank", "cursor="+url.QueryEscape(old)), "")
	require.Equal(t, 400, code)
	_, body, _ := h.api(c, "GET", searchURL("kiwi", "order=rank", "limit=2"), "")
	cur := body["next_cursor"].(string)
	code, _, _ = h.api(c, "GET", searchURL("kiwi", "order=rank", "limit=2", "cursor="+url.QueryEscape(cur)), "")
	require.Equal(t, 200, code)
	code, _, _ = h.api(c, "GET", searchURL("kiwi", "order=date", "limit=2", "cursor="+url.QueryEscape(cur)), "")
	require.Equal(t, 400, code)
}

// typing=1 makes the unfinished last word a prefix; without it a finished-looking word never widens.
func TestSearchTypingParam(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	a := h.addItem(f, seedItem{Title: "Apple pie", Text: "an apple a day", SortAt: 100})
	b := h.addItem(f, seedItem{Title: "Application form", Text: "please fill in the application", SortAt: 200})
	_, body, _ := h.api(c, "GET", searchURL("apple"), "")
	require.Equal(t, strs(a), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("apple", "typing=1"), "")
	require.Equal(t, strs(b, a), itemIDs(t, body))
	_, body, _ = h.api(c, "GET", searchURL("apple ", "typing=1"), "")
	require.Equal(t, strs(a), itemIDs(t, body))
}

// mark-read honors scope.fallback: the list's own flag decides the expression.
func TestMarkReadScopeFallbackFlag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("One", 0)
	h.addItem(f, seedItem{Title: "Orchards", Text: "apples grow", SortAt: 100})
	h.addItem(f, seedItem{Title: "Pastry", Text: "a pie needs pastry", SortAt: 200})
	_, body, _ := h.api(c, "GET", searchURL("orchards pastry "), "")
	require.Equal(t, true, body["fallback"])
	asOf := body["as_of"].(string)
	// an exact match arrives after the list
	h.addItem(f, seedItem{Title: "Both", Text: "orchards and pastry", SortAt: 300})
	code, out, rec := h.api(c, "POST", "/api/items/mark-read", `{"scope":{"all":true,"view":"all","q":"orchards pastry ","fallback":true},"max_id":"`+asOf+`","read":true}`)
	require.Equal(t, 200, code, rec.Body.String())
	require.EqualValues(t, 2, out["count"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE read = 0")) // the late exact match stays unread
}
