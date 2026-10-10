package greader

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// The Reader API knows nothing about filters (backend-additions 1.4): a muted item is an ordinary
// read item. These tests drive real ingest and the retroactive paths through the store and check
// what Reader API clients would see.

func (h *harness) mkFilter(action string, terms ...string) store.Filter {
	h.t.Helper()
	f, err := h.db.CreateFilter(context.Background(), store.Filter{Enabled: true, Scope: "global", Kind: "text", Terms: terms,
		Fields: []string{"title"}, WholeWord: true, FoldDiacritics: true, Action: action})
	require.NoError(h.t, err)
	return f
}

// ingest commits one fetch of titled items into feed (whose url is feedURL) and returns their ids by title.
func (h *harness) ingest(feed int64, feedURL string, titles ...string) map[string]int64 {
	h.t.Helper()
	pub := h.clk.Now()
	var items []fetch.Item
	for i, title := range titles {
		items = append(items, fetch.Item{UID: fmt.Sprintf("uid-%d-%s", feed, title), URL: fmt.Sprintf("%s/%d", feedURL, i), Title: title,
			ContentHTML: "<p>" + title + "</p>", ContentText: title, ContentHash: "c" + title, TextHash: "t" + title, Published: &pub})
	}
	res := &fetch.Result{Snap: fetch.Snapshot{ID: feed, URL: feedURL, Trigger: fetch.TriggerScheduled}, StartedAt: pub, Outcome: fetch.OutcomeOK,
		Status: 200, Feed: &fetch.Feed{Title: "F", Items: items}, NextFetchAt: pub.Add(30 * time.Minute)}
	_, err := h.db.CommitFetch(context.Background(), res)
	require.NoError(h.t, err)
	out := map[string]int64{}
	for _, title := range titles {
		out[title] = q[int64](h, "SELECT id FROM items WHERE feed_id = ? AND title = ?", feed, title)
	}
	return out
}

func TestReaderAgreesWithMutedIngest(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const url = "https://f.example/rss"
	feed := h.addFeed(url, "F", "")
	h.mkFilter("mute", "sponsored")
	h.mkFilter("mark_read", "weekly")
	h.mkFilter("star", "urgent")

	ids := h.ingest(feed, url, "Sponsored: buy", "Weekly digest", "Urgent notice", "Plain story", "Urgent sponsored")
	muted := ids["Sponsored: buy"]
	weekly, urgent, plain, both := ids["Weekly digest"], ids["Urgent notice"], ids["Plain story"], ids["Urgent sponsored"]

	// unread-count and xt=read agree: only the unread, unmuted items count
	require.EqualValues(t, 3, h.unreadTotal(), "urgent x2 and plain; muted and mark_read items are read")
	unread := h.listIDs("&xt=user/-/state/com.google/read")
	require.ElementsMatch(t, []int64{urgent, plain, both}, unread)
	readIDs := h.listIDs("&it=user/-/state/com.google/read")
	require.ElementsMatch(t, []int64{muted, weekly}, readIDs)
	// the reading list still shows every item (a muted one is a read item there)
	require.ElementsMatch(t, []int64{muted, weekly, urgent, plain, both}, h.listIDs(""))
	// starred stream: the star rule worked, the mute did not win over it
	starredIDs, _, _ := idsPage(t, h.get(rd+"stream/items/ids?s=user/-/state/com.google/starred&n=100"))
	require.ElementsMatch(t, []int64{urgent, both}, starredIDs)

	// the contents call shows a muted item with the read state
	w := h.post(rd+"stream/items/contents", contentsBody(FormatLongID(muted)))
	require.Contains(t, w.Body.String(), "user/-/state/com.google/read")

	// r=read on a muted item is the un-mute: it is unread again and leaves the Muted view
	require.Equal(t, 200, h.post(rd+"edit-tag", editBody("r="+readSt, FormatLongID(muted))).Code)
	require.EqualValues(t, 4, h.unreadTotal())
	require.Zero(t, q[int](h, "SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", muted))

	// a second muted item, starred through the Reader API: un-muted and stays read
	extra := h.ingest(feed, url, "Sponsored again")["Sponsored again"]
	require.EqualValues(t, 4, h.unreadTotal())
	require.Equal(t, 200, h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(extra))).Code)
	require.EqualValues(t, 4, h.unreadTotal())
	require.Zero(t, q[int](h, "SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", extra))
	require.Equal(t, 1, q[int](h, "SELECT read FROM items WHERE id = ?", extra))

	// the invariant scan: nothing muted is ever unread
	require.Zero(t, q[int](h, "SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND (read = 0 OR starred = 1)"))
}

func TestReaderUnreadCountsFollowRetroactiveApplyAndUnmute(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const url = "https://g.example/rss"
	feed := h.addFeed(url, "G", "")
	for i := 0; i < 12; i++ {
		title := fmt.Sprintf("ham %d", i)
		if i%3 == 0 {
			title = fmt.Sprintf("spam %d", i)
		}
		h.addItem(feed, itemSeed{Title: title})
	}
	require.EqualValues(t, 12, h.unreadTotal())
	rule := h.mkFilter("mute", "spam")

	res, err := h.db.ApplyFilter(context.Background(), rule.ID, false, 12, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 4, res.Changed)
	require.EqualValues(t, 8, h.unreadTotal(), "muted by a retroactive apply: the Reader sees them read")
	require.Len(t, h.listIDs("&xt=user/-/state/com.google/read"), 8)
	require.Len(t, h.listIDs("&it=user/-/state/com.google/read"), 4)

	// deleting the rule with unmute=read: they stay read, and are simply visible in All again
	// (a second rule does the unread variant)
	rule2 := h.mkFilter("mute", "ham 1")
	_, err = h.db.ApplyFilter(context.Background(), rule2.ID, false, 12, nil, nil)
	require.NoError(t, err)
	require.EqualValues(t, 7, h.unreadTotal())

	n, ok, err := h.db.DeleteFilter(context.Background(), rule.ID, store.UnmuteRead, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 4, n)
	require.EqualValues(t, 7, h.unreadTotal(), "unmute=read leaves the items read")

	n, ok, err = h.db.DeleteFilter(context.Background(), rule2.ID, store.UnmuteUnread, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, n)
	require.EqualValues(t, 8, h.unreadTotal(), "unmute=unread brings the item back to the unread list")
	require.Zero(t, q[int](h, "SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
}

// A muted item is never queued for extraction, so the Reader hold never applies to it: it is served
// at once as a read item while an ordinary new item of the same full-text feed is held.
func TestMutedItemsAreNeverHeldByTheReader(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const url = "https://ft.example/rss"
	feed := h.fulltextFeed(url)
	h.mkFilter("mute", "sponsored")

	ids := h.ingest(feed, url, "Sponsored post", "Real story")
	muted, real := ids["Sponsored post"], ids["Real story"]
	// what the scheduler does after the commit: queue the new items that were not muted
	h.db.MarkFulltextPending(real)

	require.Equal(t, []int64{muted}, h.listIDs(""), "the pending item is held; the muted one is served at once")
	require.EqualValues(t, 0, h.unreadTotal())
	require.Len(t, h.contentIDs(muted, real), 1, "contents by id omits only the held item")

	// a retroactive mute of the still-held item takes effect, and the hold ends when its extraction does
	rule := h.mkFilter("mute", "real")
	res, err := h.db.ApplyFilter(context.Background(), rule.ID, false, 2, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Changed)
	require.Equal(t, []int64{muted}, h.listIDs(""), "still held while its extraction is pending")
	h.db.ClearFulltextPending(real)
	require.ElementsMatch(t, []int64{muted, real}, h.listIDs(""))
	require.EqualValues(t, 0, h.unreadTotal())
	require.Empty(t, h.listIDs("&xt=user/-/state/com.google/read"))
}
