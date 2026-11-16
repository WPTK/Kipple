package api

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A content page is cut at a byte bound: the items that fit are sent, next_cursor continues the list, and
// following it returns the rest in order with nothing repeated or skipped. An ordinary page is untouched.
func TestListItemsIncludeContentIsBoundedByBytes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	big := strings.Repeat("x", 3<<20)
	var ids []string
	for i := range 5 {
		ids = append(ids, sid(h.addItem(f, seedItem{Title: fmt.Sprintf("Big %d", i), Text: big, SortAt: int64(1000 - i)})))
	}
	var got []string
	path := "/api/items?include=content"
	pages := 0
	for {
		code, body, _ := h.api(c, "GET", path, "")
		require.Equal(t, 200, code)
		items := body["items"].([]any)
		require.NotEmpty(t, items)
		require.LessOrEqual(t, len(items), 2, "8 MiB holds two of these 3 MiB articles")
		for _, it := range items {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		pages++
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/api/items?include=content&cursor=" + next
	}
	require.Equal(t, ids, got)
	require.Equal(t, 3, pages)

	// A page of ordinary articles is whole, with no continuation.
	h2 := newHarness(t)
	c2 := h2.login()
	f2 := h2.addFeed("B", 0)
	for i := range 5 {
		h2.addItem(f2, seedItem{Title: fmt.Sprintf("Small %d", i), SortAt: int64(1000 - i)})
	}
	_, body, _ := h2.api(c2, "GET", "/api/items?include=content", "")
	require.Len(t, body["items"], 5)
	require.Nil(t, body["next_cursor"])
}

// The cut page's next_cursor comes from the cards the page consumed, not from asking the list again: an
// item that leaves the unread list while the page is being built (here, one the page itself sends) does not
// move the continuation, so nothing after it is skipped.
func TestIncludeContentContinuationSurvivesAnItemLeavingTheList(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	big := strings.Repeat("x", 3<<20)
	var ids []string
	for i := range 5 {
		ids = append(ids, sid(h.addItem(f, seedItem{Title: fmt.Sprintf("Big %d", i), Text: big, SortAt: int64(1000 - i)})))
	}
	// Between the page's list query and its content reads (the first time the list handler asks for the
	// time), the reader marks the second item read.
	fired := false
	clock := h.srv.now
	// This relies on listItems calling s.now between its list query and its content reads.
	h.srv.now = func() time.Time {
		if pc, _, _, ok := runtime.Caller(1); ok && !fired && strings.Contains(runtime.FuncForPC(pc).Name(), "listItems") {
			fired = true
			h.exec("UPDATE items SET read = 1 WHERE id = ?", ids[1])
		}
		return clock()
	}
	var got []string
	path := "/api/items?include=content"
	for {
		code, body, _ := h.api(c, "GET", path, "")
		require.Equal(t, 200, code)
		for _, it := range body["items"].([]any) {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/api/items?include=content&cursor=" + next
	}
	require.True(t, fired)
	require.Equal(t, ids, got)
}

// A relevance-ordered content page that is cut at the byte bound continues from the rank of the last card it
// consumed, taken from the list query that built the page. An article that matches no more by the time the
// page's content is read (here, one deleted) does not move the continuation, so nothing after it is skipped.
func TestIncludeContentRankContinuationSurvivesAnItemLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	big := strings.Repeat("x", 3<<20)
	var ids []string
	for i := range 5 {
		ids = append(ids, sid(h.addItem(f, seedItem{Title: fmt.Sprintf("needle %d", i), Text: big, SortAt: int64(1000 - i)})))
	}
	fired := false
	clock := h.srv.now
	// This relies on listItems calling s.now between its list query and its content reads.
	h.srv.now = func() time.Time {
		if pc, _, _, ok := runtime.Caller(1); ok && !fired && strings.Contains(runtime.FuncForPC(pc).Name(), "listItems") {
			fired = true
			h.exec("DELETE FROM items WHERE id = ?", ids[1])
		}
		return clock()
	}
	var got []string
	path := "/api/items?include=content&order=rank&q=needle"
	for {
		code, body, _ := h.api(c, "GET", path, "")
		require.Equal(t, 200, code)
		for _, it := range body["items"].([]any) {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/api/items?include=content&order=rank&q=needle&cursor=" + next
	}
	require.True(t, fired)
	require.Equal(t, []string{ids[0], ids[2], ids[3], ids[4]}, got)
}
