package api

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/sched"
)

func TestDeleteFeedArchivesStarredByDefault(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("Doomed", 0)
	keep := h.addItem(f, seedItem{Starred: true})
	h.addItem(f, seedItem{Starred: true, Read: true})
	h.addItem(f, seedItem{})
	other := h.addFeed("Other", 0)
	h.addItem(other, seedItem{Starred: true})

	_, boot, _ := h.api(c, "GET", "/api/bootstrap", "")
	for _, fe := range boot["feeds"].([]any) {
		if fe.(map[string]any)["id"] == sid(f) {
			require.EqualValues(t, 2, fe.(map[string]any)["starred_count"], "the confirm dialog's number")
		}
	}
	sub := h.events()

	code, _, rec := h.api(c, "DELETE", "/api/feeds/"+sid(f), "")
	require.Equal(t, 204, code)
	require.Empty(t, rec.Body.String())
	require.Zero(t, h.count("SELECT count(*) FROM feeds WHERE id = ?", f))
	require.Equal(t, 2, h.count("SELECT count(*) FROM items i JOIN feeds a ON a.id = i.feed_id WHERE a.disabled_reason = 'archive' AND i.starred = 1 AND i.origin_title = 'Doomed'"))
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND uid LIKE ?", keep, fmt.Sprintf("a%d:%%", f)))
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE feed_id = ?", f), "the unstarred item went with the feed")
	require.Equal(t, []string{fmt.Sprintf(`{"feed_id":"%d"}`, f)}, feedChanged(t, sub))

	// the archive feed is not in bootstrap (an unsubscribed feed shows up nowhere), and is deleted for real
	var archID int64
	require.NoError(t, h.db.Reader().QueryRow("SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&archID))
	arch := sid(archID)
	_, boot, _ = h.api(c, "GET", "/api/bootstrap", "")
	for _, fe := range boot["feeds"].([]any) {
		require.NotEqual(t, arch, fe.(map[string]any)["id"])
		require.NotEqual(t, true, fe.(map[string]any)["is_archive"])
	}
	code, body409, _ := h.api(c, "DELETE", "/api/feeds/"+arch, "")
	require.Equal(t, 409, code)
	require.Equal(t, "archive_has_starred", body409["error"])
	require.Contains(t, body409["message"], "delete_starred=1")
	require.Equal(t, 2, h.count("SELECT count(*) FROM items i JOIN feeds a ON a.id = i.feed_id WHERE a.disabled_reason = 'archive' AND i.starred = 1 AND i.origin_title = 'Doomed'"), "refused delete keeps the items")
	code, _, _ = h.api(c, "DELETE", "/api/feeds/"+arch+"?delete_starred=1", "")
	require.Equal(t, 204, code)
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE starred = 1 AND feed_id != ?", other))
}

func TestDeleteFeedWithStarredDeleted(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("Doomed", 0)
	h.addItem(f, seedItem{Starred: true})
	code, _, _ := h.api(c, "DELETE", "/api/feeds/"+sid(f)+"?delete_starred=1", "")
	require.Equal(t, 204, code)
	require.Zero(t, h.count("SELECT count(*) FROM items"))
	require.Zero(t, h.count("SELECT count(*) FROM feeds WHERE disabled_reason = 'archive'"), "no archive feed is created")
	// delete_starred=0 is the default
	g := h.addFeed("Second", 0)
	h.addItem(g, seedItem{Starred: true})
	code, _, _ = h.api(c, "DELETE", "/api/feeds/"+sid(g)+"?delete_starred=0", "")
	require.Equal(t, 204, code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE starred = 1"))
}

func TestDeleteFeedErrors(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/feeds/999", 404}, {"/api/feeds/abc", 404}, {"/api/feeds/0", 404},
		{"/api/feeds/" + sid(f) + "?delete_starred=yes", 400}, {"/api/feeds/" + sid(f) + "?delete_starred=true", 400},
	} {
		code, _, _ := h.api(c, "DELETE", tc.path, "")
		require.Equal(t, tc.code, code, tc.path)
	}
	require.Equal(t, 1, h.count("SELECT count(*) FROM feeds WHERE id = ?", f))
}

func TestPurgeArchiveUnstarred(t *testing.T) {
	h := newHarness(t)
	c := h.login()

	code, body, _ := h.api(c, "POST", "/api/archive/purge-unstarred", "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, body["deleted"], "no archive feed is fine")

	f := h.addFeed("Gone", 0)
	h.addItem(f, seedItem{Starred: true})
	h.api(c, "DELETE", "/api/feeds/"+sid(f), "")
	var arch int64
	require.NoError(t, h.db.Reader().QueryRow("SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&arch))
	// a starred item that was later unstarred, plus one still starred
	h.addItem(arch, seedItem{})
	h.addItem(arch, seedItem{Read: true})
	live := h.addFeed("Live", 0)
	h.addItem(live, seedItem{}) // unstarred items of other feeds are never touched
	sub := h.events()

	code, body, _ = h.api(c, "POST", "/api/archive/purge-unstarred", "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 2, body["deleted"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE feed_id = ?", arch))
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE feed_id = ?", live))
	require.Equal(t, []string{fmt.Sprintf(`{"feed_id":"%d"}`, arch)}, feedChanged(t, sub))
	// the FTS trigger kept the index consistent
	require.Zero(t, h.count("SELECT count(*) FROM items_fts WHERE rowid NOT IN (SELECT id FROM items)"))
}

func TestMarkFetchRead(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	other := h.addFeed("B", 0)
	i1 := h.addItem(f, seedItem{})
	i2 := h.addItem(f, seedItem{})
	i3 := h.addItem(f, seedItem{})
	i4 := h.addItem(f, seedItem{})
	o1 := h.addItem(other, seedItem{})
	ins := func(feed int64, first, last any) int64 {
		h.exec(`INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, first_item_id, last_item_id) VALUES (?, 'scheduled', 1, 1, 'ok', ?, ?)`, feed, first, last)
		var id int64
		require.NoError(t, h.db.Reader().QueryRow("SELECT max(id) FROM fetch_log").Scan(&id))
		return id
	}
	log := ins(f, i2, i3)
	empty := ins(f, nil, nil)
	foreign := ins(other, o1, o1)
	sub := h.events()
	path := "/api/feeds/" + sid(f) + "/mark-fetch-read"

	code, body, _ := h.api(c, "POST", path, jsonStr(map[string]any{"fetch_log_id": sid(log)}))
	require.Equal(t, 200, code, body)
	require.EqualValues(t, 2, body["changed"])
	require.Equal(t, 2, h.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 1 AND id IN (?, ?)", f, i2, i3))
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE read = 1 AND id IN (?, ?, ?)", i1, i4, o1), "outside the range or the feed")
	require.Len(t, drain(sub, "items.state", 100*time.Millisecond), 1)
	require.Zero(t, h.count("SELECT count(*) FROM stats_events"), "a bulk mark-read is not a read")

	// replay is a no-op; numeric ids are accepted; a row without a range changes nothing
	_, body, _ = h.api(c, "POST", path, jsonStr(map[string]any{"fetch_log_id": log}))
	require.EqualValues(t, 0, body["changed"])
	code, body, _ = h.api(c, "POST", path, jsonStr(map[string]any{"fetch_log_id": sid(empty)}))
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, body["changed"])

	// a ledger row inside the range is marked read too
	h.exec("UPDATE items SET read = 0 WHERE id IN (?, ?)", i2, i3)
	h.trim(i3, true, 5)
	code, body, _ = h.api(c, "POST", path, jsonStr(map[string]any{"fetch_log_id": sid(log)}))
	require.Equal(t, 200, code)
	require.EqualValues(t, 1, body["changed"], "ledger rows are marked but, as in mark-read, only live items are reported")
	require.Equal(t, 1, h.count("SELECT count(*) FROM trimmed_items WHERE id = ? AND read = 1", i3))

	for _, tc := range []struct {
		name, path, body string
		code             int
	}{
		{"log of another feed", path, jsonStr(map[string]any{"fetch_log_id": sid(foreign)}), 404},
		{"unknown log", path, `{"fetch_log_id":"999"}`, 404},
		{"missing field", path, `{}`, 400},
		{"not a number", path, `{"fetch_log_id":"x"}`, 400},
		{"zero", path, `{"fetch_log_id":0}`, 400},
		{"unknown field", path, `{"fetch_log_id":"1","x":1}`, 400},
		{"bad json", path, `{`, 400},
		{"unknown feed", "/api/feeds/999/mark-fetch-read", jsonStr(map[string]any{"fetch_log_id": sid(log)}), 404},
		{"bad feed id", "/api/feeds/x/mark-fetch-read", `{"fetch_log_id":"1"}`, 404},
	} {
		code, _, _ := h.api(c, "POST", tc.path, tc.body)
		require.Equal(t, tc.code, code, tc.name)
	}
}

func TestResetTrimmedUnread(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	h.exec("UPDATE feeds SET trimmed_unread_count = 12, trimmed_unread_since = 5 WHERE id = ?", f)
	code, _, rec := h.api(c, "POST", "/api/feeds/"+sid(f)+"/trimmed-unread/reset", "")
	require.Equal(t, 204, code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, "0", h.feedRow(f, "trimmed_unread_count").String)
	require.Equal(t, fmt.Sprint(h.clk.Now().Unix()), h.feedRow(f, "trimmed_unread_since").String)
	for _, p := range []string{"/api/feeds/999/trimmed-unread/reset", "/api/feeds/x/trimmed-unread/reset"} {
		code, _, _ = h.api(c, "POST", p, "")
		require.Equal(t, 404, code, p)
	}
}

func TestRefreshFeed(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	path := "/api/feeds/" + sid(id) + "/refresh"

	h.sched.reply = sched.Reply{Outcome: "ok", NewItems: 4}
	code, body, _ := h.api(c, "POST", path, "")
	require.Equal(t, 200, code, body)
	require.Equal(t, map[string]any{"outcome": "ok", "new_items": float64(4), "error_class": nil, "error": nil}, body)
	code, _, _ = h.api(c, "POST", path+"?full=1", "")
	require.Equal(t, 200, code)
	code, _, _ = h.api(c, "POST", path+"?full=0", "")
	require.Equal(t, 200, code)
	require.Equal(t, []sched.Priority{{FeedID: id}, {FeedID: id, Full: true}, {FeedID: id}}, h.sched.submitted())

	h.sched.reply = sched.Reply{Outcome: "error", ErrClass: "timeout", ErrMsg: "slow"}
	_, body, _ = h.api(c, "POST", path, "")
	require.Equal(t, map[string]any{"outcome": "error", "new_items": float64(0), "error_class": "timeout", "error": "slow"}, body)

	// gone or disabled feeds must be re-enabled first
	h.sched.reply = sched.Reply{Err: sched.ErrDisabled}
	code, body, _ = h.api(c, "POST", path, "")
	require.Equal(t, 409, code)
	require.Equal(t, "disabled", body["error"])
	h.sched.reply = sched.Reply{Err: sched.ErrNotFound}
	code, _, _ = h.api(c, "POST", path, "")
	require.Equal(t, 404, code)
	h.sched.reply = sched.Reply{Err: sched.ErrStopped}
	code, _, _ = h.api(c, "POST", path, "")
	require.Equal(t, 503, code)
	h.sched.reply = sched.Reply{}
	h.sched.submitErr = sched.ErrStopped
	code, _, _ = h.api(c, "POST", path, "")
	require.Equal(t, 503, code)
	h.sched.submitErr = nil

	for _, tc := range []struct {
		path string
		code int
	}{
		{path + "?full=yes", 400}, {path + "?full=true", 400}, {"/api/feeds/999/refresh", 404}, {"/api/feeds/x/refresh", 404},
	} {
		code, _, _ := h.api(c, "POST", tc.path, "")
		require.Equal(t, tc.code, code, tc.path)
	}
	h.exec(`INSERT INTO feeds (folder_id, url, url_key, host, enabled, disabled_reason, retention) VALUES (1,'kipple:archive','kipple:archive','kipple.invalid',0,'archive',0)`)
	code, body, _ = h.api(c, "POST", "/api/feeds/"+sid(h.feedByURL2("kipple:archive"))+"/refresh", "")
	require.Equal(t, 409, code)
	require.Equal(t, "archive_feed", body["error"])
}

func TestRefreshFeedPendingWhenSlow(t *testing.T) {
	shortWaits(t)
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	h.sched.hang = true
	code, body, _ := h.api(c, "POST", "/api/feeds/"+sid(id)+"/refresh?full=1", "")
	require.Equal(t, 202, code)
	require.Equal(t, map[string]any{"pending": true}, body)
	require.Equal(t, []sched.Priority{{FeedID: id, Full: true}}, h.sched.submitted())
}

func TestRefreshFeedShutdownDuringWait(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	id := h.storeFeed("https://a.example/feed")
	h.sched.hang = true
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.sched.stop()
	}()
	code, _, _ := h.api(c, "POST", "/api/feeds/"+sid(id)+"/refresh", "")
	require.Equal(t, 503, code)
}

func TestFeedLog(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	other := h.addFeed("B", 0)
	now := h.clk.Now().Unix()
	day := int64(86400)
	ins := func(feed, started int64, outcome string, keep int, note string) {
		h.exec(`INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, http_status, error_class, error, new_items, first_item_id, last_item_id, final_url, note, keep)
			VALUES (?, 'scheduled', ?, 12, ?, 200, NULL, NULL, 2, 5, 9, 'https://f.example/x', ?, ?)`, feed, started, outcome, nullable(note), keep)
	}
	ins(f, now-20*day, "ok", 0, "")                          // too old
	ins(f, now-20*day, "ok", 1, "redirect_migrated: a -> b") // old but kept
	ins(f, now-13*day, "ok", 0, "")                          // inside 14 days
	ins(f, now-1*day, "error", 0, "")                        // recent
	ins(other, now-1*day, "ok", 0, "")                       // another feed's
	code, body, _ := h.api(c, "GET", "/api/health/feeds/"+sid(f)+"/log", "")
	require.Equal(t, 200, code)
	rows := body["log"].([]any)
	require.Len(t, rows, 3)
	notes := []any{}
	for _, r := range rows {
		notes = append(notes, r.(map[string]any)["note"])
	}
	first := rows[0].(map[string]any)
	require.Equal(t, "error", first["outcome"], "newest first")
	require.Equal(t, "scheduled", first["trigger"])
	require.EqualValues(t, 12, first["duration_ms"])
	require.EqualValues(t, 200, first["http_status"])
	require.Equal(t, "5", first["first_item_id"])
	require.Equal(t, "9", first["last_item_id"])
	require.Equal(t, "https://f.example/x", first["final_url"])
	require.Equal(t, false, first["keep"])
	require.Contains(t, notes, "redirect_migrated: a -> b")
	require.Equal(t, true, rows[2].(map[string]any)["keep"])

	// no rows at all is an empty array, not null
	g := h.addFeed("C", 0)
	_, body, _ = h.api(c, "GET", "/api/health/feeds/"+sid(g)+"/log", "")
	require.Equal(t, []any{}, body["log"])

	for _, p := range []string{"/api/health/feeds/999/log", "/api/health/feeds/x/log"} {
		code, _, _ = h.api(c, "GET", p, "")
		require.Equal(t, 404, code, p)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func TestFolders(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.events()

	code, body, _ := h.api(c, "POST", "/api/folders", `{"name":"  Comics "}`)
	require.Equal(t, 201, code, body)
	require.Equal(t, "Comics", body["name"])
	require.Equal(t, false, body["is_default"])
	comics := body["id"].(string)
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"News","position":40}`)
	require.Equal(t, 201, code)
	require.EqualValues(t, 40, body["position"])
	news := body["id"].(string)
	// default position goes last
	_, body, _ = h.api(c, "POST", "/api/folders", `{"name":"Zed"}`)
	require.EqualValues(t, 41, body["position"])

	// names are unique ignoring case
	code, body, _ = h.api(c, "POST", "/api/folders", `{"name":"comics"}`)
	require.Equal(t, 409, code)
	require.Equal(t, "folder_exists", body["error"])

	for _, tc := range []struct{ name, body string }{
		{"empty", `{}`}, {"blank", `{"name":"  "}`}, {"not string", `{"name":3}`}, {"too long", `{"name":"` + strings.Repeat("x", 101) + `"}`},
		{"control", `{"name":"a\nb"}`}, {"negative pos", `{"name":"Q","position":-1}`}, {"float pos", `{"name":"Q","position":1.5}`},
		{"unknown", `{"name":"Q","x":1}`}, {"bad json", `{`},
	} {
		code, _, _ := h.api(c, "POST", "/api/folders", tc.body)
		require.Equal(t, 400, code, tc.name)
	}

	// PATCH: rename, reposition, both
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+comics, `{"name":"Webcomics"}`)
	require.Equal(t, 200, code)
	require.Equal(t, "Webcomics", body["name"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+comics, `{"position":3}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 3, body["position"])
	require.Equal(t, "Webcomics", body["name"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+comics, `{"name":"Webcomics","position":9}`) // same name, own folder: fine
	require.Equal(t, 200, code, body)
	code, body, _ = h.api(c, "PATCH", "/api/folders/"+comics, `{"name":"NEWS"}`)
	require.Equal(t, 409, code, "the UI never merges folders")
	require.Equal(t, "folder_exists", body["error"])
	code, body, _ = h.api(c, "PATCH", "/api/folders/1", `{"name":"Inbox"}`)
	require.Equal(t, 200, code, "the default folder may be renamed")
	require.Equal(t, true, body["is_default"])
	for _, tc := range []struct{ path, body string }{
		{"/api/folders/999", `{"name":"x"}`}, {"/api/folders/x", `{"name":"x"}`},
	} {
		code, _, _ := h.api(c, "PATCH", tc.path, tc.body)
		require.Equal(t, 404, code)
	}
	for _, b := range []string{`{"name":""}`, `{"name":null}`, `{"position":null}`, `{"position":-2}`, `{"nope":1}`, `{`} {
		code, _, _ := h.api(c, "PATCH", "/api/folders/"+comics, b)
		require.Equal(t, 400, code, b)
	}

	// DELETE: feeds move to the default folder, feed.changed per feed
	cid := int64(0)
	fmt.Sscan(news, &cid)
	f1, f2 := h.addFeed("One", cid), h.addFeed("Two", cid)
	h.api(c, "PATCH", "/api/feeds/"+sid(f1), `{"folder_id":"`+news+`"}`) // ensure it is in there
	drain(sub, "", 20*time.Millisecond)
	code, _, rec := h.api(c, "DELETE", "/api/folders/"+news, "")
	require.Equal(t, 204, code)
	require.Empty(t, rec.Body.String())
	require.Equal(t, 2, h.count("SELECT count(*) FROM feeds WHERE folder_id = 1 AND id IN (?, ?)", f1, f2))
	require.Zero(t, h.count("SELECT count(*) FROM folders WHERE id = ?", cid))
	require.ElementsMatch(t, []string{fmt.Sprintf(`{"feed_id":"%d"}`, f1), fmt.Sprintf(`{"feed_id":"%d"}`, f2)}, feedChanged(t, sub))

	code, body, _ = h.api(c, "DELETE", "/api/folders/1", "")
	require.Equal(t, 409, code)
	require.Equal(t, "default_folder", body["error"])
	for _, p := range []string{"/api/folders/999", "/api/folders/x"} {
		code, _, _ = h.api(c, "DELETE", p, "")
		require.Equal(t, 404, code, p)
	}
}

// folderChanged returns the data of every folder.changed event so far.
func folderChanged(t *testing.T, sub *events.Sub) []string {
	t.Helper()
	var out []string
	for _, ev := range drain(sub, "folder.changed", 50*time.Millisecond) {
		out = append(out, string(ev.Data))
	}
	return out
}

func TestFolderChangedEvents(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	sub := h.events()

	_, body, _ := h.api(c, "POST", "/api/folders", `{"name":"Comics"}`)
	id := body["id"].(string)
	require.Equal(t, []string{`{"folder_id":"` + id + `"}`}, folderChanged(t, sub), "create")

	h.api(c, "PATCH", "/api/folders/"+id, `{"name":"Webcomics"}`)
	require.Equal(t, []string{`{"folder_id":"` + id + `"}`}, folderChanged(t, sub), "rename")

	h.api(c, "PATCH", "/api/folders/"+id, `{"name":"  "}`)
	require.Empty(t, folderChanged(t, sub), "a refused patch announces nothing")

	// moving a feed between folders
	fid := h.storeFeed("https://a.example/f")
	h.api(c, "PATCH", "/api/feeds/"+sid(fid), `{"folder_id":`+id+`}`)
	require.Equal(t, []string{`{}`}, folderChanged(t, sub), "feed move")
	h.api(c, "PATCH", "/api/feeds/"+sid(fid), `{"custom_title":"x"}`)
	require.Empty(t, folderChanged(t, sub), "a rename of a feed is not a folder change")

	// reorder
	code, _, _ := h.api(c, "POST", "/api/reorder", `{"folders":[`+id+`,1]}`)
	require.Equal(t, 200, code)
	require.Equal(t, []string{`{}`}, folderChanged(t, sub), "reorder")
	h.api(c, "POST", "/api/reorder", `{"folders":[`+id+`,1]}`)
	require.Empty(t, folderChanged(t, sub), "a no-op reorder announces nothing")

	// delete
	code, _, _ = h.api(c, "DELETE", "/api/folders/"+id, "")
	require.Equal(t, 204, code)
	require.Equal(t, []string{`{"folder_id":"` + id + `"}`}, folderChanged(t, sub), "delete")
	code, _, _ = h.api(c, "DELETE", "/api/folders/1", "")
	require.Equal(t, 409, code)
	require.Empty(t, folderChanged(t, sub), "the default folder cannot go")
}
