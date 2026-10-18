package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

var seqNames = []string{"A", "a", "B", "A/B", "News", "Tech", "x y", "Ünï"}

// FuzzAPISequence runs random sequences of folder, feed and item requests through the real handlers against one
// database and checks after every step: no 5xx, no foreign-key violation, no folder cycle, every item still
// where the sequence put it (read and starred as the last request left them; only an item of a deleted feed
// may disappear, and a starred one moves to the archive feed), and the counts the app shows (per feed, per
// folder subtree and in total) equal what the tables hold. Refusals (4xx) are fine.
func FuzzAPISequence(f *testing.F) {
	f.Add([]byte{0, 0, 0, 3, 1, 0, 3, 2, 1, 6, 0, 0, 8, 1, 1, 7, 2, 0, 4, 1, 1, 5, 1, 0})
	f.Add([]byte{0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 0, 1, 3, 1, 1, 3, 2, 2, 9, 0, 0, 5, 0, 0, 2, 1, 0})
	f.Add([]byte{3, 1, 0, 3, 2, 1, 3, 3, 2, 8, 0, 0, 8, 1, 0, 8, 2, 0, 5, 0, 0, 5, 1, 1, 7, 0, 0, 7, 3, 0})
	f.Add([]byte{0, 0, 0, 1, 0, 1, 1, 1, 0, 1, 2, 0, 2, 0, 0, 2, 1, 0, 3, 0, 0, 4, 0, 1, 9, 1, 1})

	h := newHarness(f)
	c := h.login()
	f.Fuzz(func(t *testing.T, ops []byte) {
		h.t = t // one database serves every run, so the harness reports to the run in progress
		h.reset()
		runSeq(t, h, c, ops)
	})
}

func runSeq(t *testing.T, h *harness, c *http.Cookie, ops []byte) {
	{
		if len(ops) > 3*40 {
			ops = ops[:3*40]
		}

		m := &seqModel{h: h, t: t, c: c, read: map[int64]bool{}, star: map[int64]bool{}, feedOf: map[int64]int64{}}

		for i := 0; i+2 < len(ops); i += 3 {
			op, a, b := int(ops[i])%11, int(ops[i+1]), int(ops[i+2])
			m.step = fmt.Sprintf("step %d (op %d %d %d)", i/3, op, a, b)
			switch op {
			case 0: // create a folder, in folder a or at the top
				m.send("POST", "/api/folders", jsonStr(map[string]any{"name": seqNames[b%len(seqNames)], "parent_id": m.folderRef(a, true)}), func(out map[string]any) {
					if id, err := strconv.ParseInt(fmt.Sprint(out["id"]), 10, 64); err == nil {
						m.folders = append(m.folders, id)
					}
				})
			case 1: // move or rename folder a
				body := map[string]any{"parent_id": m.folderRef(b, true)}
				if b%3 == 0 {
					body = map[string]any{"name": seqNames[a%len(seqNames)]}
				}
				m.send("PATCH", "/api/folders/"+m.folderID(a), jsonStr(body), nil)
			case 2: // delete folder a
				m.send("DELETE", "/api/folders/"+m.folderID(a), "", nil)
			case 3: // a feed with items, in folder a
				folder := int64(1)
				if len(m.folders) > 0 && a%2 == 0 {
					folder = m.folders[a/2%len(m.folders)]
				}
				if !h.folderExists(folder) {
					folder = 1
				}
				m.nfeed++
				fid := h.addFeed(fmt.Sprintf("Feed %d", m.nfeed), folder)
				m.feeds = append(m.feeds, fid)
				for k := 0; k < 1+b%3; k++ {
					rd, st := b>>2&1 == 1, b>>3&1 == 1 && k == 0
					id := h.addItem(fid, seedItem{Read: rd, Starred: st})
					m.items = append(m.items, id)
					m.read[id], m.star[id], m.feedOf[id] = rd, st, fid
				}
			case 4: // move feed a to folder b
				m.send("PATCH", "/api/feeds/"+m.feedID(a), jsonStr(map[string]any{"folder_id": m.folderRef(b, false)}), nil)
			case 5: // delete feed a (b odd: its starred items too)
				fid := m.feedID(a)
				path := "/api/feeds/" + fid
				if b%2 == 1 {
					path += "?delete_starred=1"
				}
				if code := m.send("DELETE", path, "", nil); code/100 == 2 {
					n, _ := strconv.ParseInt(fid, 10, 64)
					m.removeFeed(n, b%2 == 1)
				}
			case 6: // mark items read or unread by id
				if id := m.itemRef(a); id != 0 {
					rd := b%2 == 0
					if code := m.send("POST", "/api/items/mark-read", jsonStr(map[string]any{"read": rd, "ids": []string{sid(id)}}), nil); code/100 == 2 {
						if _, ok := m.read[id]; ok {
							m.read[id] = rd
						}
					}
				}
			case 7: // mark a whole feed, folder tree or the library read
				scope := map[string]any{"all": true}
				switch b % 3 {
				case 0:
					scope = map[string]any{"feed_id": m.feedID(a)}
				case 1:
					scope = map[string]any{"folder_id": m.folderID(a)}
				}
				m.send("POST", "/api/items/mark-read", jsonStr(map[string]any{"read": true, "scope": scope, "reason": "bulk"}), nil)
				m.resync() // the scope's reach is the store's rule; the checks below hold either way
			case 8: // star or unstar an item
				if id := m.itemRef(a); id != 0 {
					st := b%2 == 0
					if code := m.send("PUT", "/api/items/"+sid(id)+"/star", jsonStr(map[string]any{"starred": st}), nil); code/100 == 2 {
						if _, ok := m.star[id]; ok {
							m.star[id] = st
						}
					}
				}
			case 9: // reorder folders and feeds
				m.send("POST", "/api/reorder", jsonStr(map[string]any{"folders": []string{m.folderID(a), m.folderID(b)}}), nil)
			case 10: // read the app's own views
				m.send("GET", "/api/bootstrap", "", nil)
				m.send("GET", "/api/items?view=all&limit=50", "", nil)
			}
			m.check()
		}
	}
}

type seqModel struct {
	h       *harness
	t       *testing.T
	c       *http.Cookie
	step    string
	folders []int64
	feeds   []int64
	items   []int64
	nfeed   int
	read    map[int64]bool
	star    map[int64]bool
	feedOf  map[int64]int64
}

func (m *seqModel) fail(format string, args ...any) {
	m.t.Helper()
	m.t.Fatalf(m.step+": "+format, args...)
}

// send makes a request; a 5xx or a non-JSON body that claims to be JSON fails the run.
func (m *seqModel) send(method, path, body string, ok func(map[string]any)) int {
	m.t.Helper()
	rec := m.h.do(method, path, body, withCookie(m.c))
	if rec.Code >= 500 {
		m.fail("%s %s %s answered %d: %s", method, path, body, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") != "" && rec.Header().Get("Content-Type")[:4] == "appl" {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			m.fail("%s %s: invalid JSON %q", method, path, rec.Body.String())
		}
	}
	if rec.Code/100 == 2 && ok != nil {
		ok(out)
	}
	return rec.Code
}

func pickID(l []int64, b int) int64 {
	if len(l) == 0 {
		return 99999
	}
	return l[b%len(l)]
}

func (m *seqModel) folderID(b int) string { return sid(pickID(append([]int64{1}, m.folders...), b)) }
func (m *seqModel) feedID(b int) string   { return sid(pickID(m.feeds, b)) }

// folderRef is a folder id, or null (top level when top, the default folder otherwise).
func (m *seqModel) folderRef(b int, top bool) any {
	if top && b%4 == 0 {
		return nil
	}
	return m.folderID(b)
}

func (m *seqModel) itemRef(b int) int64 {
	if len(m.items) == 0 {
		return 0
	}
	return m.items[b%len(m.items)]
}

func (m *seqModel) removeFeed(fid int64, withStarred bool) {
	for id, f := range m.feedOf {
		if f != fid {
			continue
		}
		if m.star[id] && !withStarred {
			continue // moved to the archive feed
		}
		delete(m.read, id)
		delete(m.star, id)
		delete(m.feedOf, id)
	}
}

// resync takes read state from the database for items a bulk request may have changed.
func (m *seqModel) resync() {
	for id := range m.read {
		var rd int
		if err := m.h.db.Reader().QueryRow("SELECT read FROM items WHERE id = ?", id).Scan(&rd); err == nil {
			m.read[id] = rd == 1
		}
	}
}

func (h *harness) folderExists(id int64) bool {
	var n int
	return h.db.Reader().QueryRow("SELECT count(*) FROM folders WHERE id = ?", id).Scan(&n) == nil && n == 1
}

func (m *seqModel) check() {
	h := m.h
	m.t.Helper()
	q := h.db.Reader()

	rows, err := q.Query("PRAGMA foreign_key_check")
	if err != nil {
		m.fail("foreign_key_check: %v", err)
	}
	if rows.Next() {
		m.fail("a foreign key is violated")
	}
	rows.Close()

	// No cycle, and every folder reaches the top within the depth cap.
	prow, err := q.Query("SELECT id, parent_id FROM folders")
	if err != nil {
		m.fail("folders: %v", err)
	}
	parent := map[int64]int64{}
	for prow.Next() {
		var id int64
		var p *int64
		if err := prow.Scan(&id, &p); err != nil {
			m.fail("scan: %v", err)
		}
		if p != nil {
			parent[id] = *p
		}
	}
	prow.Close()
	for id := range parent {
		for d, cur := 0, id; ; d++ {
			p, ok := parent[cur]
			if !ok {
				break
			}
			if d > 50 {
				m.fail("folder %d is in a cycle", id)
			}
			cur = p
		}
	}
	var orphan int
	if err := q.QueryRow("SELECT count(*) FROM feeds f WHERE NOT EXISTS (SELECT 1 FROM folders WHERE id = f.folder_id)").Scan(&orphan); err != nil || orphan != 0 {
		m.fail("%d feeds sit in no folder (%v)", orphan, err)
	}

	// Items are where the sequence left them.
	for id, rd := range m.read {
		var gotRead, gotStar, feed int64
		err := q.QueryRow("SELECT read, starred, feed_id FROM items WHERE id = ?", id).Scan(&gotRead, &gotStar, &feed)
		if err != nil {
			m.fail("item %d is gone (feed %d, starred %v): %v", id, m.feedOf[id], m.star[id], err)
		}
		if (gotRead == 1) != rd || (gotStar == 1) != m.star[id] {
			m.fail("item %d: read=%d starred=%d, expected read=%v starred=%v", id, gotRead, gotStar, rd, m.star[id])
		}
	}

	// What the app shows equals the tables.
	rec := h.do("GET", "/api/bootstrap", "", withCookie(m.c))
	if rec.Code != 200 {
		m.fail("bootstrap answered %d: %s", rec.Code, rec.Body.String())
	}
	var boot struct {
		Counts struct{ Unread int64 }
		Feeds  []struct {
			ID       string
			FolderID string `json:"folder_id"`
			Unread   int64
		}
		Folders []struct {
			ID     string
			Unread int64
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		m.fail("bootstrap JSON: %v", err)
	}
	var total, archive int64
	for _, fe := range boot.Feeds {
		id, _ := strconv.ParseInt(fe.ID, 10, 64)
		var n int64
		if err := q.QueryRow("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", id).Scan(&n); err != nil {
			m.fail("count: %v", err)
		}
		if n != fe.Unread {
			m.fail("feed %d shows %d unread, the table has %d", id, fe.Unread, n)
		}
		total += fe.Unread
	}
	if err := q.QueryRow(`SELECT count(*) FROM items i JOIN feeds f ON f.id = i.feed_id WHERE f.disabled_reason = 'archive' AND i.read = 0`).Scan(&archive); err != nil {
		m.fail("archive count: %v", err)
	}
	if boot.Counts.Unread != total+archive {
		m.fail("total unread shows %d, feeds hold %d (+%d in the archive feed)", boot.Counts.Unread, total, archive)
	}
	for _, fo := range boot.Folders {
		id, _ := strconv.ParseInt(fo.ID, 10, 64)
		var want int64
		for _, fe := range boot.Feeds {
			fid, _ := strconv.ParseInt(fe.FolderID, 10, 64)
			for d, cur := 0, fid; d < 60; d++ {
				if cur == id {
					want += fe.Unread
					break
				}
				p, ok := parent[cur]
				if !ok {
					break
				}
				cur = p
			}
		}
		if fo.Unread != want {
			m.fail("folder %d shows %d unread, its feeds hold %d", id, fo.Unread, want)
		}
	}
}

// reset empties the library between runs (the folders, feeds and items a run made, the archive feed included),
// so one database serves every run.
func (h *harness) reset() {
	h.exec("DELETE FROM feeds")
	h.exec("DELETE FROM folders WHERE is_default = 0")
}
