package api

import (
	"fmt"
	"strings"
	"testing"

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
