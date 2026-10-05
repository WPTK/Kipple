package greader

// Conformance: edit-tag and mark-all-as-read. See conformance_test.go for the
// reference tags.

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConformanceEditTag(t *testing.T) {
	h := newHarness(t)
	l := seedConf(h)
	c := newConf(t, h)
	i := func(names ...string) string {
		parts := make([]string, len(names))
		for k, n := range names {
			// Mix the id forms a client may send in one batch [GR][FR][MF].
			switch k % 3 {
			case 0:
				parts[k] = q1("i", longID(l.dec(n)))
			case 1:
				parts[k] = q1("i", l.dec(n))
			default:
				parts[k] = q1("i", FormatHex16(l.ids[n]))
			}
		}
		return strings.Join(parts, "&")
	}
	read := func(n string) bool { return isRead(h, l.ids[n]) }
	starred := func(n string) bool { return isStarred(h, l.ids[n]) }
	unread3 := []string{"tech-unread", "news-unread", "loose-old"}

	// a=read on a batch marks every id read [GR][FR][MF].
	c.write("edit-tag", i(unread3...)+"&"+q1("a", stateRead))
	for _, n := range unread3 {
		require.True(t, read(n), n)
	}
	// Replaying the same request is a no-op and still OK.
	c.write("edit-tag", i(unread3...)+"&"+q1("a", stateRead))

	// r=read marks unread [GR][FR][MF].
	c.write("edit-tag", i(unread3...)+"&"+q1("r", stateRead))
	for _, n := range unread3 {
		require.False(t, read(n), n)
	}

	// a=kept-unread marks unread, r=kept-unread marks read [GR][MF].
	c.write("edit-tag", i("tech-old-read")+"&"+q1("a", "user/-/state/com.google/kept-unread"))
	require.False(t, read("tech-old-read"))
	c.write("edit-tag", i("tech-old-read")+"&"+q1("r", "user/-/state/com.google/kept-unread"))
	require.True(t, read("tech-old-read"))

	// a=starred / r=starred [GR][FR][MF].
	c.write("edit-tag", i("tech-new", "loose-old")+"&"+q1("a", stateStarred))
	require.True(t, starred("tech-new"))
	require.True(t, starred("loose-old"))
	c.write("edit-tag", i("tech-new", "loose-old")+"&"+q1("r", stateStarred))
	require.False(t, starred("tech-new"))
	require.False(t, starred("loose-old"))

	// a= and r= together, and repeated a=, in one request [GR][FR].
	c.write("edit-tag", i("news-unread-starred")+"&"+q1("a", stateRead)+"&"+q1("r", stateStarred))
	require.True(t, read("news-unread-starred"))
	require.False(t, starred("news-unread-starred"))
	c.write("edit-tag", i("tech-unread")+"&"+q1("a", stateRead)+"&"+q1("a", stateStarred))
	require.True(t, read("tech-unread"))
	require.True(t, starred("tech-unread"))

	// The user/<id>/state form is the same state [GR].
	c.write("edit-tag", i("tech-unread")+"&"+q1("r", "user/1/state/com.google/read"))
	require.False(t, read("tech-unread"))

	// i= in the query string of the POST, tags in the body [K §6.2].
	r := c.call(http.MethodPost, rd+"edit-tag?"+i("tech-unread"), q1("a", stateRead)+"&T="+url.QueryEscape(c.token), nil)
	require.Equal(t, 200, r.code)
	require.True(t, read("tech-unread"))

	// Labels, broadcast, like and unknown tags are accepted and change no state [K §6.7]; unknown and
	// unparseable ids, and no ids at all, are still OK so a client's queue never stalls [FR][K §6.7].
	before := q[int](h, "SELECT count(*) FROM items WHERE read = 1")
	c.write("edit-tag", i("tech-new")+"&"+q1("a", "user/-/label/Tech")+"&"+q1("a", "user/-/state/com.google/broadcast")+"&"+q1("a", "user/-/state/com.google/like")+"&"+q1("a", "user/-/state/com.google/tracking-body-link-used"))
	require.Equal(t, before, q[int](h, "SELECT count(*) FROM items WHERE read = 1"))
	c.write("edit-tag", "i=999&i=not-an-id&"+q1("a", stateRead))
	c.write("edit-tag", q1("a", stateRead))

	// A batch the size clients send (hundreds of ids) in one request.
	var many []int64
	for k := 0; k < 300; k++ {
		many = append(many, h.addItem(l.loose, itemSeed{Title: "bulk " + strconv.Itoa(k)}))
	}
	var parts []string
	for _, id := range many {
		parts = append(parts, q1("i", FormatLongID(id)))
	}
	c.write("edit-tag", strings.Join(parts, "&")+"&"+q1("a", stateRead))
	require.Equal(t, 300, q[int](h, "SELECT count(*) FROM items WHERE read = 1 AND title LIKE 'bulk %'"))
}

func TestConformanceMarkAllAsRead(t *testing.T) {
	unreadOf := func(h *harness) []string {
		var out []string
		rows, err := h.db.Reader().Query("SELECT title FROM items WHERE read = 0 ORDER BY id DESC")
		require.NoError(h.t, err)
		defer rows.Close()
		for rows.Next() {
			var s string
			require.NoError(h.t, rows.Scan(&s))
			out = append(out, s)
		}
		return out
	}
	allUnread := []string{"tech-new", "news-unread", "news-unread-starred", "tech-unread", "loose-old"}

	// ts is "older than": items crawled at or before it are marked [GR][FR][MF]. Units by digit count
	// [K §3]: seconds (10 digits) [MF], microseconds (16 digits) [FR][MF], milliseconds and
	// nanoseconds tolerated.
	cut := func(l confLib) int64 { return l.ids["news-unread-starred"] } // marks news-unread-starred and older
	for name, ts := range map[string]func(us int64) string{
		"seconds":      func(us int64) string { return strconv.FormatInt(us/1_000_000, 10) },
		"milliseconds": func(us int64) string { return strconv.FormatInt(us/1_000, 10) },
		"microseconds": func(us int64) string { return strconv.FormatInt(us, 10) },
		"nanoseconds":  func(us int64) string { return strconv.FormatInt(us*1_000, 10) },
	} {
		t.Run("ts in "+name, func(t *testing.T) {
			h := newHarness(t)
			l := seedConf(h)
			c := newConf(t, h)
			c.write("mark-all-as-read", q1("s", stateReadingList)+"&ts="+ts(cut(l)))
			require.Equal(t, []string{"tech-new", "news-unread"}, unreadOf(h))
		})
	}

	scopes := map[string]struct {
		s    func(l confLib) string
		left []string
	}{
		"reading list, no ts: everything": {func(confLib) string { return stateReadingList }, nil},
		"no s: the reading list":          {func(confLib) string { return "" }, nil},
		"feed/<id>":                       {func(l confLib) string { return feedID(l.tech) }, []string{"news-unread", "news-unread-starred", "loose-old"}},
		"feed/<url>":                      {func(confLib) string { return "feed/https://tech.example/feed.xml" }, []string{"news-unread", "news-unread-starred", "loose-old"}},
		"label":                           {func(confLib) string { return "user/-/label/News & Politics" }, []string{"tech-new", "tech-unread", "loose-old"}},
		"label, user id form":             {func(confLib) string { return "user/7/label/Tech" }, []string{"news-unread", "news-unread-starred", "loose-old"}},
		"starred":                         {func(confLib) string { return stateStarred }, []string{"tech-new", "news-unread", "tech-unread", "loose-old"}},
		// [FR] marks every unread item on the unread stream (Miniflux does nothing there); [K §6.8] the
		// kept-unread stream is the same set of items, so it does the same.
		"unread state":      {func(confLib) string { return "user/-/state/com.google/unread" }, nil},
		"kept-unread state": {func(confLib) string { return "user/-/state/com.google/kept-unread" }, nil},
		// The read stream holds only read items: nothing to do.
		"read state":     {func(confLib) string { return stateRead }, allUnread},
		"unknown label":  {func(confLib) string { return "user/-/label/Missing" }, allUnread},
		"unknown stream": {func(confLib) string { return "nonsense" }, allUnread},
	}
	for name, sc := range scopes {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			l := seedConf(h)
			c := newConf(t, h)
			form := "x=1"
			if s := sc.s(l); s != "" {
				form = q1("s", s)
			}
			c.write("mark-all-as-read", form)
			require.Equal(t, sc.left, unreadOf(h))
		})
	}

	t.Run("an item that arrives after ts stays unread", func(t *testing.T) {
		// The point of ts [GR]: the client marks what it has shown, not what came in since.
		h := newHarness(t)
		l := seedConf(h)
		c := newConf(t, h)
		ts := strconv.FormatInt(l.ids["tech-new"], 10)
		later := h.addItem(l.tech, itemSeed{ID: l.ids["tech-new"] + 5_000_000, Title: "arrived later"})
		c.write("mark-all-as-read", q1("s", feedID(l.tech))+"&ts="+ts)
		require.False(t, isRead(h, later))
		require.True(t, isRead(h, l.ids["tech-new"]))
	})
}
