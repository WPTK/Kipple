package greader

// Conformance: subscriptions, folder labels, unread counts and OPML. See
// conformance_test.go for the reference tags.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// confSubs returns subscription/list keyed by stream id.
func (c *confClient) confSubs() map[string]map[string]any {
	c.t.Helper()
	m := c.getJSON(rd + "subscription/list?output=json")
	out := map[string]map[string]any{}
	for _, s := range m["subscriptions"].([]any) {
		sub := s.(map[string]any)
		out[sub["id"].(string)] = sub
	}
	return out
}

// confLabels returns the label ids in tag/list, in order.
func (c *confClient) confLabels() []string {
	c.t.Helper()
	var out []string
	for _, tg := range c.getJSON(rd + "tag/list?output=json")["tags"].([]any) {
		id := tg.(map[string]any)["id"].(string)
		if strings.HasPrefix(id, labelPrefix) {
			out = append(out, id)
		}
	}
	return out
}

func subLabel(sub map[string]any) string {
	cats := sub["categories"].([]any)
	if len(cats) == 0 {
		return ""
	}
	return cats[0].(map[string]any)["id"].(string)
}

func TestConformanceSubscriptionList(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	// [FR][MF] {"subscriptions":[{id, title, categories:[{id, label}], url, htmlUrl, iconUrl}]}; the
	// id is feed/<number> and every feed has its folder as its one category. output=json is the
	// documented request [MF]; JSON is returned whatever output says.
	first := c.call(http.MethodGet, rd+"subscription/list?output=json", "", nil)
	for _, path := range []string{"subscription/list", "subscription/list?output=xml"} {
		r := c.call(http.MethodGet, rd+path, "", nil)
		require.Equal(t, first.body, r.body, path)
	}
	subs := c.confSubs()
	require.Len(t, subs, 3)
	tech := subs[feedID(l.tech)]
	require.NotNil(t, tech)
	require.Equal(t, "Tech Daily", tech["title"])
	require.Equal(t, "https://tech.example/feed.xml", tech["url"])
	require.Equal(t, "https://tech.example/", tech["htmlUrl"])
	require.IsType(t, "", tech["iconUrl"], "[FR][MF] iconUrl is a string (empty when there is no icon)")
	require.Equal(t, []any{map[string]any{"id": "user/-/label/Tech", "label": "Tech"}}, tech["categories"])
	require.Equal(t, "user/-/label/News & Politics", subLabel(subs[feedID(l.news)]), "names are plain JSON strings, never HTML-escaped")
	require.Equal(t, "user/-/label/Uncategorized", subLabel(subs[feedID(l.loose)]), "a feed outside any folder is in the default folder")

	// [FR][MF] iconUrl is an absolute URL a client fetches without credentials.
	require.NoError(t, execSQL(h, "INSERT INTO feed_icons (feed_id, data, content_type, hash, fetched_at) VALUES (?, x'89504e47', 'image/png', 'abc123', 1)", l.tech))
	h.api.opt.PublicURL = c.base[:len(c.base)-len(base)]
	icon := c.confSubs()[feedID(l.tech)]["iconUrl"].(string)
	require.True(t, strings.HasPrefix(icon, c.base+"/icon/"), icon)
	anon := *c.client
	anon.auth, anon.base = "", ""
	r := anon.doAny(http.MethodGet, icon, "", nil)
	require.Equal(t, 200, r.code)
	require.Equal(t, "image/png", r.header.Get("Content-Type"))

	// [K §6.9] ETag + If-None-Match → 304, a change → 200.
	first = c.call(http.MethodGet, rd+"subscription/list?output=json", "", nil)
	etag := first.header.Get("ETag")
	require.NotEmpty(t, etag)
	r = c.call(http.MethodGet, rd+"subscription/list?output=json", "", map[string]string{"If-None-Match": etag})
	require.Equal(t, http.StatusNotModified, r.code)
	require.Empty(t, r.body)
}

func TestConformanceTagList(t *testing.T) {
	h := newHarness(t)
	seedConf(h)
	h.addFolder("Empty")
	c := newConf(t, h)

	// [FR] tags: starred and reading-list states first, then one {"id":"user/-/label/<name>","type":"folder"}
	// per folder. [MF] lists starred and the labels.
	tags := c.getJSON(rd + "tag/list?output=json")["tags"].([]any)
	ids := map[string]map[string]any{}
	for _, tg := range tags {
		m := tg.(map[string]any)
		ids[m["id"].(string)] = m
	}
	require.Contains(t, ids, stateStarred)
	require.Contains(t, ids, stateReadingList)
	for _, name := range []string{"Tech", "News & Politics", "Uncategorized", "Empty"} {
		m := ids[labelPrefix+name]
		require.NotNil(t, m, name)
		require.Equal(t, "folder", m["type"], name)
	}
}

func TestConformanceQuickAdd(t *testing.T) {
	h := newHarness(t)
	c := newConf(t, h)

	// [GR][FR][MF] quickadd=<url> → {numResults:1, query, streamId:"feed/<id>", streamName}.
	quick := func(v string) map[string]any {
		t.Helper()
		r := c.call(http.MethodPost, rd+"subscription/quickadd", q1("quickadd", v)+"&T="+url.QueryEscape(c.token), nil)
		require.Equal(t, 200, r.code, r.body)
		require.Contains(t, r.header.Get("Content-Type"), "application/json")
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(r.body), &m))
		return m
	}
	m := quick("https://added.example/feed.xml")
	require.EqualValues(t, 1, m["numResults"])
	require.Equal(t, "https://added.example/feed.xml", m["query"])
	sid := m["streamId"].(string)
	require.True(t, strings.HasPrefix(sid, "feed/"))
	// Not fetched yet, the feed is named by its URL (never an empty name), the same in the list.
	require.Equal(t, "https://added.example/feed.xml", m["streamName"])
	require.Contains(t, c.confSubs(), sid, "the new feed is in the next subscription/list")
	require.Equal(t, "https://added.example/feed.xml", c.confSubs()[sid]["title"])

	// [FR] a leading feed/ is stripped; adding the same URL again returns the same stream.
	require.Equal(t, sid, quick("feed/https://added.example/feed.xml")["streamId"])

	// [FR][MF] an unusable URL is numResults 0, still 200.
	bad := quick("not a url")
	require.EqualValues(t, 0, bad["numResults"])
	require.Len(t, c.confSubs(), 1)

	// Parameters in the query string of the POST work too [MF].
	r := c.call(http.MethodPost, rd+"subscription/quickadd?"+q1("quickadd", "https://second.example/rss"), "T="+url.QueryEscape(c.token), nil)
	require.Equal(t, 200, r.code)
	require.Len(t, c.confSubs(), 2)
}

func TestConformanceSubscriptionEdit(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	// subscribe: s=feed/<url>, optional t=<title> and a=<label> [GR][FR][MF]. A new folder is created.
	c.write("subscription/edit", "ac=subscribe&"+q1("s", "feed/https://sub.example/feed.xml")+"&"+q1("t", "Chosen Title")+"&"+q1("a", "user/-/label/Reading"))
	var sid string
	for id, s := range c.confSubs() {
		if s["url"] == "https://sub.example/feed.xml" {
			sid = id
			require.Equal(t, "Chosen Title", s["title"])
			require.Equal(t, "user/-/label/Reading", subLabel(s))
		}
	}
	require.NotEmpty(t, sid)
	require.Contains(t, c.confLabels(), "user/-/label/Reading")

	// edit: s=feed/<id> with t= renames [FR][MF].
	c.write("subscription/edit", "ac=edit&"+q1("s", sid)+"&"+q1("t", "Renamed"))
	require.Equal(t, "Renamed", c.confSubs()[sid]["title"])

	// edit: a=<label> moves the feed [FR][MF]; the label may be addressed with the user id form [GR].
	c.write("subscription/edit", "ac=edit&"+q1("s", feedID(l.loose))+"&"+q1("a", "user/1/label/Tech"))
	require.Equal(t, "user/-/label/Tech", subLabel(c.confSubs()[feedID(l.loose)]))

	// edit: r=<label> without a= moves it out to the default folder [FR].
	c.write("subscription/edit", "ac=edit&"+q1("s", feedID(l.loose))+"&"+q1("r", "user/-/label/Tech"))
	require.Equal(t, "user/-/label/Uncategorized", subLabel(c.confSubs()[feedID(l.loose)]))

	// edit by feed/<url> as well as feed/<id> [FR searchByUrl].
	c.write("subscription/edit", "ac=edit&"+q1("s", "feed/https://loose.example/atom")+"&"+q1("t", "By URL"))
	require.Equal(t, "By URL", c.confSubs()[feedID(l.loose)]["title"])

	// Several s= with one t= each, in one request [FR].
	c.write("subscription/edit", "ac=edit&"+q1("s", feedID(l.tech))+"&"+q1("t", "T1")+"&"+q1("s", feedID(l.news))+"&"+q1("t", "N1"))
	subs := c.confSubs()
	require.Equal(t, "T1", subs[feedID(l.tech)]["title"])
	require.Equal(t, "N1", subs[feedID(l.news)]["title"])

	// ac and s in the query string of the POST [MF merged form values].
	r := c.call(http.MethodPost, rd+"subscription/edit?ac=unsubscribe&"+q1("s", sid), "T="+url.QueryEscape(c.token), nil)
	require.Equal(t, 200, r.code)
	require.NotContains(t, c.confSubs(), sid)

	// unsubscribe several at once [GR][FR].
	c.write("subscription/edit", "ac=unsubscribe&"+q1("s", feedID(l.tech))+"&"+q1("s", feedID(l.loose)))
	subs = c.confSubs()
	require.Len(t, subs, 1)
	require.Contains(t, subs, feedID(l.news))

	// An unknown feed or action is still OK, so a client's queue never stalls [K §6.9].
	c.write("subscription/edit", "ac=edit&s=feed/999999&t=x")
	c.write("subscription/edit", "ac=frobnicate&"+q1("s", feedID(l.news)))
}

func TestConformanceRenameAndDisableTag(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	// rename-tag: s=<old label>, dest=<new label> [GR][FR][MF].
	c.write("rename-tag", q1("s", "user/-/label/Tech")+"&"+q1("dest", "user/-/label/Technology"))
	labels := c.confLabels()
	require.Contains(t, labels, "user/-/label/Technology")
	require.NotContains(t, labels, "user/-/label/Tech")
	require.Equal(t, "user/-/label/Technology", subLabel(c.confSubs()[feedID(l.tech)]))

	// The label's stream follows the new name.
	r := c.call(http.MethodGet, rd+"stream/items/ids?n=50&"+q1("s", "user/-/label/Technology"), "", nil)
	ids, _ := itemRefs(t, r)
	require.Len(t, ids, 3)

	// disable-tag: s repeatable; the folder goes and its feeds move to the default folder [FR][MF].
	c.write("disable-tag", q1("s", "user/-/label/Technology")+"&"+q1("s", "user/-/label/News & Politics"))
	labels = c.confLabels()
	require.NotContains(t, labels, "user/-/label/Technology")
	require.NotContains(t, labels, "user/-/label/News & Politics")
	subs := c.confSubs()
	require.Equal(t, "user/-/label/Uncategorized", subLabel(subs[feedID(l.tech)]))
	require.Equal(t, "user/-/label/Uncategorized", subLabel(subs[feedID(l.news)]))

	// Unknown labels are OK no-ops [K §6.9].
	c.write("rename-tag", q1("s", "user/-/label/Nope")+"&"+q1("dest", "user/-/label/Other"))
	c.write("disable-tag", q1("s", "user/-/label/Nope"))
}

func TestConformanceUnreadCount(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)

	// [GR][FR] {"max":n,"unreadcounts":[{"id","count","newestItemTimestampUsec"}]} with one entry for
	// the reading list, each label and each feed; counts are numbers, timestamps decimal strings.
	m := c.getJSON(rd + "unread-count?output=json")
	require.EqualValues(t, 5, m["max"])
	got := map[string]map[string]any{}
	for _, u := range m["unreadcounts"].([]any) {
		e := u.(map[string]any)
		got[e["id"].(string)] = e
		require.IsType(t, float64(0), e["count"])
		require.IsType(t, "", e["newestItemTimestampUsec"])
	}
	want := map[string]struct {
		n      int
		newest string
	}{
		stateReadingList:               {5, l.dec("tech-new")},
		"user/-/label/Tech":            {2, l.dec("tech-new")},
		"user/-/label/News & Politics": {2, l.dec("news-unread")},
		"user/-/label/Uncategorized":   {1, l.dec("loose-old")},
		feedID(l.tech):                 {2, l.dec("tech-new")},
		feedID(l.news):                 {2, l.dec("news-unread")},
		feedID(l.loose):                {1, l.dec("loose-old")},
	}
	require.Len(t, got, len(want))
	for id, w := range want {
		require.EqualValues(t, w.n, got[id]["count"], id)
		require.Equal(t, w.newest, got[id]["newestItemTimestampUsec"], id)
	}
	// The counts agree with the unread listing a client builds [K §6.9].
	ids, _ := itemRefs(t, c.call(http.MethodGet, rd+"stream/items/ids?n=1000&s="+url.QueryEscape(stateReadingList)+"&xt="+url.QueryEscape(stateRead), "", nil))
	require.Len(t, ids, 5)
}

func TestConformanceOPML(t *testing.T) {
	h := newHarness(t)
	seedConf(h)
	c := newConf(t, h)

	// [FR] subscription/export: an OPML attachment of every subscription.
	r := c.call(http.MethodGet, rd+"subscription/export", "", nil)
	require.Equal(t, 200, r.code)
	require.Contains(t, r.header.Get("Content-Type"), "opml")
	require.Contains(t, r.body, `xmlUrl="https://tech.example/feed.xml"`)
	require.Contains(t, r.body, "<opml")

	// [FR] subscription/import: the raw OPML document is the POST body; answers 200 OK.
	opml := `<?xml version="1.0"?><opml version="2.0"><head><title>x</title></head><body>
<outline text="Imported"><outline type="rss" text="New One" xmlUrl="https://imported.example/feed.xml"/></outline>
</body></opml>`
	r = c.call(http.MethodPost, rd+"subscription/import", opml, map[string]string{"Content-Type": "text/x-opml"})
	require.Equal(t, 200, r.code, r.body)
	require.Equal(t, "OK", r.body)
	found := false
	for _, s := range c.confSubs() {
		if s["url"] == "https://imported.example/feed.xml" {
			found = true
			require.Equal(t, "user/-/label/Imported", subLabel(s))
		}
	}
	require.True(t, found)

	// A body that is not OPML is 400 [FR badRequest].
	r = c.call(http.MethodPost, rd+"subscription/import", "not xml", nil)
	require.Equal(t, 400, r.code)
}
