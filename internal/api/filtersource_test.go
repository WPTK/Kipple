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
		{"a rule that does not mute", map[string]any{"action": "mark_read", "terms": []string{"spam"}, "source_item_id": fmt.Sprint(spam6)}, "not_mute"},
		{"a rule saved turned off", map[string]any{"action": "mute", "terms": []string{"spam 6"}, "enabled": false, "source_item_id": fmt.Sprint(spam6)}, "disabled"},
		{"an article that no longer exists", map[string]any{"action": "mute", "terms": []string{"gone"}, "source_item_id": "424242"}, "gone"},
	} {
		code, out = h.postFilter(c, tc.body)
		require.Equal(t, 201, code, "%s: %v", tc.name, out)
		require.Equal(t, tc.want, out["source"].(map[string]any)["result"], tc.name)
	}
	// no source: source is null
	code, out = h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"plain"}})
	require.Equal(t, 201, code, "%v", out)
	require.Nil(t, out["source"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND starred = 1 AND muted_by IS NULL", spam4))
}

func TestCreateFilterRefusesAMalformedSourceID(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	for _, src := range []any{"not an id", true, map[string]any{}} {
		code, out := h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "source_item_id": src})
		require.Equal(t, 400, code, "%v", src)
		require.Equal(t, "source_item_id", out["field"], "%v", src)
	}
	require.Zero(t, h.count("SELECT count(*) FROM filters"), "nothing is stored")
}

// Restore in Muted un-mutes and puts back the read state from before the mute (several rules and an
// orphan: TestRestoreMutedWhicheverRuleMutedIt in the store).
func TestUnmuteRestoresTheReadStateBeforeTheMute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 6)
	id := func(title string) int64 { return int64(h.count("SELECT id FROM items WHERE title = ?", title)) }
	wasRead, wasUnread, plain := id("spam 2"), id("spam 4"), id("ham 1")
	h.exec(fmt.Sprintf("UPDATE items SET read = 1, read_at = 5 WHERE id = %d", wasRead))
	h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{"include_read": true}})
	require.Eventually(t, func() bool { return h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL") == 3 }, 5*time.Second, 20*time.Millisecond)
	sub := h.hub.Subscribe(0)
	defer sub.Close()

	code, out, _ := h.api(c, "POST", "/api/items/unmute", jsonStr(map[string]any{"ids": []string{fmt.Sprint(wasRead), fmt.Sprint(wasUnread), fmt.Sprint(plain)}}))
	require.Equal(t, 200, code, "%v", out)
	require.ElementsMatch(t, []any{fmt.Sprint(wasRead), fmt.Sprint(wasUnread)}, out["changed"], "an article that is not muted is left alone")
	require.Equal(t, []any{fmt.Sprint(wasUnread)}, out["made_unread"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND read_at = 5 AND muted_by IS NULL AND muted_was_read IS NULL", wasRead))
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND read = 0 AND muted_by IS NULL", wasUnread))
	evs := ofType(collect(t, sub, "items.state", 5*time.Second), "items.state")
	require.Equal(t, false, evs[0]["muted"])

	for _, body := range []any{map[string]any{"ids": []string{}}, map[string]any{"ids": []string{"x"}}, map[string]any{}} {
		code, _, _ = h.api(c, "POST", "/api/items/unmute", jsonStr(body))
		require.Equal(t, 400, code, "%v", body)
	}
}
