package greader

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
)

// editBody builds an edit-tag body the way a client does: T=x plus a valid header.
func editBody(tag string, ids ...string) string {
	var b strings.Builder
	b.WriteString("T=x")
	for _, id := range ids {
		b.WriteString("&i=" + url.QueryEscape(id))
	}
	b.WriteString("&" + tag)
	return b.String()
}

func isRead(h *harness, id int64) bool {
	return q[int](h, "SELECT read FROM items WHERE id = ?", id) == 1
}
func isStarred(h *harness, id int64) bool {
	return q[int](h, "SELECT starred FROM items WHERE id = ?", id) == 1
}

func TestEditTagReadStarAndReplay(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 3, nil)

	w := h.post(rd+"edit-tag", editBody("a="+readSt, FormatLongID(ids[0]), FormatHex16(ids[1])))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "OK", w.Body.String())
	require.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	require.True(t, isRead(h, ids[0]))
	require.True(t, isRead(h, ids[1]))
	require.False(t, isRead(h, ids[2]))
	readAt := q[int64](h, "SELECT read_at FROM items WHERE id = ?", ids[0])
	require.Equal(t, h.clk.Now().Unix(), readAt)

	// Replays are no-ops: read_at is not rewritten.
	h.clk.Advance(3600e9)
	h.post(rd+"edit-tag", editBody("a="+readSt, FormatLongID(ids[0])))
	require.Equal(t, readAt, q[int64](h, "SELECT read_at FROM items WHERE id = ?", ids[0]))

	h.post(rd+"edit-tag", editBody("r="+readSt, FormatLongID(ids[0])))
	require.False(t, isRead(h, ids[0]))
	require.Equal(t, 1, q[int](h, "SELECT read_at IS NULL FROM items WHERE id = ?", ids[0]))

	h.post(rd+"edit-tag", editBody("a="+starred, FormatDecimal(ids[2])))
	require.True(t, isStarred(h, ids[2]))
	require.Equal(t, 1, q[int](h, "SELECT starred_at IS NOT NULL FROM items WHERE id = ?", ids[2]))
	h.post(rd+"edit-tag", editBody("r="+starred, FormatDecimal(ids[2])))
	require.False(t, isStarred(h, ids[2]))

	// kept-unread is the inverse of read.
	h.post(rd+"edit-tag", editBody("r=user/-/state/com.google/kept-unread", FormatDecimal(ids[2])))
	require.True(t, isRead(h, ids[2]))
	h.post(rd+"edit-tag", editBody("a=user/-/state/com.google/kept-unread", FormatDecimal(ids[2])))
	require.False(t, isRead(h, ids[2]))

	// Several tags in one request; labels and tracking tags are ignored.
	h.post(rd+"edit-tag", "i="+FormatDecimal(ids[1])+"&a="+readSt+"&a="+starred+"&a=user/-/label/Whatever&r=user/-/state/com.google/tracking-kept-unread")
	require.True(t, isStarred(h, ids[1]))

	// Read-state changes never write stats.
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events"))
}

func TestEditTagAlwaysOKForUnknownAndBadInput(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 2, nil)
	h.trim(ids[0], false)

	cases := []string{
		editBody("a="+readSt, FormatLongID(999)),                       // unknown
		editBody("a="+readSt, FormatLongID(ids[0])),                    // ledger-only
		editBody("a="+starred, FormatLongID(ids[0])),                   // ledger-only star: ignored
		editBody("a=" + readSt),                                        // no ids
		"T=x&i=&a=" + readSt,                                           // empty i
		"T=x&i=garbage&i=-&a=" + readSt,                                // unparseable ids
		"T=x&i=" + FormatDecimal(ids[1]),                               // no tag
		"T=x&i=" + FormatDecimal(ids[1]) + "&a=user/-/label/X&r=bogus", // unknown tags
		"", // empty body
		"garbage-without-equals",
	}
	for _, body := range cases {
		w := h.do("POST", base+rd+"edit-tag", body, nil)
		require.Equal(t, 200, w.Code, body)
		require.Equal(t, "OK", w.Body.String(), body)
	}
	require.False(t, isRead(h, ids[1]))
}

func TestEditTagOnTrimmedIDs(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 6, nil)
	// 0: unread, stubbed; 1: read, stubbed; 2: unread, ledger only; 3: read, ledger only.
	require.NoError(t, execSQL(h, "UPDATE items SET read = 1 WHERE id IN (?, ?)", ids[1], ids[3]))
	for i, stub := range []bool{true, true, false, false} {
		h.trim(ids[i], stub)
	}
	ledgerRead := func(id int64) int { return q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", id) }

	// Mark read updates the ledger silently and does not restore.
	h.post(rd+"edit-tag", editBody("a="+readSt, FormatDecimal(ids[0]), FormatDecimal(ids[2])))
	require.Equal(t, 1, ledgerRead(ids[0]))
	require.Equal(t, 1, ledgerRead(ids[2]))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE id IN (?, ?)", ids[0], ids[2]))

	// Star on a stubbed id restores it, starred, and it shows up in the starred list.
	h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(ids[1])))
	require.True(t, isStarred(h, ids[1]))
	require.True(t, isRead(h, ids[1]), "keeps its read state")
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM trimmed_items WHERE id = ?", ids[1]))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM item_content WHERE item_id = ?", ids[1]))
	got, _, _ := idsPage(t, h.get(rd+"stream/items/ids?s="+starred))
	require.Equal(t, []int64{ids[1]}, got)
	w := h.post(rd+"stream/items/contents", contentsBody(FormatLongID(ids[1])))
	require.Contains(t, w.Body.String(), FormatLongID(ids[1]))

	// Star on a ledger-only id is ignored.
	h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(ids[3])))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE id = ?", ids[3]))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM trimmed_items WHERE id = ?", ids[3]))

	// Mark unread: restores a stubbed id (held for 7 days), only flips the flag otherwise.
	h.post(rd+"edit-tag", editBody("r="+readSt, FormatDecimal(ids[3])))
	require.Equal(t, 0, ledgerRead(ids[3]))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE id = ?", ids[3]))
	h.post(rd+"edit-tag", editBody("r="+readSt, FormatDecimal(ids[0])))
	require.False(t, isRead(h, ids[0]))
	require.Equal(t, h.clk.Now().Unix()+7*86400, q[int64](h, "SELECT retain_until FROM items WHERE id = ?", ids[0]))
	unread, _, _ := idsPage(t, h.get(rd+"stream/items/ids?s="+rl+"&xt="+readSt))
	require.Contains(t, unread, ids[0])
	// The FTS index came back with the restored rows.
	require.Equal(t, 2, q[int](h, "SELECT count(*) FROM items_fts WHERE items_fts MATCH 'item' AND rowid IN (?, ?)", ids[0], ids[1]))
}

func TestEditTagFourPassesThousandIDs(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 60, nil)
	h.trim(ids[0], true)
	h.trim(ids[1], false)
	var long []string
	for _, id := range ids {
		long = append(long, FormatLongID(id))
	}
	for i := 0; i < 940; i++ { // pad to 1000 with unknown ids
		long = append(long, FormatLongID(int64(1000+i)))
	}
	require.Len(t, long, 1000)

	for _, pass := range []string{"a=" + readSt, "r=" + readSt, "a=" + starred, "r=" + starred} {
		var b strings.Builder
		b.WriteString("T=" + h.tok)
		for _, id := range long {
			b.WriteString("&i=" + id) // a client leaves ':' ',' '/' unencoded
		}
		b.WriteString("&" + pass)
		w := h.post(rd+"edit-tag", b.String())
		require.Equal(t, 200, w.Code, pass)
		require.Equal(t, "OK", w.Body.String(), pass)
	}
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE read = 1 OR starred = 1"))
	// The unread pass restored the stubbed id (ids[0]); the ledger-only id stays in the ledger.
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM items WHERE id = ?", ids[0]))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM trimmed_items WHERE id = ?", ids[0]))
	// Replays of the last passes are idempotent.
	h.post(rd+"edit-tag", editBody("r="+starred, long...))
	h.post(rd+"edit-tag", editBody("a="+readSt, long...))
	h.post(rd+"edit-tag", editBody("a="+readSt, long...))
	require.Equal(t, 59, q[int](h, "SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 1, q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", ids[1]))
}

func TestMarkAllAsReadTSFormats(t *testing.T) {
	// 2026-09-24 12:00:00 UTC; items every 10 s around it.
	const cutS = int64(1_790_251_200)
	for name, ts := range map[string]string{
		"seconds (10 digits)":      strconv.FormatInt(cutS, 10),
		"milliseconds (13 digits)": strconv.FormatInt(cutS*1000, 10),
		"microseconds (16 digits)": strconv.FormatInt(cutS*1_000_000, 10),
		"nanoseconds (19 digits)":  strconv.FormatInt(cutS*1_000_000_000, 10),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			f := h.addFeed("https://a.example/f", "A", "")
			other := h.addFeed("https://b.example/f", "B", "")
			var before, after []int64
			for i := int64(-3); i <= 3; i++ {
				id := (cutS + i*10) * 1_000_000
				if i <= 0 {
					before = append(before, h.addItem(f, itemSeed{ID: id})) // i == 0 is exactly at the cutoff: included
				} else {
					after = append(after, h.addItem(f, itemSeed{ID: id}))
				}
			}
			otherItem := h.addItem(other, itemSeed{ID: (cutS - 50) * 1_000_000})
			tOld := h.addItem(f, itemSeed{ID: (cutS - 100) * 1_000_000})
			tNew := h.addItem(f, itemSeed{ID: (cutS + 100) * 1_000_000})
			h.trim(tOld, true)
			h.trim(tNew, true)

			w := h.post(rd+"mark-all-as-read", "T=x&s=feed/"+strconv.FormatInt(f, 10)+"&ts="+ts)
			require.Equal(t, 200, w.Code)
			require.Equal(t, "OK", w.Body.String())
			for _, id := range before {
				require.True(t, isRead(h, id))
			}
			for _, id := range after {
				require.False(t, isRead(h, id), "newer than the cutoff stays unread")
			}
			require.False(t, isRead(h, otherItem), "other feeds untouched")
			require.Equal(t, 1, q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", tOld))
			require.Equal(t, 0, q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", tNew))
			require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events"), "mark-all never produces stats")
		})
	}
}

func TestMarkAllAsReadScopes(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/f", "A", "Comics")
	fb := h.addFeed("https://b.example/f", "B", "Comics")
	fc := h.addFeed("https://c.example/f", "C", "")
	a := h.addItem(fa, itemSeed{})
	b := h.addItem(fb, itemSeed{Starred: true})
	c1 := h.addItem(fc, itemSeed{Starred: true})
	c2 := h.addItem(fc, itemSeed{})
	tr := h.addItem(fc, itemSeed{})
	h.trim(tr, true)

	// Read, broadcast, unknown streams: OK, nothing changes (the unread stream is the reading list,
	// covered by the conformance suite).
	for _, s := range []string{readSt, "user/-/state/com.google/broadcast", "user/-/label/Nope", "feed/9999", "garbage"} {
		w := h.post(rd+"mark-all-as-read", "T=x&s="+url.QueryEscape(s))
		require.Equal(t, "OK", w.Body.String(), s)
	}
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE read = 1"))

	// Label scope.
	h.post(rd+"mark-all-as-read", "s="+url.QueryEscape("user/-/label/Comics"))
	require.True(t, isRead(h, a))
	require.True(t, isRead(h, b))
	require.False(t, isRead(h, c1))

	// Starred scope: starred only, and no ledger statement.
	h.post(rd+"mark-all-as-read", "s="+starred)
	require.True(t, isRead(h, c1))
	require.False(t, isRead(h, c2))
	require.Equal(t, 0, q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", tr))

	// Reading-list with no ts: everything committed, ledger included.
	h.post(rd+"mark-all-as-read", "s="+rl)
	require.True(t, isRead(h, c2))
	require.Equal(t, 1, q[int](h, "SELECT read FROM trimmed_items WHERE id = ?", tr))
}

func TestMarkAllAsReadAbsentOrBadTSUsesCommittedMax(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	old := h.addItem(f, itemSeed{})
	for _, ts := range []string{"", "0", "abc", "-5"} {
		require.NoError(t, execSQL(h, "UPDATE items SET read = 0"))
		w := h.post(rd+"mark-all-as-read", "s="+rl+"&ts="+ts)
		require.Equal(t, "OK", w.Body.String())
		require.True(t, isRead(h, old), "ts=%q", ts)
	}
	// The cutoff is the committed max id, not the allocator (which can be ahead).
	h.db.IDs().Next()
	id, err := h.db.MaxCommittedID(t.Context())
	require.NoError(t, err)
	require.Equal(t, old, id)
}

func TestNormalizeTS(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1790251200", 1790251200_000000, true},
		{"1", 1_000_000, true},
		{"999999999999", 999999999999 * 1_000_000, true}, // 12 digits: seconds
		{"1790251200123", 1790251200123_000, true},       // 13: ms
		{"179025120012345", 179025120012345_000, true},   // 15: ms
		{"1790251200123456", 1790251200123456, true},     // 16: us
		{"1790251200123456789", 1790251200123456, true},  // 19: ns
		{"", 0, false}, {"0", 0, false}, {"abc", 0, false}, {"-5", 0, false},
		{"99999999999999999999", 0, false},
	} {
		got, ok := normalizeTS(tc.in)
		require.Equal(t, tc.ok, ok, tc.in)
		if ok {
			require.Equal(t, tc.want, got, tc.in)
		}
	}
}

func TestEditTagEventCapsIDsAndFallsBackToResync(t *testing.T) {
	h := newHarness(t)
	hub := events.New()
	h.api.opt.Events = hub
	sub := hub.Subscribe(0)
	defer sub.Close()

	f := h.addFeed("https://a.example/f", "A", "")
	var small []string
	for i := 0; i < 3; i++ {
		small = append(small, "i="+FormatDecimal(h.addItem(f, itemSeed{})))
	}
	w := h.post(rd+"edit-tag", "T="+h.tok+"&a=user/-/state/com.google/read&"+strings.Join(small, "&"))
	require.Equal(t, 200, w.Code)
	ev := <-sub.C
	require.Equal(t, "items.state", ev.Type, "a small batch still lists its ids")
	require.Contains(t, string(ev.Data), `"ids"`)

	var big []string
	for i := 0; i < events.MaxStateIDs+1; i++ {
		big = append(big, "i="+FormatDecimal(h.addItem(f, itemSeed{})))
	}
	w = h.post(rd+"edit-tag", "T="+h.tok+"&a=user/-/state/com.google/read&"+strings.Join(big, "&"))
	require.Equal(t, 200, w.Code)
	ev = <-sub.C
	require.Equal(t, "resync", ev.Type, "an oversized batch publishes a resync hint instead of thousands of ids")
	require.JSONEq(t, `{}`, string(ev.Data))
}

// A label whose name contains "/state/com.google/..." is a label, never a state
// stream: user/<x>/ must be one segment followed directly by the state path.
func TestLabelNamedLikeStateIsNotAState(t *testing.T) {
	for _, id := range []string{"user/-/label/x/state/com.google/read", "user/-/label/a/state/com.google/reading-list", "user/1/2/state/com.google/read"} {
		_, ok := stateName(id)
		require.False(t, ok, id)
	}
	n, ok := stateName("user/1005921515/state/com.google/read")
	require.True(t, ok)
	require.Equal(t, "read", n)
	n, ok = labelName("user/-/label/x/state/com.google/read")
	require.True(t, ok)
	require.Equal(t, "x/state/com.google/read", n)
	_, ok = labelName("user/-/state/com.google/label/foo")
	require.False(t, ok, "a state path is not a label")

	h := newHarness(t)
	odd := "x/state/com.google/reading-list"
	inOdd := h.addFeed("https://odd.example/f", "Odd", odd)
	other := h.addFeed("https://other.example/f", "Other", "News")
	oddIDs := seedN(h, inOdd, 1, nil)
	otherIDs := seedN(h, other, 1, nil)

	// edit-tag with a label that looks like the read state changes nothing.
	w := h.post(rd+"edit-tag", editBody("a="+url.QueryEscape("user/-/label/x/state/com.google/read"), FormatLongID(otherIDs[0])))
	require.Equal(t, 200, w.Code)
	require.False(t, isRead(h, otherIDs[0]), "a label is not the read state")

	// mark-all-as-read on the odd label marks that folder only, not the reading list.
	w = h.post(rd+"mark-all-as-read", "T=x&s="+url.QueryEscape("user/-/label/"+odd))
	require.Equal(t, "OK", w.Body.String())
	require.True(t, isRead(h, oddIDs[0]), "the label's own items")
	require.False(t, isRead(h, otherIDs[0]), "not every item")
}
