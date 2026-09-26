package greader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
)

func subsOf(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	w := h.get(rd + "subscription/list?output=json")
	require.Equal(t, 200, w.Code)
	var out []map[string]any
	for _, s := range jsonBody(t, w)["subscriptions"].([]any) {
		out = append(out, s.(map[string]any))
	}
	return out
}

func findSub(subs []map[string]any, id string) map[string]any {
	for _, s := range subs {
		if s["id"] == id {
			return s
		}
	}
	return nil
}

func labelsOf(sub map[string]any) []string {
	var out []string
	for _, c := range sub["categories"].([]any) {
		out = append(out, c.(map[string]any)["label"].(string))
	}
	return out
}

func TestSubscriptionListShapeAndETag(t *testing.T) {
	h := newHarness(t)
	f1 := h.addFeed("https://a.example/feed.xml", "Alpha", "Comics")
	h.addFeed("https://b.example/rss", "", "")

	w := h.get(rd + "subscription/list?output=json")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
	etag := w.Header().Get("ETag")
	require.Regexp(t, `^"[0-9a-f]{16}"$`, etag)
	body := jsonBody(t, w)
	subs := body["subscriptions"].([]any)
	require.Len(t, subs, 2)
	a := findSub(subsOf(t, h), feedID(f1))
	require.Equal(t, "Alpha", a["title"])
	require.Equal(t, "https://a.example/feed.xml", a["url"])
	require.Equal(t, "https://a.example/", a["htmlUrl"])
	require.Equal(t, "", a["iconUrl"], "iconUrl is always present, empty when icon_urls is off")
	require.Equal(t, []string{"Comics"}, labelsOf(a))
	require.Equal(t, "user/-/label/Comics", a["categories"].([]any)[0].(map[string]any)["id"])

	// Conditional GET: match -> 304 empty; W/ prefix tolerated.
	w = h.do(http.MethodGet, base+rd+"subscription/list", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, 304, w.Code)
	require.Empty(t, w.Body.String())
	w = h.do(http.MethodGet, base+rd+"subscription/list", "", map[string]string{"If-None-Match": "W/" + etag})
	require.Equal(t, 304, w.Code)
	w = h.do(http.MethodGet, base+rd+"subscription/list", "", map[string]string{"If-None-Match": `"other"`})
	require.Equal(t, 200, w.Code)
}

func TestTagList(t *testing.T) {
	h := newHarness(t)
	h.addFolder("News")
	w := h.get(rd + "tag/list?output=json")
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"tags":[{"id":"user/-/state/com.google/starred"},{"id":"user/-/state/com.google/reading-list"},
		{"id":"user/-/label/Uncategorized","type":"folder"},{"id":"user/-/label/News","type":"folder"}]}`, w.Body.String())
	etag := w.Header().Get("ETag")
	w = h.do(http.MethodGet, base+rd+"tag/list?types=1", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, 304, w.Code)
	h.addFolder("Tech")
	w = h.do(http.MethodGet, base+rd+"tag/list", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, 200, w.Code)
}

func TestQuickAddNewThenOldETagStillGets200WithNewFeed(t *testing.T) {
	h := newHarness(t)
	h.addFeed("https://a.example/feed.xml", "Alpha", "")
	etag := h.get(rd + "subscription/list").Header().Get("ETag")

	w := h.post(rd+"subscription/quickadd", "T="+h.tok+"&quickadd="+url.QueryEscape("https://new.example/atom.xml"))
	require.Equal(t, 200, w.Code)
	res := jsonBody(t, w)
	require.EqualValues(t, 1, res["numResults"])
	require.Equal(t, "https://new.example/atom.xml", res["query"])
	require.Equal(t, "new.example", res["streamName"], "host until the first fetch")
	sid := res["streamId"].(string)
	require.Regexp(t, `^feed/\d+$`, sid)
	require.Equal(t, int32(1), h.wakes.Load(), "scheduler woken, no outbound HTTP")

	w = h.do(http.MethodGet, base+rd+"subscription/list", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, 200, w.Code, "old ETag no longer matches")
	require.NotNil(t, findSub(subsOf(t, h), sid))
	require.EqualValues(t, 1, q[int](h, "SELECT enabled FROM feeds WHERE id = ?", strings.TrimPrefix(sid, "feed/")))
	require.Equal(t, h.clk.Now().Unix(), q[int64](h, "SELECT next_fetch_at FROM feeds WHERE id = ?", strings.TrimPrefix(sid, "feed/")))
}

func TestQuickAddIsIdempotentAcrossSchemes(t *testing.T) {
	h := newHarness(t)
	id := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	w := h.post(rd+"subscription/quickadd", "T="+h.tok+"&quickadd="+url.QueryEscape("http://a.example/feed.xml"))
	require.Equal(t, 200, w.Code)
	res := jsonBody(t, w)
	require.EqualValues(t, 1, res["numResults"])
	require.Equal(t, feedID(id), res["streamId"])
	require.Equal(t, "Alpha", res["streamName"])
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM feeds"))
	require.Equal(t, int32(0), h.wakes.Load())

	// A leading feed/ is stripped.
	w = h.post(rd+"subscription/quickadd", "quickadd=feed/https://a.example/feed.xml")
	require.Equal(t, feedID(id), jsonBody(t, w)["streamId"])
}

func TestQuickAddInvalidURL(t *testing.T) {
	h := newHarness(t)
	for _, u := range []string{"not a url", "ftp://x.example/feed", "http://127.0.0.1/feed", "http://[::1]/feed", "http://169.254.169.254/latest"} {
		w := h.post(rd+"subscription/quickadd", "quickadd="+url.QueryEscape(u))
		require.Equal(t, 200, w.Code, u)
		res := jsonBody(t, w)
		require.EqualValues(t, 0, res["numResults"], u)
		require.NotEmpty(t, res["error"], u)
	}
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM feeds"))
}

func TestEditMoveRenameAndDefaultFolder(t *testing.T) {
	h := newHarness(t)
	id := h.addFeed("https://a.example/feed.xml", "Alpha", "Old")
	fid := feedID(id)

	// ac=edit with a title and a label that does not exist yet: folder created.
	w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+fid+"&t=Renamed;Title&a=user/-/label/New")
	require.Equal(t, 200, w.Code)
	require.Equal(t, "OK", w.Body.String())
	s := findSub(subsOf(t, h), fid)
	require.Equal(t, "Renamed;Title", s["title"])
	require.Equal(t, []string{"New"}, labelsOf(s))

	// r= without a= moves to Uncategorized.
	w = h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+fid+"&r=user/-/label/New")
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, []string{"Uncategorized"}, labelsOf(findSub(subsOf(t, h), fid)))

	// a= and r= together (NNW move between folders).
	h.post(rd+"subscription/edit", "ac=edit&s="+fid+"&r=user/-/label/Old&a=user/-/label/Comics")
	require.Equal(t, []string{"Comics"}, labelsOf(findSub(subsOf(t, h), fid)))

	// Unknown feeds and bad refs still get OK.
	w = h.post(rd+"subscription/edit", "ac=edit&s=feed/9999&t=x")
	require.Equal(t, 200, w.Code)
	require.Equal(t, "OK", w.Body.String())
	w = h.post(rd+"subscription/edit", "ac=bogus&s=zzz")
	require.Equal(t, "OK", w.Body.String())
}

func TestFolderNameWithSemicolonSurvives(t *testing.T) {
	h := newHarness(t)
	id := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	// NNW leaves ';' unencoded in a= values.
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(id)+"&a=user/-/label/Tech;News,Etc$")
	require.Equal(t, []string{"Tech;News,Etc$"}, labelsOf(findSub(subsOf(t, h), feedID(id))))
	w := h.get(rd + "tag/list")
	require.Contains(t, w.Body.String(), `"user/-/label/Tech;News,Etc$"`)
}

func TestEditSubscribeAndUnsubscribeByURL(t *testing.T) {
	h := newHarness(t)
	w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss")+
		"&s=feed/"+url.QueryEscape("https://two.example/rss")+"&t=One&t=Two&a=user/-/label/Reading")
	require.Equal(t, "OK", w.Body.String())
	subs := subsOf(t, h)
	require.Len(t, subs, 2)
	require.Equal(t, "One", subs[0]["title"])
	require.Equal(t, []string{"Reading"}, labelsOf(subs[0]))
	require.Equal(t, int32(2), h.wakes.Load())

	// Subscribing again moves/renames an existing feed only when asked.
	h.post(rd+"subscription/edit", "ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss"))
	require.Equal(t, "One", subsOf(t, h)[0]["title"])
	require.Equal(t, int32(2), h.wakes.Load())

	h.post(rd+"subscription/edit", "ac=unsubscribe&s=feed/"+url.QueryEscape("https://one.example/rss"))
	require.Len(t, subsOf(t, h), 1)
}

func TestUnsubscribeArchivesStarredItems(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	keep := h.addItem(f, itemSeed{Title: "starred one", Starred: true})
	gone := h.addItem(f, itemSeed{Title: "plain"})
	other := h.addFeed("https://b.example/feed.xml", "Beta", "")

	w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=unsubscribe&s="+feedID(f))
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM items WHERE id = ?", gone))
	arch := q[int64](h, "SELECT feed_id FROM items WHERE id = ?", keep)
	require.Equal(t, "archive", q[string](h, "SELECT disabled_reason FROM feeds WHERE id = ?", arch))
	require.Equal(t, "Alpha", q[string](h, "SELECT origin_title FROM items WHERE id = ?", keep))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM feeds WHERE id = ?", f))
	// The archive feed is listed while it holds items.
	require.NotNil(t, findSub(subsOf(t, h), feedID(arch)))
	require.NotNil(t, findSub(subsOf(t, h), feedID(other)))

	// The archived starred item is still in the starred stream.
	sw := h.get(rd + "stream/items/ids?output=json&s=" + starred)
	require.Contains(t, sw.Body.String(), `"`+FormatDecimal(keep)+`"`)

	// Unsubscribing the archive while it holds starred items is skipped: the reply
	// is still OK and the items survive.
	w = h.post(rd+"subscription/edit", "T="+h.tok+"&ac=unsubscribe&s="+feedID(arch))
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM items WHERE id = ?", keep))
	require.NotNil(t, findSub(subsOf(t, h), feedID(arch)))
}

func TestUnsubscribeStarredFeedAndArchiveTogether(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/feed.xml", "Alpha", "")
	keep := h.addItem(f, itemSeed{Title: "starred one", Starred: true})
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=unsubscribe&s="+feedID(f))
	arch := q[int64](h, "SELECT feed_id FROM items WHERE id = ?", keep)
	g := h.addFeed("https://b.example/feed.xml", "Beta", "")
	kept2 := h.addItem(g, itemSeed{Title: "starred two", Starred: true})

	// One request naming the archive feed (first) and a starred feed.
	w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=unsubscribe&s="+feedID(arch)+"&s="+feedID(g))
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM items WHERE id = ?", keep))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM items WHERE id = ?", kept2))
	require.Equal(t, 2, q[int](h, "SELECT count(*) FROM items WHERE starred = 1"))
}

func TestRenameTagAndMerge(t *testing.T) {
	h := newHarness(t)
	a := h.addFeed("https://a.example/f", "A", "Old Name")
	b := h.addFeed("https://b.example/f", "B", "Target")

	w := h.post(rd+"rename-tag", "T="+h.tok+"&s=user/-/label/Old Name&dest=user/-/label/Renamed")
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, []string{"Renamed"}, labelsOf(findSub(subsOf(t, h), feedID(a))))

	// Merge: the destination already exists (case-insensitively).
	h.post(rd+"rename-tag", "s=user/-/label/Renamed&dest=user/-/label/target")
	subs := subsOf(t, h)
	require.Equal(t, []string{"Target"}, labelsOf(findSub(subs, feedID(a))))
	require.Equal(t, []string{"Target"}, labelsOf(findSub(subs, feedID(b))))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Renamed'"))

	// Unknown tag: OK.
	w = h.post(rd+"rename-tag", "s=user/-/label/Nope&dest=user/-/label/X")
	require.Equal(t, "OK", w.Body.String())

	// The default folder may be renamed.
	h.post(rd+"rename-tag", "s=user/-/label/Uncategorized&dest=user/-/label/Inbox")
	require.Equal(t, "Inbox", q[string](h, "SELECT name FROM folders WHERE id = 1"))
}

func TestDisableTagRawBodyFallback(t *testing.T) {
	h := newHarness(t)
	news := h.addFeed("https://a.example/f", "A", "News & Politics+")
	plain := h.addFeed("https://b.example/f", "B", "Sports")
	// A different folder that the '&'-split value ("News ") could hit by mistake.
	h.addFolder("News")

	// NNW: T first, s last, no re-encoding of the folder id.
	w := h.do(http.MethodPost, base+rd+"disable-tag", "T="+h.tok+"&s=user/-/label/News & Politics+", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "OK", w.Body.String())
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'News & Politics+'"))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = 'News'"), "the look-alike folder survives")
	subs := subsOf(t, h)
	require.Equal(t, []string{"Uncategorized"}, labelsOf(findSub(subs, feedID(news))), "feeds fall back to Uncategorized")
	require.Equal(t, []string{"Sports"}, labelsOf(findSub(subs, feedID(plain))))

	// A plainly encoded id works too, and the default folder is never deleted.
	h.post(rd+"disable-tag", "T="+h.tok+"&s="+url.QueryEscape("user/-/label/Sports"))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM folders WHERE name = 'Sports'"))
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/Uncategorized")
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE id = 1"))
	w = h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/Nope")
	require.Equal(t, "OK", w.Body.String())
}

func TestSubscriptionImportOPML(t *testing.T) {
	h := newHarness(t)
	opmlBody := `<?xml version="1.0"?><opml version="2.0"><head/><body>
	<outline text="Tech; News &amp; More">
	  <outline type="rss" text="Alpha &amp; Omega; Inc" xmlUrl="https://a.example/rss?x=1;y=2"/>
	</outline></body></opml>`
	// NNW: raw OPML, text/xml, no T, exactly 200.
	w := h.do(http.MethodPost, base+rd+"subscription/import", opmlBody, map[string]string{"Content-Type": "text/xml"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, int32(1), h.wakes.Load())
	subs := subsOf(t, h)
	require.Len(t, subs, 1)
	require.Equal(t, "Alpha & Omega; Inc", subs[0]["title"])
	require.Equal(t, []string{"Tech; News & More"}, labelsOf(subs[0]))
	require.Contains(t, w.Header().Get("Cache-Control"), "no-cache")

	// Requires the header; a T does not authenticate the raw route.
	w = h.do(http.MethodPost, base+rd+"subscription/import", opmlBody, map[string]string{"Authorization": "", "Content-Type": "text/xml"})
	require.Equal(t, 401, w.Code)

	// Re-import adds nothing; garbage is a 400 that never wedges the queue with a 5xx.
	w = h.do(http.MethodPost, base+rd+"subscription/import", opmlBody, map[string]string{"Content-Type": "text/xml"})
	require.Equal(t, 200, w.Code)
	require.Len(t, subsOf(t, h), 1)
	w = h.do(http.MethodPost, base+rd+"subscription/import", "<<<not xml", map[string]string{"Content-Type": "text/xml"})
	require.Equal(t, 400, w.Code)
}

func TestSubscriptionExport(t *testing.T) {
	h := newHarness(t)
	h.addFeed("https://a.example/rss", "Alpha", "Comics")
	w := h.get(rd + "subscription/export")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Header().Get("Content-Disposition"), "attachment")
	require.Contains(t, w.Body.String(), `xmlUrl="https://a.example/rss"`)
	require.Contains(t, w.Body.String(), "Comics")
}

func TestUnreadCount(t *testing.T) {
	h := newHarness(t)
	f1 := h.addFeed("https://a.example/f", "A", "Comics")
	f2 := h.addFeed("https://b.example/f", "B", "Comics")
	f3 := h.addFeed("https://c.example/f", "C", "")
	h.addItem(f1, itemSeed{Title: "1"})
	last := h.addItem(f1, itemSeed{Title: "2"})
	h.addItem(f1, itemSeed{Title: "read", Read: true})
	h.addItem(f2, itemSeed{Title: "3"})
	h.addItem(f3, itemSeed{Title: "trimmed away"})
	h.trim(h.addItem(f3, itemSeed{Title: "unread trimmed"}), true)

	w := h.get(rd + "unread-count?output=json")
	require.Equal(t, 200, w.Code)
	res := jsonBody(t, w)
	require.EqualValues(t, 4, res["max"])
	counts := map[string]map[string]any{}
	for _, u := range res["unreadcounts"].([]any) {
		m := u.(map[string]any)
		counts[m["id"].(string)] = m
	}
	require.EqualValues(t, 4, counts[stateReadingList]["count"])
	require.EqualValues(t, 3, counts["user/-/label/Comics"]["count"])
	require.EqualValues(t, 2, counts[feedID(f1)]["count"])
	require.EqualValues(t, 1, counts[feedID(f2)]["count"])
	require.EqualValues(t, 1, counts[feedID(f3)]["count"], "ledger rows are never counted")
	require.Equal(t, FormatDecimal(last), counts[feedID(f1)]["newestItemTimestampUsec"])
}

func TestIconEndpoint(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("https://a.example/f", "A", "")
	require.NoError(t, execSQL(h, "INSERT INTO feed_icons (feed_id, data, content_type, hash, fetched_at) VALUES (?, x'89504e47', 'image/png', 'abc123', 1)", f))

	// On by default (a stored row is only needed to turn it off); no public URL yet, so no iconUrl.
	require.Equal(t, "", subsOf(t, h)[0]["iconUrl"])
	require.NoError(t, execSQL(h, "INSERT INTO settings (key, value) VALUES ('greader.icon_urls', 'false')"))
	require.Equal(t, 404, h.do(http.MethodGet, base+"/icon/"+FormatDecimal(f)+"-abc123", "", map[string]string{"Authorization": ""}).Code, "off when set to false")
	h.api.opt.PublicURL = "https://rss.example.org/"
	require.Equal(t, "", subsOf(t, h)[0]["iconUrl"], "off when set to false")
	require.NoError(t, execSQL(h, "DELETE FROM settings WHERE key = 'greader.icon_urls'")) // back to the default: on
	h.api.opt.PublicURL = "https://rss.example.org/"
	require.Equal(t, "https://rss.example.org/api/greader.php/icon/"+FormatDecimal(f)+"-abc123", subsOf(t, h)[0]["iconUrl"])
	w := h.do(http.MethodGet, base+"/icon/"+FormatDecimal(f)+"-abc123", "", map[string]string{"Authorization": ""})
	require.Equal(t, 200, w.Code, "icons are unauthenticated")
	require.Equal(t, "image/png", w.Header().Get("Content-Type"))
	require.Equal(t, "\x89PNG", w.Body.String())
	require.Equal(t, 404, h.do(http.MethodGet, base+"/icon/"+FormatDecimal(f)+"-wrong", "", nil).Code)
}

// Every feed.changed event carries the feed id as a string, like the web API and
// the scheduler do (a JSON number would lose precision in JS clients).
func TestFeedChangedEventsCarryStringFeedID(t *testing.T) {
	h := newHarness(t)
	hub := events.New()
	h.api.opt.Events = hub
	sub := hub.Subscribe(0)
	defer sub.Close()
	feedChanged := func() []map[string]any {
		var out []map[string]any
		for {
			select {
			case ev := <-sub.C:
				if ev.Type == "feed.changed" {
					var m map[string]any
					require.NoError(t, json.Unmarshal(ev.Data, &m))
					out = append(out, m)
				}
			default:
				return out
			}
		}
	}
	requireStrings := func(evs []map[string]any, n int, what string) {
		t.Helper()
		require.Len(t, evs, n, what)
		for _, m := range evs {
			require.IsType(t, "", m["feed_id"], what)
			require.NotEmpty(t, m["feed_id"], what)
		}
	}

	// subscribe (afterSubscribe)
	require.Equal(t, "OK", h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss")).Body.String())
	requireStrings(feedChanged(), 1, "subscribe")

	// edit / unsubscribe (publishFeeds)
	id := h.addFeed("https://a.example/feed.xml", "Alpha", "Old")
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(id)+"&t=Renamed")
	evs := feedChanged()
	require.NotEmpty(t, evs, "edit")
	requireStrings(evs, len(evs), "edit")
	require.Equal(t, strconv.FormatInt(id, 10), evs[0]["feed_id"])
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=unsubscribe&s="+feedID(id))
	evs = feedChanged()
	requireStrings(evs, len(evs), "unsubscribe")

	// OPML import
	opmlBody := `<?xml version="1.0"?><opml version="2.0"><head/><body><outline type="rss" text="Z" xmlUrl="https://z.example/rss"/></body></opml>`
	w := h.do(http.MethodPost, base+rd+"subscription/import", opmlBody, map[string]string{"Content-Type": "text/xml"})
	require.Equal(t, 200, w.Code)
	requireStrings(feedChanged(), 1, "import")
}

// NNW sends folder names with '&' and '+' unencoded; every label-carrying
// parameter must see the whole name and never create a truncated folder.
var rawFolderNames = []string{"News & Politics+", "R&D", "A+B", "Tom & Jerry", "Café & Thé", "日本&ニュース", "Plain Name"}

func folderCount(h *harness) int { return q[int](h, "SELECT count(*) FROM folders") }

func TestRawLabelNamesSubscriptionEdit(t *testing.T) {
	for _, name := range rawFolderNames {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			f := h.addFeed("https://a.example/f", "A", "")
			before := folderCount(h)
			id := "user/-/label/" + nnwEnc(name)
			// NNW encodes '&' and '+', so the name arrives exactly (phase 1 parser).
			want := name
			// edit, a= last and in the middle (before T=), then r= to go back.
			h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s=feed/"+strconv.FormatInt(f, 10)+"&a="+id)
			require.Equal(t, before+1, folderCount(h), "exactly one folder created")
			require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE name = ?", want), "whole name")
			h.post(rd+"subscription/edit", "ac=edit&s=feed/"+strconv.FormatInt(f, 10)+"&a="+id+"&T="+h.tok)
			require.Equal(t, before+1, folderCount(h))
			require.Equal(t, []string{want}, labelsOf(findSub(subsOf(t, h), feedID(f))))
			h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s=feed/"+strconv.FormatInt(f, 10)+"&r="+id)
			require.Equal(t, []string{"Uncategorized"}, labelsOf(findSub(subsOf(t, h), feedID(f))))

			// subscribe (existing feed URL, no fetch) files it under the whole name too.
			h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/https://a.example/f&a="+id)
			require.Equal(t, before+1, folderCount(h))
			require.Equal(t, []string{want}, labelsOf(findSub(subsOf(t, h), feedID(f))))
		})
	}
}

func TestRawLabelNamesRenameAndDisable(t *testing.T) {
	for _, name := range rawFolderNames {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			f := h.addFeed("https://a.example/f", "A", name)
			h.addFolder("News")
			h.addFolder("R")
			h.addFolder("A")
			h.addFolder("Tom ")
			before := folderCount(h)
			id := "user/-/label/" + nnwEnc(name)
			// rename-tag: s= raw in the middle, dest= raw last (NNW order is T, s, dest).
			h.post(rd+"rename-tag", "T="+h.tok+"&s="+id+"&dest=user/-/label/"+nnwEnc("Renamed & Co+"))
			require.Equal(t, before, folderCount(h), "rename creates nothing")
			require.Equal(t, []string{"Renamed & Co+"}, labelsOf(findSub(subsOf(t, h), feedID(f))))
			// and back, dest carrying the odd name.
			h.post(rd+"rename-tag", "s=user/-/label/"+nnwEnc("Renamed & Co+")+"&T="+h.tok+"&dest="+id)
			require.Equal(t, before, folderCount(h))
			require.Equal(t, []string{name}, labelsOf(findSub(subsOf(t, h), feedID(f))))
			// disable-tag, raw last and raw in the middle.
			h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/"+name)
			require.Equal(t, before-1, folderCount(h))
			require.Equal(t, 4, q[int](h, "SELECT count(*) FROM folders WHERE name IN ('News','R','A','Tom ')"), "look-alikes survive")
			require.Equal(t, []string{"Uncategorized"}, labelsOf(findSub(subsOf(t, h), feedID(f))))
		})
	}
}

func TestParseUserPath(t *testing.T) {
	n, ok := parseUserPath("user/-/label/Tech", "/label/")
	require.True(t, ok)
	require.Equal(t, "Tech", n)
	_, ok = parseUserPath("user/-/label/  ", "/label/")
	require.False(t, ok)
	_, ok = parseUserPath("user/-/state/com.google/", "/state/com.google/")
	require.False(t, ok)
	n, ok = parseUserPath("user/1/state/com.google/read", "/state/com.google/")
	require.True(t, ok)
	require.Equal(t, "read", n)
	_, ok = parseUserPath("feed/1", "/label/")
	require.False(t, ok)
}

// nnwEnc encodes a folder name the way NNW does for a=/r=/t=/rename-tag: percent
// encoding with '&' and '+' encoded too (netnewswire.md section 4).
func nnwEnc(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// Every folder mutation over the Reader API announces folder.changed.
func TestFolderChangedEventsFromReaderAPI(t *testing.T) {
	h := newHarness(t)
	hub := events.New()
	h.api.opt.Events = hub
	sub := hub.Subscribe(0)
	defer sub.Close()
	count := func() int {
		n := 0
		for {
			select {
			case ev := <-sub.C:
				if ev.Type == "folder.changed" {
					n++
				}
			default:
				return n
			}
		}
	}
	id := h.addFeed("https://a.example/feed.xml", "Alpha", "Old")
	count()

	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss")+"&a=user/-/label/Made")
	require.Equal(t, 1, count(), "subscribe into a new folder")
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(id)+"&t=OnlyATitle")
	require.Equal(t, 0, count(), "a title edit moves nothing")
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(id)+"&a=user/-/label/Elsewhere")
	require.Equal(t, 1, count(), "edit into a new folder")
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(id)+"&r=user/-/label/Elsewhere")
	require.Equal(t, 1, count(), "edit to the default folder")
	h.post(rd+"rename-tag", "T="+h.tok+"&s=user/-/label/Made&dest=user/-/label/Merged")
	require.Equal(t, 1, count(), "rename-tag")
	h.post(rd+"rename-tag", "T="+h.tok+"&s=user/-/label/Nope&dest=user/-/label/Zip")
	require.Equal(t, 0, count(), "renaming a folder that does not exist changes nothing")
	h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/Merged")
	require.Equal(t, 1, count(), "disable-tag")
	h.post(rd+"subscription/import", `<?xml version="1.0"?><opml version="2.0"><head/><body><outline text="Imp"><outline type="rss" text="B" xmlUrl="https://b.example/f.xml"/></outline></body></opml>`)
	require.Equal(t, 1, count(), "import creating a folder")
}

func TestQuickAddFetchNowOnlyWhenSettingOn(t *testing.T) {
	h := newHarness(t)
	var calls []int64
	h.api.opt.FetchNow = func(_ context.Context, id int64, wait time.Duration) {
		require.Equal(t, subscribeFetchWait, wait)
		calls = append(calls, id)
	}
	add := func(u string) map[string]any {
		w := h.post(rd+"subscription/quickadd", "T="+h.tok+"&quickadd="+url.QueryEscape(u))
		require.Equal(t, 200, w.Code)
		return jsonBody(t, w)
	}
	add("https://off.example/feed.xml")
	require.Empty(t, calls, "default: no synchronous fetch")

	require.NoError(t, h.db.SetSettings(context.Background(), map[string]any{"greader.subscribe_fetch_now": true}))
	res := add("https://on.example/feed.xml")
	require.Len(t, calls, 1)
	require.Equal(t, "feed/"+strconv.FormatInt(calls[0], 10), res["streamId"])

	add("https://on.example/feed.xml")
	require.Len(t, calls, 1, "an existing feed is never fetched by a client")
}
