package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// The list reports the max committed id so mark-all can use it as max_id whatever
// the order (design §7.1): oldest-first page 1 holds the LOWEST ids, so the highest
// id on the page would leave the rest of the list unmarked.
func TestListItemsAsOfSweepsOldestFirstList(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	// backdated sort_at: the newest id sorts first in an oldest-first list
	newest := h.addItem(f, seedItem{SortAt: 10})
	var rest []int64
	for i := 0; i < 5; i++ {
		rest = append(rest, h.addItem(f, seedItem{SortAt: int64(100 + i)}))
	}
	ledger := h.addItem(f, seedItem{})
	h.trim(ledger, false, 5)
	_ = newest

	code, out, _ := h.api(c, "GET", "/api/items?view=unread&order=oldest&limit=2", "")
	require.Equal(t, 200, code)
	asOf, ok := out["as_of"].(string)
	require.True(t, ok, "as_of is a string id")
	require.Equal(t, sid(ledger), asOf, "the committed max includes ledger ids")
	require.Len(t, out["items"], 2)

	code, out, _ = h.api(c, "POST", "/api/items/mark-read", fmt.Sprintf(`{"scope":{"all":true,"view":"unread"},"max_id":%q,"read":true,"reason":"bulk"}`, asOf))
	require.Equal(t, 200, code)
	require.Len(t, out["changed"], 6, "ids far above page 1 are swept")
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE read = 0"))
	require.Equal(t, []any{sid(ledger)}, out["ledger_ids"])

	// an item that arrives after the list loaded is not swept
	late := h.addItem(f, seedItem{})
	code, out, _ = h.api(c, "POST", "/api/items/mark-read", fmt.Sprintf(`{"scope":{"all":true},"max_id":%q,"read":true}`, asOf))
	require.Equal(t, 200, code)
	require.Empty(t, out["changed"])
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", late))

	// every kind of page carries it
	code, out, _ = h.api(c, "GET", "/api/items?ids="+sid(late), "")
	require.Equal(t, 200, code)
	require.Equal(t, sid(late), out["as_of"])
}

// Undoing a mark-all restores the trimmed-ledger rows it read, without resurrecting stubs.
func TestBulkUndoRestoresLedgerRows(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	live := h.addItem(f, seedItem{})
	stub := h.addItem(f, seedItem{})
	bare := h.addItem(f, seedItem{})
	h.trim(stub, true, 5)
	h.trim(bare, false, 5)

	code, out, _ := h.api(c, "POST", "/api/items/mark-read", `{"scope":{"all":true},"read":true,"reason":"bulk"}`)
	require.Equal(t, 200, code)
	require.Equal(t, strs(live), anyStrs(out["changed"]))
	require.ElementsMatch(t, strs(stub, bare), anyStrs(out["ledger_ids"]))
	require.Equal(t, true, out["undoable"])
	require.Equal(t, 2, h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))

	code, out, _ = h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{
		"ids": out["changed"], "ledger_ids": out["ledger_ids"], "read": false, "reason": "key"}))
	require.Equal(t, 200, code)
	require.Equal(t, strs(live), anyStrs(out["changed"]))
	require.ElementsMatch(t, strs(stub, bare), anyStrs(out["ledger_ids"]))
	require.Equal(t, 0, h.count("SELECT count(*) FROM trimmed_items WHERE read = 1"), "the Reader API sees them unread again")
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE id IN (?, ?)", stub, bare), "stubs are not resurrected")
	require.Equal(t, 0, h.count("SELECT read FROM items WHERE id = ?", live))

	code, _, _ = h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": []string{}, "ledger_ids": strs(stub), "read": true}))
	require.Equal(t, 400, code, "ledger_ids is for undo only")
}
