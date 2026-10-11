package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// POST /api/filters with source_item_id: a rule saved from an article ("Mute similar...") mutes that
// article when it matches it, and says what it did.

func TestCreateFilterMutesTheSourceArticle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 10)
	id := func(title string) int64 { return int64(h.count("SELECT id FROM items WHERE title = ?", title)) }
	spam2, spam4, spam6 := id("spam 2"), id("spam 4"), id("spam 6")
	h.exec(fmt.Sprintf("UPDATE items SET read = 1, read_at = 5 WHERE id = %d", spam2))
	h.exec(fmt.Sprintf("UPDATE items SET starred = 1, starred_at = 5 WHERE id = %d", spam4))
	sub := h.hub.Subscribe(0)
	defer sub.Close()

	// a read article that the rule matches: muted, and still read once the rule is deleted with unmute=unread
	code, out := h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "source_item_id": fmt.Sprint(spam2)})
	require.Equal(t, 201, code, "%v", out)
	require.Equal(t, map[string]any{"item_id": fmt.Sprint(spam2), "result": "muted"}, out["source"])
	f := out["filter"].(map[string]any)
	require.EqualValues(t, 1, f["muted_items"])
	require.EqualValues(t, 1, f["hits"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"), "the source only, not the rest of the feed")
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND read_at = 5 AND muted_was_read = 1", spam2))
	st := ofType(collect(t, sub, "items.state", 5*time.Second), "items.state")[0]
	require.Equal(t, []any{fmt.Sprint(spam2)}, st["ids"])
	require.Equal(t, true, st["muted"])
	require.Equal(t, true, st["read"])
	require.Zero(t, h.count("SELECT count(*) FROM stats_events"), "a mute is not a read")

	for _, tc := range []struct {
		name string
		body map[string]any
		want any
	}{
		{"the rule does not match it", map[string]any{"action": "mute", "terms": []string{"ham"}, "source_item_id": fmt.Sprint(spam6)}, "no_match"},
		{"starred", map[string]any{"action": "mute", "terms": []string{"spam 4"}, "source_item_id": fmt.Sprint(spam4)}, "starred"},
		{"muted by another rule", map[string]any{"action": "mute", "terms": []string{"spam 2"}, "source_item_id": fmt.Sprint(spam2)}, "already_muted"},
	} {
		code, out = h.postFilter(c, tc.body)
		require.Equal(t, 201, code, "%s: %v", tc.name, out)
		require.Equal(t, tc.want, out["source"].(map[string]any)["result"], tc.name)
	}
	// no source, or a rule that does not mute: source is null
	for _, body := range []map[string]any{
		{"action": "mute", "terms": []string{"plain"}},
		{"action": "mark_read", "terms": []string{"spam"}, "source_item_id": fmt.Sprint(spam6)},
	} {
		code, out = h.postFilter(c, body)
		require.Equal(t, 201, code, "%v", out)
		require.Nil(t, out["source"])
	}
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND starred = 1 AND muted_by IS NULL", spam4))
}

func TestCreateFilterRefusesAnUnknownSourceArticle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	h.bulkItems(h.addFeed("A", 0), 2)
	for _, src := range []any{"424242", "not an id", true} {
		code, out := h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "source_item_id": src})
		require.Equal(t, 400, code, "%v", src)
		require.Equal(t, "source_item_id", out["field"], "%v", src)
	}
	require.Zero(t, h.count("SELECT count(*) FROM filters"), "nothing is stored")
}
