package greader

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Hold-back of pending full-text items (design §6.5). The fake clock is shared
// with the API, so the window is crossed with Advance, never with sleeping.

func (h *harness) fulltextFeed(url string) int64 {
	h.t.Helper()
	f := h.addFeed(url, "FT", "")
	require.NoError(h.t, execSQL(h, "UPDATE feeds SET fulltext = 1 WHERE id = ?", f))
	return f
}

// crawledAgo adds an item whose crawl time (id) is d before the fake clock's now.
func (h *harness) crawledAgo(feed int64, d time.Duration, s itemSeed) int64 {
	h.t.Helper()
	s.ID = h.clk.Now().Add(-d).UnixMicro()
	return h.addItem(feed, s)
}

func (h *harness) listIDs(extra string) []int64 {
	h.t.Helper()
	ids, _, _ := idsPage(h.t, h.get(rd+"stream/items/ids?s="+rl+"&n=100"+extra))
	return ids
}

func (h *harness) contentIDs(ids ...int64) []string {
	h.t.Helper()
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = FormatDecimal(id)
	}
	w := h.post(rd+"stream/items/contents", contentsBody(s...))
	require.Equal(h.t, 200, w.Code)
	var env struct{ Items []itemJSON }
	require.NoError(h.t, json.Unmarshal(w.Body.Bytes(), &env))
	out := []string{}
	for _, it := range env.Items {
		out = append(out, it.ID)
	}
	return out
}

func (h *harness) unreadTotal() int64 {
	h.t.Helper()
	w := h.get(rd + "unread-count?output=json")
	var body struct {
		Max          int64 `json:"max"`
		UnreadCounts []struct {
			ID    string `json:"id"`
			Count int64  `json:"count"`
		} `json:"unreadcounts"`
	}
	require.NoError(h.t, json.Unmarshal(w.Body.Bytes(), &body))
	for _, u := range body.UnreadCounts {
		if u.ID == rl {
			require.Equal(h.t, body.Max, u.Count)
			return u.Count
		}
	}
	return 0
}

func TestHoldListings(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	plain := h.addFeed("https://plain.example/f", "P", "")

	held := h.crawledAgo(ft, 2*time.Second, itemSeed{Title: "held"})
	viaOK := h.crawledAgo(ft, 3*time.Second, itemSeed{Title: "ok"})
	viaErr := h.crawledAgo(ft, 4*time.Second, itemSeed{Title: "err"})
	np := h.crawledAgo(plain, 1*time.Second, itemSeed{Title: "plain"})
	old := h.crawledAgo(ft, 5*time.Minute, itemSeed{Title: "old"})

	require.NoError(t, execSQL(h, "INSERT INTO item_fulltext (item_id, content_html, content_text, extracted_at) VALUES (?, '<p>x</p>', 'x', 1)", viaOK))
	require.NoError(t, execSQL(h, "INSERT INTO item_fulltext (item_id, error, error_class, extracted_at) VALUES (?, 'no readable content', 'permanent', 1)", viaErr))

	// Only the item that is pending, new and effectively full-text is hidden.
	require.Equal(t, []int64{np, viaOK, viaErr, old}, h.listIDs(""))
	require.Equal(t, []int64{old, viaErr, viaOK, np}, h.listIDs("&r=o"))
	require.Equal(t, []string{FormatLongID(np), FormatLongID(viaOK), FormatLongID(viaErr), FormatLongID(old)},
		h.contentIDs(held, np, viaOK, viaErr, old), "contents by id omits the held item too")
	w := h.get(rd + "stream/contents/" + rl + "?n=50")
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 4)
	// Unread counts match the listing.
	require.EqualValues(t, 4, h.unreadTotal())
	require.EqualValues(t, 4, len(h.listIDs("&xt=user/-/state/com.google/read")))

	// A successful extraction releases the hold at once.
	require.NoError(t, execSQL(h, "INSERT INTO item_fulltext (item_id, content_html, content_text, extracted_at) VALUES (?, '<p>y</p>', 'y', 1)", held))
	require.Equal(t, []int64{np, held, viaOK, viaErr, old}, h.listIDs(""))
	require.EqualValues(t, 5, h.unreadTotal())
	require.Len(t, h.contentIDs(held), 1)
}

func TestHoldReleasedByWindowExpiry(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	held := h.crawledAgo(ft, time.Second, itemSeed{Title: "held"})
	require.Empty(t, h.listIDs(""))
	require.EqualValues(t, 0, h.unreadTotal())
	h.clk.Advance(28 * time.Second) // 29 s old
	require.Empty(t, h.listIDs(""))
	h.clk.Advance(2 * time.Second) // 31 s old
	require.Equal(t, []int64{held}, h.listIDs(""))
	require.EqualValues(t, 1, h.unreadTotal())
	require.Len(t, h.contentIDs(held), 1)
}

func TestHoldNeverAppliesToNonFulltext(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	plain := h.addFeed("https://plain.example/f", "P", "")

	a := h.crawledAgo(plain, time.Second, itemSeed{Title: "plain feed"})
	// A per-item override off wins over the feed setting; on wins over a plain feed.
	off := h.crawledAgo(ft, 1500*time.Millisecond, itemSeed{Title: "ft feed, item off"})
	require.NoError(t, execSQL(h, "UPDATE items SET fulltext_mode = 0 WHERE id = ?", off))
	on := h.crawledAgo(plain, 2*time.Second, itemSeed{Title: "plain feed, item on"})
	require.NoError(t, execSQL(h, "UPDATE items SET fulltext_mode = 1 WHERE id = ?", on))
	require.Equal(t, []int64{a, off}, h.listIDs(""))
	require.EqualValues(t, 2, h.unreadTotal())
}

func TestHoldDisabledAndClamped(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	id := h.crawledAgo(ft, time.Second, itemSeed{})

	h.api.opt.FulltextHold = -1
	require.Equal(t, []int64{id}, h.listIDs(""), "negative disables the hold")

	// A huge configured window is capped at MaxFulltextHold, inside the ot slack.
	h.api.opt.FulltextHold = 10 * time.Minute
	require.Empty(t, h.listIDs(""))
	h.clk.Advance(MaxFulltextHold)
	require.Equal(t, []int64{id}, h.listIDs(""))

	// A custom, shorter window applies.
	h.api.opt.FulltextHold = 5 * time.Second
	id2 := h.crawledAgo(ft, time.Second, itemSeed{})
	require.Equal(t, []int64{id}, h.listIDs(""))
	h.clk.Advance(5 * time.Second)
	require.Equal(t, []int64{id2, id}, h.listIDs(""))
}

func TestHoldPagingAndContinuation(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	plain := h.addFeed("https://plain.example/f", "P", "")

	// Interleave: newest first order p5 f4(h) p3 f2(h) p1 f0(h); the held ones are the ft items.
	var visible, all []int64
	for i := 0; i < 6; i++ {
		age := time.Duration(20-i) * time.Second
		if i%2 == 0 {
			all = append(all, h.crawledAgo(ft, age, itemSeed{}))
		} else {
			id := h.crawledAgo(plain, age, itemSeed{})
			all = append(all, id)
			visible = append(visible, id)
		}
	}
	page := func(dir string) []int64 {
		var got []int64
		cont := ""
		for pages := 0; ; pages++ {
			require.Less(t, pages, 10)
			extra := "&n=1" + dir
			if cont != "" {
				extra += "&c=" + cont
			}
			ids, c, has := idsPage(t, h.get(rd+"stream/items/ids?s="+rl+extra))
			got = append(got, ids...)
			if !has {
				return got
			}
			cont = c
		}
	}
	require.Equal(t, reverse(visible), page(""), "n=1 pages skip held rows without empty pages")
	require.Equal(t, visible, page("&r=o"))
	// stream/contents pages the same way.
	w := h.get(rd + "stream/contents/" + rl + "?n=2")
	var env struct {
		Continuation string
		Items        []itemJSON
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 2)
	require.Equal(t, strconv.FormatInt(visible[1], 10), env.Continuation)

	// After the window everything is listed, still in id order.
	h.clk.Advance(time.Minute)
	require.Equal(t, reverse(all), page(""))
}

// The ot hazard: a client syncs while an item is held, so it advances its ot
// past the item's crawl time. The next sync must still get the item, because
// leg 1 reaches 120 s (> the hold) before ot.
func TestHoldOTClientAdvancedPastHeldItem(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	plain := h.addFeed("https://plain.example/f", "P", "")

	heldAt := h.clk.Now()
	held := h.crawledAgo(ft, 0, itemSeed{Title: "held"})
	h.clk.Advance(10 * time.Second)
	seen := h.crawledAgo(plain, 0, itemSeed{Title: "later, other feed"}) // client's newest crawl time is after the held item
	syncAt := h.clk.Now().Unix()
	ot := strconv.FormatInt(syncAt, 10)

	// Sync 1 (ot = previous sync, well before): the held item is missing.
	require.Equal(t, []int64{seen}, h.listIDs("&ot="+strconv.FormatInt(heldAt.Unix()-300, 10)))

	// The hold ends (extraction lands or the window passes); the client's next sync
	// uses ot = its last sync time or the newest crawl time it saw.
	h.clk.Advance(25 * time.Second)
	require.Equal(t, []int64{seen, held}, h.listIDs("&ot="+ot), "held item is reached through the ot slack")
	require.Equal(t, []int64{seen, held}, h.listIDs("&ot="+strconv.FormatInt(seen/1_000_000, 10)))
	// Even with the maximum hold, the crawl time is at most MaxFulltextHold before any ot taken while it was held.
	require.Less(t, int64(MaxFulltextHold/time.Second), int64(120))
}

func TestHoldMarkAllAsReadSkipsHeldItems(t *testing.T) {
	h := newHarness(t)
	ft := h.fulltextFeed("https://ft.example/f")
	plain := h.addFeed("https://plain.example/f", "P", "")
	seen := h.crawledAgo(plain, 3*time.Second, itemSeed{})
	held := h.crawledAgo(ft, 2*time.Second, itemSeed{})
	other := h.crawledAgo(ft, time.Minute, itemSeed{}) // old: not held

	w := h.post(rd+"mark-all-as-read", "s="+rl)
	require.Equal(t, 200, w.Code)
	read := func(id int64) int64 { return q[int64](h, "SELECT read FROM items WHERE id = ?", id) }
	require.EqualValues(t, 1, read(seen), "visible item is marked")
	require.EqualValues(t, 0, read(held), "a held item the client cannot have seen stays unread")
	require.EqualValues(t, 1, read(other), "an old full-text item is marked")
}
