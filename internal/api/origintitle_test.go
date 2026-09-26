package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCardAndDetailSourceUsesOriginTitle(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("Alpha", 0)
	plain := h.addItem(f, seedItem{SortAt: 2000})
	orphan := h.addItem(f, seedItem{SortAt: 1000})
	h.exec("UPDATE items SET origin_title = 'Doomed' WHERE id = ?", orphan)

	_, body, _ := h.api(c, "GET", "/api/items?view=all", "")
	cards := body["items"].([]any)
	src := map[string]map[string]any{}
	for _, it := range cards {
		m := it.(map[string]any)
		src[m["id"].(string)] = m
	}
	require.Equal(t, "Alpha", src[sid(plain)]["source"])
	require.Nil(t, src[sid(plain)]["origin_title"])
	require.Equal(t, "Doomed", src[sid(orphan)]["source"])
	require.Equal(t, "Doomed", src[sid(orphan)]["origin_title"])

	_, det, _ := h.api(c, "GET", "/api/items/"+sid(orphan), "")
	require.Equal(t, "Doomed", det["source"])
	require.Equal(t, "Doomed", det["origin_title"])
	_, det, _ = h.api(c, "GET", "/api/items/"+sid(plain), "")
	require.Equal(t, "Alpha", det["source"])

	// Search cards carry it too.
	h.exec("UPDATE items SET title = 'zebra crossing' WHERE id = ?", orphan)
	h.exec("INSERT INTO items_fts(items_fts) VALUES('rebuild')")
	_, res, _ := h.api(c, "GET", "/api/items?view=all&q=zebra", "")
	got := res["items"].([]any)
	require.Len(t, got, 1)
	require.Equal(t, "Doomed", got[0].(map[string]any)["source"])
}
