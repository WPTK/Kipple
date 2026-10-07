package greader

// Conformance: stream ids, stream/items/ids, stream/contents and
// stream/items/contents. See conformance_test.go for the reference tags.

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newest first, as every listing returns them by default.
var confAllDesc = []string{"tech-new", "news-unread", "news-unread-starred", "tech-unread", "loose-old", "news-old-starred", "tech-old-read"}

func TestConformanceStreamItemIDs(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)
	ids := func(query string) []string {
		t.Helper()
		got, _ := itemRefs(t, c.call(http.MethodGet, rd+"stream/items/ids?n=1000&"+query, "", nil))
		return got
	}
	s := func(id string) string { return q1("s", id) }

	// [GR][RS] itemRefs[].id is the decimal id as a string, newest first.
	require.Equal(t, l.decs(confAllDesc...), ids(s(stateReadingList)))

	// Stream grammar [GR][RS]: states, labels, feed/<id>, feed/<url>, user/<id>/… forms.
	cases := map[string][]string{
		s(stateStarred):                          {"news-unread-starred", "news-old-starred"},
		s(stateRead):                             {"news-old-starred", "tech-old-read"},
		s("user/-/state/com.google/unread"):      {"tech-new", "news-unread", "news-unread-starred", "tech-unread", "loose-old"},
		s("user/-/state/com.google/kept-unread"): {"tech-new", "news-unread", "news-unread-starred", "tech-unread", "loose-old"},
		s("user/-/label/Tech"):                   {"tech-new", "tech-unread", "tech-old-read"},
		s("user/-/label/News & Politics"):        {"news-unread", "news-unread-starred", "news-old-starred"},
		s(feedID(l.loose)):                       {"loose-old"},
		s("feed/https://news.example/rss"):       {"news-unread", "news-unread-starred", "news-old-starred"},
		s("user/1/state/com.google/starred"):     {"news-unread-starred", "news-old-starred"},
		s("user/12345/label/Tech"):               {"tech-new", "tech-unread", "tech-old-read"},
		"":                                       confAllDesc, // no s: the reading list
		s("user/-/state/com.google/broadcast"):   nil,
		s("user/-/label/Missing"):                nil,
		s("feed/999999"):                         nil,
		s("nonsense"):                            nil,
	}
	for q, want := range cases {
		got := ids(q)
		if want == nil {
			require.Empty(t, got, q)
			continue
		}
		require.Equal(t, l.decs(want...), got, q)
	}

	// xt excludes, it includes; both repeatable and ANDed [GR][RS].
	xtRead := q1("xt", stateRead)
	require.Equal(t, l.decs("tech-new", "news-unread", "news-unread-starred", "tech-unread", "loose-old"), ids(s(stateReadingList)+"&"+xtRead))
	require.Equal(t, l.decs("tech-new", "tech-unread"), ids(s("user/-/label/Tech")+"&"+xtRead))
	require.Equal(t, l.decs("tech-new", "news-unread", "tech-unread", "loose-old", "tech-old-read"), ids(s(stateReadingList)+"&"+q1("xt", stateStarred)))
	require.Equal(t, l.decs("tech-new", "news-unread", "tech-unread", "loose-old"), ids(s(stateReadingList)+"&"+xtRead+"&"+q1("xt", stateStarred)))
	require.Equal(t, l.decs("news-unread-starred"), ids(s(stateReadingList)+"&"+q1("it", stateStarred)+"&"+xtRead))
	require.Equal(t, l.decs("news-old-starred", "tech-old-read"), ids(s(stateReadingList)+"&"+q1("xt", "user/-/state/com.google/unread")))

	// r=o: oldest first [GR][RS].
	asc := l.decs(confAllDesc...)
	slices.Reverse(asc)
	require.Equal(t, asc, ids(s(stateReadingList)+"&r=o"))

	// ot (seconds): only items crawled at or after it, with Kipple's 120 s slack [GR][RS][K §3].
	ot := (l.ids["news-unread"] / 1_000_000) + 121
	require.Equal(t, l.decs("tech-new"), ids(s(stateReadingList)+"&ot="+strconv.FormatInt(ot, 10)))
	// nt (seconds): only items crawled before it [GR][RS].
	nt := l.ids["loose-old"]/1_000_000 - 1
	require.Equal(t, l.decs("news-old-starred", "tech-old-read"), ids(s(stateReadingList)+"&nt="+strconv.FormatInt(nt, 10)))

	// includeAllDirectStreamIds and output are accepted and do not change the ids [GR].
	require.Equal(t, l.decs(confAllDesc...), ids(s(stateReadingList)+"&includeAllDirectStreamIds=true&output=json"))

	// n defaults to 20 [GR][RS]; a smaller n pages with a string continuation, followed with c= until
	// it is absent, with no item repeated or skipped, in either order [GR][RS].
	for _, order := range []string{"", "&r=o"} {
		var all []string
		cont := ""
		for pages := 0; ; pages++ {
			require.Less(t, pages, 10)
			q := "stream/items/ids?n=3&" + s(stateReadingList) + order
			if cont != "" {
				q += "&c=" + url.QueryEscape(cont)
			}
			page, next := itemRefs(t, c.call(http.MethodGet, rd+q, "", nil))
			require.LessOrEqual(t, len(page), 3)
			all = append(all, page...)
			if next == "" {
				break
			}
			cont = next
		}
		want := l.decs(confAllDesc...)
		if order != "" {
			slices.Reverse(want)
		}
		require.Equal(t, want, all, "paging%s", order)
	}

	// An unusable c is ignored rather than an error [FR ctype_digit].
	require.Equal(t, l.decs(confAllDesc...), ids(s(stateReadingList)+"&c=not-a-number"))
}

func TestConformanceStreamContents(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)
	get := func(path string) confStream {
		t.Helper()
		return decodeStream(t, c.call(http.MethodGet, rd+path, "", nil))
	}

	// The stream may be in the path or in s= [GR][RS]; no stream is the reading list.
	forms := map[string][]string{
		"stream/contents/user/-/state/com.google/reading-list?n=50": confAllDesc,
		"stream/contents?n=50&" + q1("s", stateReadingList):         confAllDesc,
		"stream/contents?n=50":                                                     confAllDesc,
		"stream/contents/user/-/state/com.google/starred":                          {"news-unread-starred", "news-old-starred"},
		"stream/contents/user/-/label/Tech":                                        {"tech-new", "tech-unread", "tech-old-read"},
		"stream/contents/user/-/label/News%20%26%20Politics":                       {"news-unread", "news-unread-starred", "news-old-starred"},
		"stream/contents/" + feedID(l.tech):                                        {"tech-new", "tech-unread", "tech-old-read"},
		"stream/contents/feed/" + url.QueryEscape("https://tech.example/feed.xml"): {"tech-new", "tech-unread", "tech-old-read"},
		"stream/contents/feed/https://tech.example/feed.xml":                       {"tech-new", "tech-unread", "tech-old-read"},
		"stream/contents?" + q1("s", "feed/https://tech.example/feed.xml"):         {"tech-new", "tech-unread", "tech-old-read"},
	}
	for path, want := range forms {
		require.Equal(t, l.decs(want...), streamDecimals(t, get(path)), path)
	}

	// Envelope [GR][RS]: id is the stream, updated a number of seconds, items an array.
	st := get("stream/contents/" + feedID(l.tech))
	require.Equal(t, feedID(l.tech), st.ID)
	require.Equal(t, h.clk.Now().Unix(), st.Updated)
	st = get("stream/contents/feed/https://tech.example/feed.xml")
	require.Equal(t, "feed/https://tech.example/feed.xml", st.ID, "the envelope names the stream the client asked for")

	// A feed URL with a query string and a "//" after the scheme cannot travel in the path (the "?"
	// starts the query, and every "//" in a path is collapsed), but always resolves as an encoded s=
	// value, on every endpoint that takes a stream [GR][RS].
	odd := "https://odd.example/a//b/feed?format=rss&x=1"
	oddFeed := h.addFeed(odd, "Odd", "")
	oddItem := h.addItem(oddFeed, itemSeed{ID: baseID - 5*confHour, Title: "odd"})
	st = get("stream/contents?" + q1("s", "feed/"+odd))
	require.Equal(t, "feed/"+odd, st.ID)
	require.Equal(t, []string{strconv.FormatInt(oddItem, 10)}, streamDecimals(t, st))
	refs, _ := itemRefs(t, c.call(http.MethodGet, rd+"stream/items/ids?"+q1("s", "feed/"+odd), "", nil))
	require.Equal(t, []string{strconv.FormatInt(oddItem, 10)}, refs)
	c.write("mark-all-as-read", q1("s", "feed/"+odd))
	require.True(t, isRead(h, oddItem))

	// Item shape [GR][RS].
	st = get("stream/contents/" + feedID(l.news) + "?n=50")
	require.Len(t, st.Items, 3)
	it := st.Items[1] // news-unread-starred
	id := l.ids["news-unread-starred"]
	require.Equal(t, longID(l.dec("news-unread-starred")), it.ID, "items[].id is the long form with 16 hex digits")
	require.Equal(t, strconv.FormatInt(id, 10), it.TimestampUsec)
	require.Equal(t, strconv.FormatInt(id/1000, 10), it.CrawlTimeMsec)
	require.Equal(t, id/1_000_000-60, it.Published)
	require.NotZero(t, it.Updated)
	require.Equal(t, "news-unread-starred", it.Title)
	require.Equal(t, "Author news-unread-starred", it.Author)
	require.Equal(t, "https://example.org/news-unread-starred", it.Canonical[0].Href)
	require.Equal(t, "https://example.org/news-unread-starred", it.Alternate[0].Href)
	require.Equal(t, "text/html", it.Alternate[0].Type)
	require.Contains(t, it.Summary.Content, "body of news-unread-starred")
	// [GR] a client reads content.content or summary.content, whichever is present; [RS] sends
	// content, [RS] summary. Kipple sends both, the same article.
	require.Equal(t, it.Summary.Content, it.Content.Content)
	require.Equal(t, feedID(l.news), it.Origin.StreamID)
	require.Equal(t, "World News", it.Origin.Title)
	require.Equal(t, "https://news.example/", it.Origin.HTMLURL)
	require.Contains(t, it.Categories, stateReadingList)
	require.Contains(t, it.Categories, "user/-/label/News & Politics")
	require.Contains(t, it.Categories, stateStarred)
	require.NotContains(t, it.Categories, stateRead, "unread: no read category")
	require.Contains(t, st.Items[2].Categories, stateRead, "news-old-starred is read")

	// n, r=o, xt, ot and c work here as on stream/items/ids [GR][RS].
	st = get("stream/contents/user/-/state/com.google/reading-list?n=2&" + q1("xt", stateRead))
	require.Equal(t, l.decs("tech-new", "news-unread"), streamDecimals(t, st))
	require.NotEmpty(t, st.Continuation)
	st = get("stream/contents/user/-/state/com.google/reading-list?n=50&" + q1("xt", stateRead) + "&c=" + url.QueryEscape(st.Continuation))
	require.Equal(t, l.decs("news-unread-starred", "tech-unread", "loose-old"), streamDecimals(t, st))
	require.Empty(t, st.Continuation, "no continuation on the last page")
	st = get("stream/contents/user/-/label/Tech?r=o")
	require.Equal(t, l.decs("tech-old-read", "tech-unread", "tech-new"), streamDecimals(t, st))
	st = get("stream/contents?ot=" + strconv.FormatInt(l.ids["news-unread"]/1_000_000+121, 10))
	require.Equal(t, l.decs("tech-new"), streamDecimals(t, st))

	// An unknown stream is an empty 200, never an error.
	st = get("stream/contents/user/-/label/Missing")
	require.Empty(t, st.Items)
}

func TestConformanceStreamItemsContents(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	id := l.ids["tech-unread"]
	hex16 := FormatHex16(id)
	short := strings.TrimLeft(hex16, "0")
	forms := map[string]string{
		"long":           longID(l.dec("tech-unread")),       // [GR][RS]
		"long unpadded":  longIDPrefix + short,               // [RS] short prefixed hex
		"bare 16 hex":    hex16,                              // [RS]
		"decimal":        l.dec("tech-unread"),               // [GR][RS] itemRefs form
		"0x hex":         "0x" + short,                       // [K §3]
		"spaces trimmed": "  " + l.dec("tech-unread") + "  ", // [K §3]
	}
	for name, v := range forms {
		// POST (the documented method) with i= in the body.
		st := decodeStream(t, c.call(http.MethodPost, rd+"stream/items/contents", q1("i", v)+"&T="+url.QueryEscape(c.token), nil))
		require.Equal(t, []string{l.dec("tech-unread")}, streamDecimals(t, st), name)
		require.Equal(t, stateReadingList, st.ID, "[RS] the envelope id is the reading list")
	}

	// Several i=, any mix of forms, unknown ids absent; newest first, or oldest first with r=o [RS].
	body := strings.Join([]string{
		q1("i", longID(l.dec("tech-old-read"))),
		q1("i", l.dec("news-unread")),
		q1("i", FormatHex16(l.ids["loose-old"])),
		q1("i", "999"),
		q1("i", "not-an-id"),
	}, "&")
	st := decodeStream(t, c.call(http.MethodPost, rd+"stream/items/contents", body, nil))
	require.Equal(t, l.decs("news-unread", "loose-old", "tech-old-read"), streamDecimals(t, st))
	st = decodeStream(t, c.call(http.MethodPost, rd+"stream/items/contents?r=o", body, nil))
	require.Equal(t, l.decs("tech-old-read", "loose-old", "news-unread"), streamDecimals(t, st))

	// GET with i= in the query is answered the same way [K §6.6].
	st = decodeStream(t, c.call(http.MethodGet, rd+"stream/items/contents?"+body, "", nil))
	require.Len(t, st.Items, 3)

	// No ids: an empty list, 200.
	st = decodeStream(t, c.call(http.MethodPost, rd+"stream/items/contents", "T="+url.QueryEscape(c.token), nil))
	require.Empty(t, st.Items)

	// The ids/contents round trip a client does: ids, then contents for those ids.
	refs, _ := itemRefs(t, c.call(http.MethodGet, rd+"stream/items/ids?n=1000&"+q1("s", stateStarred), "", nil))
	var parts []string
	for _, r := range refs {
		parts = append(parts, q1("i", r))
	}
	st = decodeStream(t, c.call(http.MethodPost, rd+"stream/items/contents", strings.Join(parts, "&"), nil))
	require.Equal(t, refs, streamDecimals(t, st))
}
