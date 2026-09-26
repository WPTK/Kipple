package sched

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// A muted item is read on arrival, never queued for full text, left out of the fetch.done ids and
// counted in muted_items; the rest of the fetch is untouched.
func TestMutedItemsSkipFulltextAndFetchDoneIds(t *testing.T) {
	r := newRig(t, Options{})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed(srv.URL, 1, 2, 3))
	id := r.ftFeed(srv.URL + "/f")
	_, err := r.db.CreateFilter(context.Background(), store.Filter{Enabled: true, Scope: "global", Kind: "text",
		Terms: []string{"Item 2"}, Fields: []string{"title"}, WholeWord: true, FoldDiacritics: true, Action: "mute"})
	require.NoError(t, err)

	r.s.Wake()
	r.waitEvents("fetch.done", 1)
	require.EqualValues(t, 3, r.num("SELECT count(*) FROM items WHERE feed_id = ?", id))
	require.EqualValues(t, 1, r.num("SELECT count(*) FROM items WHERE feed_id = ? AND muted_by IS NOT NULL AND read = 1 AND title = 'Item 2'", id))

	ev := r.events("fetch.done")[0]
	require.EqualValues(t, 3, ev["new_items"])
	require.EqualValues(t, 1, ev["muted_items"])
	require.Len(t, ev["new_item_ids"], 2, "the muted item is not in the new ids a client catches up on")

	r.waitRows(id, 2, 0)
	require.Equal(t, 1, srv.count("/a/1"))
	require.Equal(t, 0, srv.count("/a/2"), "no page fetch for a muted item")
	require.Equal(t, 1, srv.count("/a/3"))
	note := r.lastNote(id)
	require.Contains(t, note, "fulltext_picked: 2", "the pick already left the muted item out")
	require.Contains(t, note, "filters: muted 1, marked_read 0, starred 0")
}
