package greader

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	rl      = "user/-/state/com.google/reading-list"
	starred = "user/-/state/com.google/starred"
	readSt  = "user/-/state/com.google/read"
)

func idsPage(t *testing.T, w *httptest.ResponseRecorder) (ids []int64, cont string, hasCont bool) {
	t.Helper()
	require.Equal(t, 200, w.Code, w.Body.String())
	var body struct {
		ItemRefs []struct {
			ID string `json:"id"`
		} `json:"itemRefs"`
		Continuation *string `json:"continuation"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.NotNil(t, body.ItemRefs, "itemRefs is always an array")
	for _, r := range body.ItemRefs {
		n, err := strconv.ParseInt(r.ID, 10, 64)
		require.NoError(t, err, "itemRefs ids are decimal strings")
		ids = append(ids, n)
	}
	if body.Continuation != nil {
		return ids, *body.Continuation, true
	}
	return ids, "", false
}

func seedN(h *harness, feed int64, n int, mod func(i int, s *itemSeed)) []int64 {
	h.t.Helper()
	out := make([]int64, n)
	for i := 0; i < n; i++ {
		s := itemSeed{Title: fmt.Sprintf("item %d", i)}
		if mod != nil {
			mod(i, &s)
		}
		out[i] = h.addItem(feed, s)
	}
	return out
}

func reverse(in []int64) []int64 {
	out := make([]int64, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func TestIDsBasicOrderAndStringShapes(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	all := seedN(h, f, 5, nil)

	w := h.get(rd + "stream/items/ids?s=" + rl + "&output=json&n=10")
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
	ids, _, hasCont := idsPage(t, w)
	require.Equal(t, reverse(all), ids, "descending by id")
	require.False(t, hasCont, "no continuation on the last page")
	require.NotContains(t, w.Body.String(), "continuation")
	require.Contains(t, w.Body.String(), `"id":"`+FormatDecimal(all[0])+`"`)

	// Default n is 20, empty s means reading-list.
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?output=json"))
	require.Len(t, ids, 5)

	// An empty database gives {"itemRefs":[]}, never null.
	h2 := newHarness(t)
	w = h2.get(rd + "stream/items/ids?s=" + rl)
	require.JSONEq(t, `{"itemRefs":[]}`, w.Body.String())
}

func TestIDsContinuationLoopHasNoEmptyTrailingPage(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	all := seedN(h, f, 7, nil)
	want := reverse(all)

	for _, n := range []int{1, 2, 3, 7, 8} {
		var got []int64
		cont, pages := "", 0
		for {
			path := fmt.Sprintf("stream/items/ids?s=%s&n=%d", rl, n)
			if cont != "" {
				path += "&c=" + cont
			}
			ids, c, has := idsPage(t, h.get(rd+path))
			pages++
			require.NotEmpty(t, ids, "n=%d page %d: never an empty page", n, pages)
			got = append(got, ids...)
			if !has {
				break
			}
			require.Equal(t, FormatDecimal(ids[len(ids)-1]), c, "continuation is the last id of the page")
			cont = c
			require.Less(t, pages, 20)
		}
		require.Equal(t, want, got, "n=%d", n)
		require.Equal(t, (7+n-1)/n, pages, "n=%d", n)
	}

	// Ascending direction with a continuation.
	var got []int64
	cont := ""
	for {
		path := "stream/items/ids?s=" + rl + "&r=o&n=3"
		if cont != "" {
			path += "&c=" + cont
		}
		ids, c, has := idsPage(t, h.get(rd+path))
		got = append(got, ids...)
		if !has {
			break
		}
		cont = c
	}
	require.Equal(t, all, got)
}

func TestIDsNHonoredUpTo100000AndClamped(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	// 2500 rows in one transaction; enough to prove n beyond the 1000 contents cap.
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < 2500; i++ {
			id := baseID + int64(i)*1000
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash)
				VALUES (?,?,?,?,?,'c','t')`, id, f, id/1_000_000, id/1_000_000, fmt.Sprintf("u%d", i)); err != nil {
				return err
			}
		}
		return nil
	}))
	ids, _, has := idsPage(t, h.get(rd+"stream/items/ids?n=100000"))
	require.Len(t, ids, 2500)
	require.False(t, has)
	ids, c, has := idsPage(t, h.get(rd+"stream/items/ids?n=2000"))
	require.Len(t, ids, 2000)
	require.True(t, has)
	require.Equal(t, FormatDecimal(ids[1999]), c)
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?n=999999999"))
	require.Len(t, ids, 2500, "n over the cap is clamped, not rejected")
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?n=abc"))
	require.Len(t, ids, 20)
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?n=0"))
	require.Len(t, ids, 20)
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?n=-5"))
	require.Len(t, ids, 20)
}

func TestIDsStreamGrammar(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/f", "A", "Comics")
	fb := h.addFeed("https://b.example/f", "B", "")
	a1 := h.addItem(fa, itemSeed{Title: "a1"})
	a2 := h.addItem(fa, itemSeed{Title: "a2", Read: true})
	b1 := h.addItem(fb, itemSeed{Title: "b1", Starred: true})
	b2 := h.addItem(fb, itemSeed{Title: "b2", Read: true, Starred: true})

	get := func(q string) []int64 {
		t.Helper()
		ids, _, _ := idsPage(t, h.get(rd+"stream/items/ids?"+q))
		return ids
	}
	require.Equal(t, []int64{b2, b1, a2, a1}, get("s="+rl))
	require.Equal(t, []int64{b1, a1}, get("s="+rl+"&xt="+readSt), "xt=read is unread only")
	require.Equal(t, []int64{b2, a2}, get("s="+readSt))
	require.Equal(t, []int64{b1, a1}, get("s=user/-/state/com.google/unread"))
	require.Equal(t, []int64{b1, a1}, get("s=user/-/state/com.google/kept-unread"))
	require.Equal(t, []int64{b2, b1}, get("s="+starred))
	require.Equal(t, []int64{b2}, get("s="+starred+"&it="+readSt))
	require.Equal(t, []int64{b1}, get("s="+starred+"&xt="+readSt))
	require.Equal(t, []int64{a2, a1}, get("s=feed/"+strconv.FormatInt(fa, 10)))
	require.Equal(t, []int64{a1}, get("s=feed/"+strconv.FormatInt(fa, 10)+"&xt="+readSt))
	require.Equal(t, []int64{a2, a1}, get("s="+url.QueryEscape("user/-/label/Comics")))
	require.Equal(t, []int64{a2, a1}, get("s="+url.QueryEscape("user/-/label/comics")), "labels match case-insensitively")
	require.Equal(t, []int64{a2, a1}, get("s="+url.QueryEscape("feed/https://a.example/f")), "feed/<url>")
	require.Equal(t, []int64{a2, a1}, get("s="+url.QueryEscape("feed/http://a.example/f")))
	// Contradictions and empty streams are an empty 200.
	require.Empty(t, get("s="+readSt+"&xt="+readSt))
	require.Empty(t, get("s=user/-/state/com.google/broadcast"))
	require.Empty(t, get("s=user/-/state/com.google/like"))
	require.Empty(t, get("s=user/-/label/Nope"))
	require.Empty(t, get("s=feed/9999"))
	require.Empty(t, get("s=feed/http://nope.example/x"))
	require.Empty(t, get("s=garbage"))
	require.Empty(t, get("s=user/-/state/com.google/whatever"))
	// user/<x>/ with another x works too.
	require.Equal(t, []int64{b2, b1}, get("s=user/12345/state/com.google/starred"))
}

func TestIDsInvalidContinuationIgnoredAndNT(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	// ids at exact second boundaries for nt.
	i1 := h.addItem(f, itemSeed{ID: 1_790_251_200_000_000})
	i2 := h.addItem(f, itemSeed{ID: 1_790_251_201_500_000})
	i3 := h.addItem(f, itemSeed{ID: 1_790_251_202_000_000})

	ids, _, _ := idsPage(t, h.get(rd+"stream/items/ids?c=notdigits"))
	require.Equal(t, []int64{i3, i2, i1}, ids)
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?c=-5"))
	require.Equal(t, []int64{i3, i2, i1}, ids)
	// nt=S keeps ids crawled before S+1 seconds.
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?nt=1790251201"))
	require.Equal(t, []int64{i2, i1}, ids)
	ids, _, _ = idsPage(t, h.get(rd+"stream/items/ids?nt=1790251200"))
	require.Equal(t, []int64{i1}, ids)
}

func TestIDsOTSemantics(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	const ot = int64(1_790_251_000) // seconds
	otUS := ot * 1_000_000

	old := h.addItem(f, itemSeed{ID: otUS - 3600*1_000_000})                                  // crawled an hour before ot, never changed
	changed := h.addItem(f, itemSeed{ID: otUS - 7200*1_000_000, ChangedAt: ot + 30})          // older, content changed after ot
	readAfter := h.addItem(f, itemSeed{ID: otUS - 1800*1_000_000, Read: true})                // read after ot: not returned
	fresh := h.addItem(f, itemSeed{ID: otUS + 10*1_000_000})                                  // crawled after ot
	slackIn := h.addItem(f, itemSeed{ID: otUS - 119*1_000_000})                               // inside the 120 s slack
	slackOut := h.addItem(f, itemSeed{ID: otUS - 121*1_000_000})                              // just outside
	changedSlackIn := h.addItem(f, itemSeed{ID: otUS - 9000*1_000_000, ChangedAt: ot - 119})  // changed inside the slack
	changedSlackOut := h.addItem(f, itemSeed{ID: otUS - 9100*1_000_000, ChangedAt: ot - 121}) // changed just outside
	_, _, _, _ = old, readAfter, slackOut, changedSlackOut

	q := func(extra string) []int64 {
		ids, _, _ := idsPage(t, h.get(rd+"stream/items/ids?s="+rl+"&ot="+strconv.FormatInt(ot, 10)+extra))
		return ids
	}
	want := []int64{fresh, slackIn, changed, changedSlackIn}
	// first page without c includes the content-changed older items (leg 2).
	require.Equal(t, want, q(""))
	require.Equal(t, reverse(want), q("&r=o"))
	// Pagination across both legs is exact, in both directions.
	for _, dir := range []string{"", "&r=o"} {
		var got []int64
		cont := ""
		for {
			extra := "&n=1" + dir
			if cont != "" {
				extra += "&c=" + cont
			}
			ids, c, has := idsPage(t, h.get(rd+"stream/items/ids?s="+rl+"&ot="+strconv.FormatInt(ot, 10)+extra))
			got = append(got, ids...)
			if !has {
				break
			}
			cont = c
		}
		if dir == "" {
			require.Equal(t, want, got)
		} else {
			require.Equal(t, reverse(want), got)
		}
	}
	// Read state changes never enter ot (readAfter has read_at after ot but is not returned).
	require.NotContains(t, q(""), readAfter)
}

// The two-leg query must equal the naive OR query for every combination of
// cursor, direction and window (design §10 property test).
func TestIDsOTTwoLegEqualsNaiveOR(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/f", "A", "")
	fb := h.addFeed("https://b.example/f", "B", "Group")
	rng := rand.New(rand.NewPCG(7, 11))
	const nItems = 400
	span := int64(30 * 86400)
	start := int64(1_790_251_200) - span
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		used := map[int64]bool{}
		for i := 0; i < nItems; i++ {
			var id int64
			for {
				id = (start+rng.Int64N(span))*1_000_000 + rng.Int64N(1_000_000)
				if !used[id] {
					used[id] = true
					break
				}
			}
			var changed any
			if rng.IntN(4) == 0 {
				changed = start + rng.Int64N(span)
			}
			var state any
			if rng.IntN(3) == 0 {
				state = start + rng.Int64N(span)
			}
			feed := fa
			if rng.IntN(2) == 0 {
				feed = fb
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, content_changed_at, state_changed_at, uid, content_hash, text_hash)
				VALUES (?,?,?,?,?,?,?,?,?,'c','t')`, id, feed, rng.IntN(2), rng.IntN(5)/4, id/1_000_000, id/1_000_000, changed, state, fmt.Sprintf("u%d", i)); err != nil {
				return err
			}
		}
		return nil
	}))

	type variant struct {
		query string // stream/items/ids parameters
		preds string // the same predicate for the reference query
	}
	variants := []variant{
		{"s=" + rl, "1"},
		{"s=" + rl + "&xt=" + readSt, "read = 0"},
		{"s=" + starred, "starred = 1"},
		{"s=feed/" + strconv.FormatInt(fb, 10), "feed_id = " + strconv.FormatInt(fb, 10)},
		{"s=" + url.QueryEscape("user/-/label/Group") + "&xt=" + readSt, "feed_id = " + strconv.FormatInt(fb, 10) + " AND read = 0"},
	}
	nonEmpty, multiPage := 0, 0
	defer func() {
		require.GreaterOrEqual(t, nonEmpty, 80)
		require.GreaterOrEqual(t, multiPage, 40)
	}()
	userChanges := false
	for iter := 0; iter < 120; iter++ {
		// The second half runs with greader.ot_includes_user_changes on: the reference then ORs
		// state_changed_at in as well.
		if iter == 60 {
			otUserChanges(t, h, true)
			userChanges = true
		}
		v := variants[rng.IntN(len(variants))]
		ot := start + rng.Int64N(span+86400)
		asc := rng.IntN(2) == 0
		n := 1 + rng.IntN(40)
		order, cmp := "DESC", "<"
		if asc {
			order, cmp = "ASC", ">"
		}
		// Reference: the naive OR, fully ordered, no paging.
		orState := ""
		if userChanges {
			orState = fmt.Sprintf(" OR state_changed_at >= %d", ot-120)
		}
		refSQL := fmt.Sprintf(`SELECT id FROM items WHERE (id >= %d OR content_changed_at >= %d%s) AND %s ORDER BY id %s`,
			(ot-120)*1_000_000, ot-120, orState, v.preds, order)
		rows, err := h.db.Reader().Query(refSQL)
		require.NoError(t, err)
		var want []int64
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			want = append(want, id)
		}
		require.NoError(t, rows.Close())

		// Walk the pages with continuation and compare the concatenation.
		var got []int64
		cont := ""
		for pages := 0; ; pages++ {
			require.Less(t, pages, 500)
			path := fmt.Sprintf("stream/items/ids?%s&ot=%d&n=%d", v.query, ot, n)
			if asc {
				path += "&r=o"
			}
			if cont != "" {
				path += "&c=" + cont
			}
			ids, c, has := idsPage(t, h.get(rd+path))
			got = append(got, ids...)
			if !has {
				break
			}
			cont = c
		}
		require.Equal(t, want, got, "iter %d %s ot=%d asc=%v n=%d cmp=%s user=%v", iter, v.query, ot, asc, n, cmp, userChanges)
		if len(want) > 0 {
			nonEmpty++
		}
		if len(want) > n {
			multiPage++
		}
	}
}

// ---- contents ----

// walkNoNull asserts no JSON null anywhere except the allowed key names.
func walkNoNull(t *testing.T, v any, path string, allow map[string]bool) {
	t.Helper()
	switch x := v.(type) {
	case nil:
		key := path[strings.LastIndex(path, ".")+1:]
		require.True(t, allow[key], "JSON null at %s", path)
	case map[string]any:
		for k, e := range x {
			walkNoNull(t, e, path+"."+k, allow)
		}
	case []any:
		for i, e := range x {
			walkNoNull(t, e, fmt.Sprintf("%s[%d]", path, i), allow)
		}
	}
}

func decodeAny(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(b, &v))
	return v
}

func contentsBody(ids ...string) string {
	var b strings.Builder
	b.WriteString("output=json")
	for _, id := range ids {
		b.WriteString("&i=" + url.QueryEscape(id))
	}
	return b.String()
}

func TestContentsEnvelopeAndSwiftFatalFields(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/f", "Alpha", "Comics")
	fb := h.addFeed("https://b.example/f", "", "") // no title yet
	unread := h.addItem(fa, itemSeed{Title: "Fish & Chips <tasty>", Author: "Ann", URL: "https://a.example/1",
		HTML: "<p>hello &amp; <b>world</b></p>", Published: 1_790_240_000, Updated: 1_790_241_000,
		Enclosure: `[{"url":"https://a.example/1.mp3","type":"audio/mpeg","length":"1234"}]`})
	both := h.addItem(fa, itemSeed{Title: "read and starred", Read: true, Starred: true, Published: 1_790_230_000})
	bare := h.addItem(fb, itemSeed{Title: "no link no author"})
	before := h.clk.Now().Unix()

	w := h.post(rd+"stream/items/contents", "T="+h.tok+"&"+contentsBody(FormatLongID(unread), FormatHex16(both), FormatDecimal(bare)))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
	var env struct {
		ID      string     `json:"id"`
		Updated int64      `json:"updated"`
		Items   []itemJSON `json:"items"`
		Cont    *string    `json:"continuation"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	require.Equal(t, rl, env.ID)
	require.GreaterOrEqual(t, env.Updated, before, "envelope updated is an int, now")
	require.Nil(t, env.Cont, "never a continuation on contents")
	require.Len(t, env.Items, 3)
	// Descending by id: bare, both, unread.
	require.Equal(t, []string{FormatLongID(bare), FormatLongID(both), FormatLongID(unread)},
		[]string{env.Items[0].ID, env.Items[1].ID, env.Items[2].ID})

	// The Swift-required keys are present with the right JSON types on every item.
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	for _, it := range raw.Items {
		require.IsType(t, "", it["id"])
		require.Regexp(t, `^tag:google\.com,2005:reader/item/[0-9a-f]{16}$`, it["id"])
		sum, ok := it["summary"].(map[string]any)
		require.True(t, ok, "summary is an object")
		require.IsType(t, "", sum["content"])
		cats, ok := it["categories"].([]any)
		require.True(t, ok, "categories is an array")
		for _, c := range cats {
			require.IsType(t, "", c)
		}
		org, ok := it["origin"].(map[string]any)
		require.True(t, ok, "origin is an object")
		require.Regexp(t, `^feed/\d+$`, org["streamId"])
		require.IsType(t, "", org["title"])
		require.IsType(t, "", org["htmlUrl"])
		require.IsType(t, "", it["author"])
		require.IsType(t, "", it["title"])
		require.IsType(t, float64(0), it["published"])
		require.IsType(t, float64(0), it["updated"])
		require.IsType(t, "", it["crawlTimeMsec"])
		require.IsType(t, "", it["timestampUsec"])
		alt, ok := it["alternate"].([]any)
		require.True(t, ok && len(alt) == 1)
		require.IsType(t, "", alt[0].(map[string]any)["href"])
		require.Equal(t, "text/html", alt[0].(map[string]any)["type"])
		can, ok := it["canonical"].([]any)
		require.True(t, ok && len(can) == 1)
		require.IsType(t, "", can[0].(map[string]any)["href"])
	}
	walkNoNull(t, decodeAny(t, w.Body.Bytes()), "$", nil)

	// origin.streamId == the subscription id from subscription/list.
	subIDs := map[string]bool{}
	for _, s := range subsOf(t, h) {
		subIDs[s["id"].(string)] = true
	}
	for _, it := range env.Items {
		require.True(t, subIDs[it.Origin.StreamID], it.Origin.StreamID)
	}

	u := env.Items[2]
	require.Equal(t, FormatDecimal(unread), u.TimestampUsec)
	require.Equal(t, FormatDecimal(unread/1000), u.CrawlTimeMsec)
	require.EqualValues(t, 1_790_240_000, u.Published)
	require.EqualValues(t, 1_790_241_000, u.Updated)
	require.Equal(t, "Fish & Chips <tasty>", u.Title, "titles are plain, never fullwidth-escaped")
	require.NotContains(t, w.Body.String(), "＆")
	require.Equal(t, "Ann", u.Author)
	require.Equal(t, "<p>hello &amp; <b>world</b></p>", u.Summary.Content)
	require.Equal(t, "ltr", u.Summary.Direction)
	require.Equal(t, "https://a.example/1", u.Alternate[0].Href)
	require.Equal(t, "Alpha", u.Origin.Title)
	require.Equal(t, "https://a.example/", u.Origin.HTMLURL)
	require.Equal(t, []string{rl, "user/-/label/Comics"}, u.Categories)
	require.Equal(t, []enclosureJSON{{Href: "https://a.example/1.mp3", URL: "https://a.example/1.mp3", Type: "audio/mpeg", Length: 1234}}, u.Enclosure)

	b := env.Items[1]
	require.Equal(t, []string{rl, "user/-/label/Comics", readSt, starred}, b.Categories)
	require.Equal(t, b.Published, b.Updated, "updated falls back to published")

	n := env.Items[0]
	require.Equal(t, "", n.Author)
	require.Equal(t, "", n.Alternate[0].Href, "no link is an empty string")
	require.Equal(t, "", n.Origin.Title, "untitled feed is an empty string")
	require.Nil(t, n.Enclosure)
	require.NotContains(t, w.Body.String(), `"enclosure":null`)
}

func TestContentsIDFormsTrimmedAndUnknown(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 6, nil)
	h.trim(ids[1], true)
	h.trim(ids[2], false)

	req := []string{
		FormatLongID(ids[0]), // long padded ids
		longIDPrefix + strings.TrimLeft(FormatHex16(ids[3]), "0"), // legacy unpadded ids
		FormatHex16(ids[4]),   // bare hex ids
		FormatDecimal(ids[5]), // decimal
		FormatLongID(ids[1]),  // trimmed (stub)
		FormatLongID(ids[2]),  // trimmed (ledger only)
		FormatLongID(12345),   // unknown
		"garbage", "",         // skipped
	}
	w := h.post(rd+"stream/items/contents", contentsBody(req...))
	require.Equal(t, 200, w.Code)
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	var got []string
	for _, it := range env.Items {
		got = append(got, it.ID)
	}
	require.Equal(t, []string{FormatLongID(ids[5]), FormatLongID(ids[4]), FormatLongID(ids[3]), FormatLongID(ids[0])}, got)

	// r=o reverses; zero matches is still 200 with items [].
	w = h.post(rd+"stream/items/contents", contentsBody(FormatLongID(12345))+"&r=o")
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"id":"`+rl+`","updated":`+strconv.FormatInt(h.clk.Now().Unix(), 10)+`,"items":[]}`, w.Body.String())
	// GET is tolerated.
	w = h.get(rd + "stream/items/contents?i=" + FormatDecimal(ids[0]))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), FormatLongID(ids[0]))
}

func TestContentsOrderAscAndCapsAndFulltext(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 3, nil)
	w := h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(ids[0]), FormatDecimal(ids[2]))+"&r=o")
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, FormatLongID(ids[0]), env.Items[0].ID)

	// 500 KB cap is UTF-8 safe: 3-byte runes straddle the boundary.
	big := strings.Repeat("€", 200_000) // 600,000 bytes
	h.addItem(f, itemSeed{Title: "big", HTML: big})
	last := q[int64](h, "SELECT max(id) FROM items")
	w = h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(last)))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	content := env.Items[0].Summary.Content
	require.LessOrEqual(t, len(content), 500_000)
	require.Greater(t, len(content), 499_990)
	require.True(t, isValidUTF8(content))
	require.Equal(t, strings.Repeat("€", len(content)/3), content)

	// Full text is served when the feed asks for it and an extraction exists.
	require.NoError(t, execSQL(h, "INSERT INTO item_fulltext (item_id, content_html, content_text, extracted_at) VALUES (?, '<p>full</p>', 'full', 1)", ids[1]))
	w = h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(ids[1])))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Contains(t, env.Items[0].Summary.Content, "body of", "feed content while the feed does not ask for full text")
	require.NoError(t, execSQL(h, "UPDATE feeds SET fulltext = 1"))
	w = h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(ids[1])))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "<p>full</p>", env.Items[0].Summary.Content)
	require.NoError(t, execSQL(h, "UPDATE items SET fulltext_mode = 0 WHERE id = ?", ids[1]))
	w = h.post(rd+"stream/items/contents", contentsBody(FormatDecimal(ids[1])))
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Contains(t, env.Items[0].Summary.Content, "body of", "per-item override wins")
}

func isValidUTF8(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestContentsIDCapIs1000(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < 1100; i++ {
			id := baseID + int64(i)*1000
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash) VALUES (?,?,1,1,?,'c','t')`, id, f, fmt.Sprintf("u%d", i)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id) VALUES (?)`, id); err != nil {
				return err
			}
		}
		return nil
	}))
	var idl []string
	for i := 0; i < 1100; i++ {
		idl = append(idl, FormatDecimal(baseID+int64(i)*1000))
	}
	w := h.post(rd+"stream/items/contents", contentsBody(idl...))
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 1000)
}

func TestStreamContents(t *testing.T) {
	h := newHarness(t)
	fa := h.addFeed("https://a.example/f", "A", "Comics")
	fb := h.addFeed("https://b.example/f", "B", "")
	a := seedN(h, fa, 5, nil)
	b1 := h.addItem(fb, itemSeed{Title: "b1", Starred: true})

	w := h.get(rd + "stream/contents?output=json&n=3")
	require.Equal(t, 200, w.Code)
	var env struct {
		ID           string     `json:"id"`
		Updated      int64      `json:"updated"`
		Continuation string     `json:"continuation"`
		Items        []itemJSON `json:"items"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, rl, env.ID)
	require.Len(t, env.Items, 3)
	require.Equal(t, FormatLongID(b1), env.Items[0].ID)
	require.Equal(t, FormatDecimal(a[3]), env.Continuation, "continuation is the last id of the page, as a string")

	// Stream in the path, and n capped at 1000.
	w = h.get(rd + "stream/contents/" + starred + "?n=5000")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, starred, env.ID)
	require.Len(t, env.Items, 1)
	w = h.get(rd + "stream/contents/user/-/label/Comics?xt=" + readSt + "&r=o")
	env.Continuation = ""
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "user/-/label/Comics", env.ID)
	require.Len(t, env.Items, 5)
	require.Equal(t, FormatLongID(a[0]), env.Items[0].ID)
	// A feed URL in the path survives the front handler's slash collapse.
	w = h.get(rd + "stream/contents/feed/https://b.example/f")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 1)
	// FeedMe smoke: ot=0 and s via query.
	w = h.get(rd + "stream/contents?s=feed/" + strconv.FormatInt(fa, 10) + "&ot=0&n=2")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 2)
	// Empty stream is 200 with items [].
	w = h.get(rd + "stream/contents/user/-/label/Nope")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"items":[]`)
}

func TestSmokeUnreadAndFeedMeRequests(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	seedN(h, f, 3, nil)
	for _, p := range []string{
		"stream/items/ids?s=" + rl + "&n=100000&output=json",
		"tag/list?types=1",
		"user-info",
		"stream/contents/" + rl + "?ot=0&n=20&r=n&output=json",
		"unread-count?all=true&output=json",
	} {
		w := h.get(rd + p)
		require.Equal(t, 200, w.Code, p)
		walkNoNull(t, decodeAny(t, w.Body.Bytes()), "$", nil)
	}
	w := h.get(base)
	require.Equal(t, "OK", w.Body.String())
}

func TestStreamRowByRowWritesBeforeFinishing(t *testing.T) {
	// The handler must flush through a 32 KB writer, not build the body: a
	// 1000-item response for large content arrives intact and well-formed.
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	html := strings.Repeat("<p>lorem ipsum dolor sit amet</p>", 200) // ~6 KB
	var idl []string
	for i := 0; i < 300; i++ {
		id := h.addItem(f, itemSeed{Title: "t", HTML: html})
		idl = append(idl, FormatLongID(id))
	}
	w := h.post(rd+"stream/items/contents", contentsBody(idl...))
	require.Greater(t, w.Body.Len(), 1_500_000)
	var env struct{ Items []itemJSON }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Items, 300)
	_ = http.StatusOK
}

// otUserChanges turns greader.ot_includes_user_changes on or off.
func otUserChanges(t *testing.T, h *harness, on bool) {
	t.Helper()
	require.NoError(t, h.db.SetSettings(context.Background(), map[string]any{"greader.ot_includes_user_changes": on}))
}

// advanceTo moves the harness clock forward to unix second s.
func advanceTo(h *harness, s int64) {
	h.clk.Advance(time.Duration(s-h.clk.Now().Unix()) * time.Second)
}

func TestOTIncludesUserChangesWhenSettingOn(t *testing.T) {
	h := newHarness(t)
	feed := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	old := h.addItem(feed, itemSeed{Title: "old"})
	ot := baseID/1_000_000 + 10_000 // well after the item was crawled
	path := rd + "stream/items/ids?s=user/-/state/com.google/reading-list&ot=" + strconv.FormatInt(ot, 10)
	ids := func() []int64 { got, _, _ := idsPage(t, h.get(path)); return got }

	advanceTo(h, ot+5)
	h.post(rd+"edit-tag", editBody("a="+readSt, FormatLongID(old)))
	require.True(t, isRead(h, old))
	require.Empty(t, ids(), "default: a user state change is not new activity")

	otUserChanges(t, h, true)
	require.Equal(t, []int64{old}, ids())

	require.NoError(t, execSQL(h, "UPDATE items SET state_changed_at = ? WHERE id = ?", ot-1000, old))
	require.Empty(t, ids(), "a change before ot (minus slack) stays out")
}

// Mark unread and unstar clear read_at and starred_at, but they are changes too: with the
// setting on they appear in ids?ot=, with it off they do not. So do star and mark-all-as-read.
func TestOTUserChangesReportUnreadAndUnstar(t *testing.T) {
	h := newHarness(t)
	feed := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	other := h.addFeed("https://b.example/feed.xml", "Beta", "")
	wasRead := h.addItem(feed, itemSeed{Title: "was read", Read: true})
	wasStarred := h.addItem(feed, itemSeed{Title: "was starred", Starred: true})
	stale := h.addItem(feed, itemSeed{Title: "stale"})
	untouched := h.addItem(feed, itemSeed{Title: "untouched"})
	markAll := h.addItem(other, itemSeed{Title: "mark all"})
	star := h.addItem(feed, itemSeed{Title: "star"})
	ot := baseID/1_000_000 + 10_000

	// Changed long before ot: out.
	h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(stale)))
	advanceTo(h, ot+30)
	h.post(rd+"edit-tag", editBody("r="+readSt, FormatLongID(wasRead)))
	h.post(rd+"edit-tag", editBody("r="+starred, FormatLongID(wasStarred)))
	h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(star)))
	w := h.post(rd+"mark-all-as-read", "T=x&s=feed/"+strconv.FormatInt(other, 10))
	require.Equal(t, 200, w.Code)
	require.False(t, isRead(h, wasRead))
	require.False(t, isStarred(h, wasStarred))
	require.Equal(t, 1, q[int](h, "SELECT read_at IS NULL AND starred_at IS NULL FROM items WHERE id = ?", wasRead))
	require.True(t, isRead(h, markAll))

	get := func(extra string) []int64 {
		got, _, more := idsPage(t, h.get(rd+"stream/items/ids?s="+rl+"&n=100&ot="+strconv.FormatInt(ot, 10)+extra))
		require.False(t, more)
		return got
	}
	require.Empty(t, get(""), "setting off")
	require.Empty(t, get("&r=o"), "setting off")

	otUserChanges(t, h, true)
	want := []int64{wasRead, wasStarred, markAll, star} // ascending: seeded in this order
	require.Equal(t, want, get("&r=o"))
	require.Equal(t, reverse(want), get(""))
	require.NotContains(t, get(""), stale)
	require.NotContains(t, get(""), untouched)
	// The state filters still apply to the state-change branch.
	require.Equal(t, []int64{wasRead, wasStarred, star}, get("&r=o&xt="+readSt))
	require.Equal(t, []int64{star}, get("&r=o&it="+starred))

	otUserChanges(t, h, false)
	require.Empty(t, get(""), "off again: the default query")
}

// Oldest first and newest first, page by page: the continuation carries across leg 2 (content
// changes and state changes, some items matching both) and leg 1 (crawled after ot), with no
// duplicates and nothing skipped.
func TestOTUserChangesPagingAcrossLegs(t *testing.T) {
	h := newHarness(t)
	otUserChanges(t, h, true)
	feed := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	ot := baseID/1_000_000 + 10_000
	var leg2, contentOnly []int64
	for i := range 9 {
		s := itemSeed{Title: fmt.Sprintf("old %d", i), Read: i%2 == 0, Starred: i%3 == 0}
		if i%3 == 1 {
			s.ChangedAt = ot + 1 // content changed after ot
		}
		id := h.addItem(feed, s)
		leg2 = append(leg2, id)
		if i == 4 || i == 7 {
			contentOnly = append(contentOnly, id) // content change only, no state change
		}
	}
	h.addItem(feed, itemSeed{Title: "untouched"})
	var leg1 []int64
	for i := range 4 {
		leg1 = append(leg1, h.addItem(feed, itemSeed{ID: (ot+100)*1_000_000 + int64(i)*1000, Title: fmt.Sprintf("new %d", i)}))
	}
	advanceTo(h, ot+50)
	for _, id := range leg2 {
		if slices.Contains(contentOnly, id) {
			continue
		}
		switch q[int](h, "SELECT read * 2 + starred FROM items WHERE id = ?", id) {
		case 0:
			h.post(rd+"edit-tag", editBody("a="+starred, FormatLongID(id)))
		case 1:
			h.post(rd+"edit-tag", editBody("r="+starred, FormatLongID(id)))
		default:
			h.post(rd+"edit-tag", editBody("r="+readSt, FormatLongID(id)))
		}
	}
	require.Equal(t, 7, q[int](h, "SELECT count(*) FROM items WHERE state_changed_at >= ?", ot))
	all := append(append([]int64{}, leg2...), leg1...)

	walk := func(order string, n int) []int64 {
		path := rd + "stream/items/ids?s=" + rl + "&n=" + strconv.Itoa(n) + "&ot=" + strconv.FormatInt(ot, 10) + order
		var got []int64
		cont := ""
		for pages := 0; ; pages++ {
			require.Less(t, pages, 20)
			p := path
			if cont != "" {
				p += "&c=" + cont
			}
			ids, c, more := idsPage(t, h.get(p))
			require.LessOrEqual(t, len(ids), n)
			got = append(got, ids...)
			if !more {
				return got
			}
			cont = c
		}
	}
	for _, n := range []int{1, 2, 3, 5, 100} {
		require.Equal(t, all, walk("&r=o", n), "oldest first, n=%d", n)
		require.Equal(t, reverse(all), walk("", n), "newest first, n=%d", n)
	}
}
