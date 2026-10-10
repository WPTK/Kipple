package greader

import (
	"bytes"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// nestedLibrary builds Tech (one feed) > Apple (one feed) > Mac (one feed), and Work (no feed) >
// Clients (one feed), plus an empty top-level folder. It returns the feed ids by folder path.
func nestedLibrary(h *harness) map[string]int64 {
	h.t.Helper()
	ctx := h.t.Context()
	mk := func(name string, parent int64) int64 {
		f, err := h.db.CreateFolder(ctx, name, parent, -1)
		require.NoError(h.t, err)
		return f.ID
	}
	tech := mk("Tech", 0)
	apple := mk("Apple", tech)
	mac := mk("Mac", apple)
	work := mk("Work", 0)
	clients := mk("Clients", work)
	mk("Empty", 0)
	feeds := map[string]int64{}
	for path, folder := range map[string]int64{"Tech": tech, "Tech/Apple": apple, "Tech/Apple/Mac": mac, "Work/Clients": clients} {
		id := h.addFeed("https://"+strings.ToLower(strings.ReplaceAll(path, "/", "-"))+".example/feed", path+" feed", "")
		require.NoError(h.t, execSQL(h, "UPDATE feeds SET folder_id = ? WHERE id = ?", folder, id))
		feeds[path] = id
	}
	return feeds
}

// Each nested folder is one flat label named by its full path; every feed has exactly one category,
// its own folder's; a pure container (Work: subfolders, no feed) is not a tag; an empty leaf is.
func TestNestedFoldersAsPathLabels(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	feeds := nestedLibrary(h)

	subs := jsonBody(t, h.get(rd+"subscription/list?output=json"))["subscriptions"].([]any)
	cats := map[string]string{}
	for _, s := range subs {
		m := s.(map[string]any)
		c := m["categories"].([]any)
		require.Len(t, c, 1, "exactly one category per feed")
		cat := c[0].(map[string]any)
		require.Equal(t, "user/-/label/"+cat["label"].(string), cat["id"])
		cats[m["id"].(string)] = cat["label"].(string)
	}
	for path, id := range feeds {
		require.Equal(t, path, cats[feedID(id)])
	}

	var tags []string
	for _, tg := range jsonBody(t, h.get(rd+"tag/list?output=json"))["tags"].([]any) {
		tags = append(tags, tg.(map[string]any)["id"].(string))
	}
	require.Equal(t, []string{"user/-/state/com.google/starred", "user/-/state/com.google/reading-list",
		"user/-/label/Uncategorized", "user/-/label/Tech", "user/-/label/Tech/Apple", "user/-/label/Tech/Apple/Mac",
		"user/-/label/Work/Clients", "user/-/label/Empty"}, tags)
}

// A label's stream, unread count and mark-all-as-read cover its own feeds only, never its subfolders'.
func TestNestedLabelScopeIsOwnFeeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	feeds := nestedLibrary(h)
	var techItem, appleItem int64
	for path, id := range feeds {
		item := h.addItem(id, itemSeed{Title: path})
		switch path {
		case "Tech":
			techItem = item
		case "Tech/Apple":
			appleItem = item
		}
	}

	ids := jsonBody(t, h.get(rd+"stream/items/ids?output=json&n=100&s=user/-/label/Tech"))["itemRefs"].([]any)
	require.Len(t, ids, 1)
	require.Equal(t, strconv.FormatInt(techItem, 10), ids[0].(map[string]any)["id"])
	ids = jsonBody(t, h.get(rd+"stream/items/ids?output=json&n=100&s="+url.QueryEscape("user/-/label/Tech/Apple")))["itemRefs"].([]any)
	require.Len(t, ids, 1)
	require.Equal(t, strconv.FormatInt(appleItem, 10), ids[0].(map[string]any)["id"])

	counts := map[string]float64{}
	for _, c := range jsonBody(t, h.get(rd+"unread-count?output=json"))["unreadcounts"].([]any) {
		m := c.(map[string]any)
		counts[m["id"].(string)] = m["count"].(float64)
	}
	require.Equal(t, float64(1), counts["user/-/label/Tech"])
	require.Equal(t, float64(1), counts["user/-/label/Tech/Apple"])
	_, ok := counts["user/-/label/Work"]
	require.False(t, ok, "a pure container has no count")

	w := h.post(rd+"mark-all-as-read", "T="+h.tok+"&s=user/-/label/Tech&ts="+strconv.FormatInt(time.Now().Add(time.Hour).UnixMicro(), 10))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, q[int](h, "SELECT read FROM items WHERE id = ?", techItem))
	require.Equal(t, 0, q[int](h, "SELECT read FROM items WHERE id = ?", appleItem), "the subfolder is not marked")
}

// Subscribing or filing into a path label creates the folders of the path; the item categories are paths.
func TestNestedLabelWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss")+
		"&a="+clientEnc("user/-/label/Work/Clients"))
	require.Equal(t, http.StatusOK, w.Code)
	clients := q[int64](h, "SELECT id FROM folder_paths WHERE path = 'Work/Clients'")
	work := q[int64](h, "SELECT id FROM folders WHERE name = 'Work' AND parent_id IS NULL")
	require.Equal(t, work, q[int64](h, "SELECT parent_id FROM folders WHERE id = ?", clients))
	feed := q[int64](h, "SELECT id FROM feeds WHERE folder_id = ?", clients)

	// ac=edit into a deeper path below an existing folder
	h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(feed)+"&a="+clientEnc("user/-/label/Work/Clients/Acme"))
	require.Equal(t, "Work/Clients/Acme", q[string](h, "SELECT fp.path FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id WHERE f.id = ?", feed))
	h.addItem(feed, itemSeed{Title: "x"})
	m := jsonBody(t, h.get(rd+"stream/contents/user/-/state/com.google/reading-list?output=json"))
	require.Contains(t, m["items"].([]any)[0].(map[string]any)["categories"], "user/-/label/Work/Clients/Acme")

	// A path past the depth cap is refused, logged and answered OK with nothing changed.
	deep := "user/-/label/1/2/3/4/5/6/7/8/9"
	before := q[int](h, "SELECT count(*) FROM folders")
	w = h.post(rd+"subscription/edit", "T="+h.tok+"&ac=edit&s="+feedID(feed)+"&a="+clientEnc(deep))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, before, q[int](h, "SELECT count(*) FROM folders"))
}

// disable-tag deletes the folder's subtree, moving every feed in it to Uncategorized.
func TestNestedDisableTagDeletesSubtree(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	feeds := nestedLibrary(h)
	w := h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/Tech")
	require.Equal(t, http.StatusOK, w.Code)
	require.Zero(t, q[int](h, "SELECT count(*) FROM folder_paths WHERE path LIKE 'Tech%'"))
	for _, p := range []string{"Tech", "Tech/Apple", "Tech/Apple/Mac"} {
		require.Equal(t, 1, q[int](h, "SELECT folder_id FROM feeds WHERE id = ?", feeds[p]), p)
	}
	require.Equal(t, "Work/Clients", q[string](h, "SELECT fp.path FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id WHERE f.id = ?", feeds["Work/Clients"]))
	// The default folder is never deleted.
	w = h.post(rd+"disable-tag", "T="+h.tok+"&s=user/-/label/Uncategorized")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM folders WHERE id = 1"))
}

// rename-tag moves and renames by path; a merge of a folder that has subfolders is refused with a
// WARN and an OK, and nothing changes.
func TestNestedRenameTag(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	h := newHarness(t, harnessOpts{logger: debugLogger(&logs)})
	feeds := nestedLibrary(h)
	path := func(feed int64) string {
		return q[string](h, "SELECT fp.path FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id WHERE f.id = ?", feed)
	}
	h.post(rd+"rename-tag", "T="+h.tok+"&s="+clientEnc("user/-/label/Tech/Apple")+"&dest="+clientEnc("user/-/label/Apple Stuff"))
	require.Equal(t, "Apple Stuff", path(feeds["Tech/Apple"]))
	require.Equal(t, "Apple Stuff/Mac", path(feeds["Tech/Apple/Mac"]))
	h.post(rd+"rename-tag", "T="+h.tok+"&s=user/-/label/Tech&dest=user/-/label/Gear")
	require.Equal(t, "Gear", path(feeds["Tech"]))

	w := h.post(rd+"rename-tag", "T="+h.tok+"&s="+clientEnc("user/-/label/Apple Stuff")+"&dest=user/-/label/Gear")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, logs.String(), "folder change refused")
	require.Equal(t, "Apple Stuff", path(feeds["Tech/Apple"]), "nothing changed")
	require.Equal(t, "Apple Stuff/Mac", path(feeds["Tech/Apple/Mac"]))
}

// A label the folder writer refuses (an empty level, more than 8 levels, below the default folder)
// never costs a subscription: each s= feed is subscribed into the default folder with a WARN, and the
// later s= values are still subscribed.
func TestSubscribeWithRefusedLabelUsesTheDefaultFolder(t *testing.T) {
	t.Parallel()
	for _, label := range []string{"user/-/label/News/", "user/-/label//News", "user/-/label/Uncategorized/X",
		"user/-/label/A//B", "user/-/label/1/2/3/4/5/6/7/8/9"} {
		t.Run(label, func(t *testing.T) {
			var logs bytes.Buffer
			h := newHarness(t, harnessOpts{logger: debugLogger(&logs)})
			before := q[int](h, "SELECT count(*) FROM folders")
			w := h.post(rd+"subscription/edit", "T="+h.tok+"&ac=subscribe&s=feed/"+url.QueryEscape("https://one.example/rss")+
				"&s=feed/"+url.QueryEscape("https://two.example/rss")+"&a="+clientEnc(label))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, 2, q[int](h, "SELECT count(*) FROM feeds WHERE folder_id = 1 AND host IN ('one.example', 'two.example')"))
			require.Equal(t, before, q[int](h, "SELECT count(*) FROM folders"), "no folder half-created")
			require.Contains(t, logs.String(), "folder label refused")
		})
	}
}
