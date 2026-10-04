package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
)

// ---- helpers ----

// bulkItems inserts n unread items titled "spam k" (even k) or "ham k" (odd k) into feed, without content.
func (h *harness) bulkItems(feed int64, n int) {
	h.t.Helper()
	h.exec(fmt.Sprintf(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < %d)
		INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, url, title, author)
		SELECT %d + i*1000, %d, 0, 0, %d + i, %d + i, 0, 'bulk:' || i, 'c', 't', 'https://x.example/bulk/' || i,
		       CASE WHEN i %% 2 = 0 THEN 'spam ' || i ELSE 'ham ' || i END, 'Ann' FROM n`,
		n, baseID+5_000_000_000, feed, baseID/1_000_000, baseID/1_000_000))
}

func (h *harness) postFilter(c *http.Cookie, v any) (int, map[string]any) {
	h.t.Helper()
	code, out, _ := h.api(c, "POST", "/api/filters", jsonStr(v))
	return code, out
}

func (h *harness) mustFilter(c *http.Cookie, v any) string {
	h.t.Helper()
	code, out := h.postFilter(c, v)
	require.Equal(h.t, 201, code, "%v", out)
	return out["filter"].(map[string]any)["id"].(string)
}

// collect gathers events from sub until one of type stop arrives (or the timeout).
func collect(t *testing.T, sub *events.Sub, stop string, wait time.Duration) []events.Event {
	t.Helper()
	var out []events.Event
	deadline := time.After(wait)
	for {
		select {
		case ev := <-sub.C:
			out = append(out, ev)
			if ev.Type == stop {
				return out
			}
		case <-deadline:
			t.Fatalf("no %s event within %s (got %d events)", stop, wait, len(out))
		}
	}
}

func ofType(evs []events.Event, typ string) []map[string]any {
	var out []map[string]any
	for _, ev := range evs {
		if ev.Type != typ {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal(ev.Data, &m)
		out = append(out, m)
	}
	return out
}

// ---- auth and origin ----

func TestFilterRoutesRequireSessionAndOrigin(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	routes := []struct{ method, path string }{
		{"GET", "/api/filters"}, {"POST", "/api/filters"}, {"POST", "/api/filters/preview"},
		{"PATCH", "/api/filters/1"}, {"DELETE", "/api/filters/1"}, {"POST", "/api/filters/1/apply"},
	}
	for _, r := range routes {
		rec := h.do(r.method, r.path, "{}")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", r.method, r.path)
		if r.method == "GET" {
			continue
		}
		rec = h.do(r.method, r.path, "{}", withCookie(c), func(q *http.Request) { q.Header.Set("Sec-Fetch-Site", "cross-site") })
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s", r.method, r.path)
		rec = h.do(r.method, r.path, "{}", withCookie(c), func(q *http.Request) { q.Header.Del("X-Kipple-Client") })
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s", r.method, r.path)
	}
}

// ---- CRUD ----

func TestFilterCRUD(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	sub := h.hub.Subscribe(0)
	defer sub.Close()

	code, out, _ := h.api(c, "GET", "/api/filters", "")
	require.Equal(t, 200, code)
	require.Equal(t, []any{}, out["filters"])

	code, out = h.postFilter(c, map[string]any{"name": "No ads", "action": "mute", "terms": []string{"sponsored", "ad"}})
	require.Equal(t, 201, code, "%v", out)
	require.Nil(t, out["applied"])
	f := out["filter"].(map[string]any)
	id := f["id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, "No ads", f["name"])
	require.Equal(t, true, f["enabled"])
	require.Equal(t, "global", f["scope"])
	require.Nil(t, f["folder_id"])
	require.Nil(t, f["feed_id"])
	require.Equal(t, "text", f["kind"])
	require.Equal(t, []any{"sponsored", "ad"}, f["terms"])
	require.Equal(t, []any{"title"}, f["fields"])
	require.Equal(t, false, f["case_sensitive"])
	require.Equal(t, true, f["whole_word"])
	require.Equal(t, true, f["fold_diacritics"])
	require.Equal(t, false, f["invert"])
	require.Equal(t, "mute", f["action"])
	require.EqualValues(t, 0, f["hits"])
	require.Nil(t, f["last_hit_at"])
	require.EqualValues(t, h.clk.Now().Unix(), f["created_at"])
	require.EqualValues(t, 0, f["muted_items"])
	require.Len(t, ofType(drain(sub, "", 100*time.Millisecond), "filters.changed"), 1, "a create tells the other tabs")

	// PATCH is partial and never retroactive
	h.clk.Advance(time.Minute)
	code, out, _ = h.api(c, "PATCH", "/api/filters/"+id, jsonStr(map[string]any{"terms": []string{"promoted"}, "whole_word": false, "position": 3}))
	require.Equal(t, 200, code, "%v", out)
	f = out["filter"].(map[string]any)
	require.Equal(t, []any{"promoted"}, f["terms"])
	require.Equal(t, false, f["whole_word"])
	require.EqualValues(t, 3, f["position"])
	require.Equal(t, "No ads", f["name"], "untouched fields stay")
	require.EqualValues(t, h.clk.Now().Unix(), f["updated_at"])
	require.Len(t, ofType(drain(sub, "filters.changed", 100*time.Millisecond), "filters.changed"), 1)

	// scope changes drop the target that no longer fits
	code, out, _ = h.api(c, "PATCH", "/api/filters/"+id, jsonStr(map[string]any{"scope": "feed", "feed_id": sid(feed)}))
	require.Equal(t, 200, code, "%v", out)
	require.Equal(t, sid(feed), out["filter"].(map[string]any)["feed_id"])
	code, out, _ = h.api(c, "PATCH", "/api/filters/"+id, jsonStr(map[string]any{"scope": "global"}))
	require.Equal(t, 200, code, "%v", out)
	require.Nil(t, out["filter"].(map[string]any)["feed_id"])

	// numbers are accepted for ids as well
	code, out, _ = h.api(c, "PATCH", "/api/filters/"+id, jsonStr(map[string]any{"scope": "feed", "feed_id": feed}))
	require.Equal(t, 200, code, "%v", out)

	// list shape, with the muted count
	h.exec("UPDATE items SET muted_by = 0 WHERE 0")
	code, out, _ = h.api(c, "GET", "/api/filters", "")
	require.Equal(t, 200, code)
	require.Len(t, out["filters"], 1)

	code, out, _ = h.api(c, "DELETE", "/api/filters/"+id, "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, out["changed"])
	require.Equal(t, 0, h.count("SELECT count(*) FROM filters"))
	for _, r := range []struct{ method, path string }{
		{"PATCH", "/api/filters/" + id}, {"DELETE", "/api/filters/" + id}, {"POST", "/api/filters/" + id + "/apply"},
		{"PATCH", "/api/filters/x"}, {"DELETE", "/api/filters/0"},
	} {
		code, out, _ = h.api(c, r.method, r.path, "{}")
		require.Equal(t, 404, code, "%s %s", r.method, r.path)
		require.Equal(t, "not_found", out["error"])
	}
}

func TestFilterValidationErrorsAre400WithFieldAndMessage(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	ok := func() map[string]any {
		return map[string]any{"action": "mute", "terms": []string{"x"}}
	}
	with := func(k string, v any) map[string]any { m := ok(); m[k] = v; return m }
	without := func(k string) map[string]any { m := ok(); delete(m, k); return m }
	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no action", without("action"), "action"},
		{"bad action", with("action", "delete"), "action"},
		{"no terms", without("terms"), "terms"},
		{"empty terms", with("terms", []string{}), "terms"},
		{"blank term", with("terms", []string{"  "}), "terms[0]"},
		{"long term", with("terms", []string{string(make([]byte, 0)) + fmt.Sprintf("%0101d", 1)}), "terms[0]"},
		{"bad scope", with("scope", "galaxy"), "scope"},
		{"folder scope without folder", with("scope", "folder"), "scope"},
		{"unknown folder", map[string]any{"action": "mute", "terms": []string{"x"}, "scope": "folder", "folder_id": "999"}, "folder_id"},
		{"unknown feed", map[string]any{"action": "mute", "terms": []string{"x"}, "scope": "feed", "feed_id": "999"}, "feed_id"},
		{"non-id feed", map[string]any{"action": "mute", "terms": []string{"x"}, "scope": "feed", "feed_id": "abc"}, "feed_id"},
		{"global with a feed", map[string]any{"action": "mute", "terms": []string{"x"}, "feed_id": sid(feed)}, "scope"},
		{"bad kind", with("kind", "glob"), "kind"},
		{"bad field", with("fields", []string{"title", "nope"}), "fields[1]"},
		{"bad regex", map[string]any{"action": "mute", "kind": "regex", "terms": []string{"a("}}, "terms[0]"},
		{"empty-matching regex", map[string]any{"action": "mute", "kind": "regex", "terms": []string{"a*"}}, "terms[0]"},
		{"regex highlight", map[string]any{"action": "highlight", "kind": "regex", "terms": []string{"a+"}}, "action"},
		{"name too long", with("name", string(make([]byte, 0))+fmt.Sprintf("%0250d", 1)), "name"},
	} {
		code, out := h.postFilter(c, tc.body)
		require.Equal(t, 400, code, tc.name)
		require.Equal(t, "bad_filter", out["error"], tc.name)
		require.Equal(t, tc.field, out["field"], "%s: %v", tc.name, out)
		require.NotEmpty(t, out["message"], tc.name)
	}
	require.Equal(t, 0, h.count("SELECT count(*) FROM filters"), "no failed create stored anything")

	// malformed bodies are plain bad_request
	code, out, _ := h.api(c, "POST", "/api/filters", "{nope")
	require.Equal(t, 400, code)
	require.Equal(t, "bad_request", out["error"])

	// a failed PATCH leaves the row alone
	id := h.mustFilter(c, ok())
	code, out, _ = h.api(c, "PATCH", "/api/filters/"+id, jsonStr(map[string]any{"terms": []string{}}))
	require.Equal(t, 400, code)
	require.Equal(t, "terms", out["field"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM filters WHERE terms = '[\"x\"]'"))

	// the set-wide caps: 200 filters
	for i := 0; i < 199; i++ {
		h.exec(`INSERT INTO filters (scope, kind, terms, action) VALUES ('global','text','["t"]','highlight')`)
	}
	code, out = h.postFilter(c, ok())
	require.Equal(t, 400, code)
	require.Equal(t, "bad_filter", out["error"])
	require.Equal(t, "rules", out["field"])
}

// ---- preview ----

func TestFilterPreview(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 100)
	h.exec("UPDATE items SET read = 1 WHERE title = 'spam 2'")
	h.exec("UPDATE items SET starred = 1 WHERE title = 'spam 4'")

	code, out, _ := h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{"spam"}}}))
	require.Equal(t, 200, code, "%v", out)
	require.EqualValues(t, 48, out["matches"])
	require.EqualValues(t, 98, out["scanned"])
	require.Equal(t, false, out["truncated"])
	sample := out["sample"].([]any)
	require.Len(t, sample, 20)
	first := sample[0].(map[string]any)
	require.Contains(t, first["title"], "spam")
	require.Contains(t, first, "muted_by")
	require.Equal(t, []any{}, out["warnings"])

	code, out, _ = h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{"spam"}}, "include_read": true}))
	require.Equal(t, 200, code)
	require.EqualValues(t, 49, out["matches"])
	require.EqualValues(t, 100, out["scanned"])

	// a saved rule by id, overlaid with an edit in progress
	id := h.mustFilter(c, map[string]any{"action": "mark_read", "terms": []string{"ham"}})
	code, out, _ = h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"id": id}))
	require.Equal(t, 200, code)
	require.EqualValues(t, 50, out["matches"])
	code, out, _ = h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"id": id, "filter": map[string]any{"terms": []string{"ham 1"}}}))
	require.Equal(t, 200, code)
	require.EqualValues(t, 1, out["matches"], "ham 1 only (whole word: 'ham 1' is a phrase)")

	// category rules warn that old items carry no categories
	code, out, _ = h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{"x"}, "fields": []string{"category"}}}))
	require.Equal(t, 200, code)
	w := out["warnings"].([]any)
	require.Len(t, w, 1)
	require.Equal(t, "category_new_items_only", w[0].(map[string]any)["code"])

	// nothing was written by any of it
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Equal(t, 0, h.count("SELECT sum(hits) FROM filters"))

	// validation and lookups
	for _, tc := range []struct {
		name string
		body any
		code int
		err  string
	}{
		{"empty", map[string]any{}, 400, "bad_filter"},
		{"bad rule", map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{}}}, 400, "bad_filter"},
		{"no action", map[string]any{"filter": map[string]any{"terms": []string{"x"}}}, 400, "bad_filter"},
		{"bad id", map[string]any{"id": "zz"}, 400, "bad_filter"},
		{"unknown id", map[string]any{"id": "424242"}, 404, "not_found"},
		{"unknown feed", map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{"x"}, "scope": "feed", "feed_id": "999"}}, 400, "bad_filter"},
	} {
		code, out, _ := h.api(c, "POST", "/api/filters/preview", jsonStr(tc.body))
		require.Equal(t, tc.code, code, tc.name)
		require.Equal(t, tc.err, out["error"], tc.name)
	}
}

func TestFilterPreviewBudget(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.PreviewBudget = time.Nanosecond })
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 2500)
	code, out, _ := h.api(c, "POST", "/api/filters/preview", jsonStr(map[string]any{"filter": map[string]any{"action": "mute", "terms": []string{"spam"}}}))
	require.Equal(t, 200, code)
	require.Equal(t, true, out["truncated"])
	require.Less(t, out["scanned"].(float64), float64(2500))
}

// ---- apply (a run) ----

func TestFilterApplyRunBatchesAndEvents(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.CountsInterval = 20 * time.Millisecond })
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 1300)
	h.exec("UPDATE items SET starred = 1 WHERE title = 'spam 4'")
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
	sub := h.hub.Subscribe(0)
	defer sub.Close()

	code, out, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
	require.Equal(t, http.StatusAccepted, code, "%v", out)
	require.Equal(t, "filter_apply", out["kind"])
	require.Equal(t, id, out["filter_id"])
	require.NotEmpty(t, out["id"])
	require.EqualValues(t, 1299, out["total"])

	evs := collect(t, sub, "run.done", 10*time.Second)
	start := ofType(evs, "run.start")
	require.Len(t, start, 1)
	require.Equal(t, out["id"], start[0]["run_id"])
	require.Equal(t, "filter_apply", start[0]["kind"])
	done := ofType(evs, "run.done")[0]
	require.Equal(t, out["id"], done["run_id"])
	require.EqualValues(t, 649, done["changed"])
	require.EqualValues(t, 0, done["errors"])
	require.EqualValues(t, 1299, done["scanned"])

	states := ofType(evs, "items.state")
	total := 0
	for _, s := range states {
		ids := s["ids"].([]any)
		require.LessOrEqual(t, len(ids), 500, "one write batch per event")
		require.Equal(t, true, s["read"])
		require.Equal(t, true, s["muted"])
		total += len(ids)
	}
	require.Equal(t, 649, total)
	require.Len(t, states, 2)
	require.NotEmpty(t, ofType(evs, "run.progress"), "the first page reports progress at once")

	// filters.changed and counts follow the run
	tail := drain(sub, "", 500*time.Millisecond)
	require.NotEmpty(t, ofType(tail, "filters.changed"))
	counts := ofType(append(evs, tail...), "counts")
	require.NotEmpty(t, counts)
	last := counts[len(counts)-1]
	require.EqualValues(t, 649, last["muted"], "the counts event carries the muted total")
	require.EqualValues(t, 1300-649, last["unread_total"])

	require.Equal(t, 649, h.count("SELECT count(*) FROM items WHERE muted_by = ?", id))
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND read = 0"))
	_, out, _ = h.api(c, "GET", "/api/filters", "")
	require.EqualValues(t, 649, out["filters"].([]any)[0].(map[string]any)["muted_items"])
	require.EqualValues(t, 649, out["filters"].([]any)[0].(map[string]any)["hits"])
}

func TestFilterApplyRejectsAndSingleFlight(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 10)
	hl := h.mustFilter(c, map[string]any{"action": "highlight", "terms": []string{"spam"}})
	off := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "enabled": false})
	on := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})

	code, out, _ := h.api(c, "POST", "/api/filters/"+hl+"/apply", "")
	require.Equal(t, 400, code)
	require.Equal(t, "action", out["field"])
	code, out, _ = h.api(c, "POST", "/api/filters/"+off+"/apply", "")
	require.Equal(t, 400, code)
	require.Equal(t, "enabled", out["field"])

	// while a run is active a second start answers 409 and the bootstrap lists the run
	h.srv.apply.mu.Lock()
	h.srv.apply.run = &applyRun{ID: 77, Kind: runKindFilterApply, FilterID: 1, Done: 5, Total: 10}
	h.srv.apply.mu.Unlock()
	code, out, _ = h.api(c, "POST", "/api/filters/"+on+"/apply", "")
	require.Equal(t, 409, code)
	require.Equal(t, "busy", out["error"])
	code, out, _ = h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, 200, code)
	runs := out["runs"].([]any)
	require.Len(t, runs, 2, "the scheduler's run and the apply run")
	ap := runs[1].(map[string]any)
	require.Equal(t, "77", ap["id"])
	require.Equal(t, "filter_apply", ap["kind"])
	require.EqualValues(t, 5, ap["done"])
	require.EqualValues(t, 10, ap["total"])
	h.srv.apply.mu.Lock()
	h.srv.apply.run = nil
	h.srv.apply.mu.Unlock()

	// include_read comes from the body
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	h.exec("UPDATE items SET read = 1 WHERE title = 'spam 2'")
	code, out, _ = h.api(c, "POST", "/api/filters/"+on+"/apply", `{"include_read":true}`)
	require.Equal(t, http.StatusAccepted, code)
	require.EqualValues(t, 10, out["total"])
	done := ofType(collect(t, sub, "run.done", 5*time.Second), "run.done")[0]
	require.EqualValues(t, 5, done["changed"])
}

func TestCreateWithApplyExisting(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 20)
	sub := h.hub.Subscribe(0)
	defer sub.Close()

	code, out := h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{"include_read": false}})
	require.Equal(t, 201, code, "%v", out)
	applied := out["applied"].(map[string]any)
	require.Equal(t, "filter_apply", applied["kind"])
	require.EqualValues(t, 20, applied["total"])
	done := ofType(collect(t, sub, "run.done", 5*time.Second), "run.done")[0]
	require.EqualValues(t, 10, done["changed"])

	// nothing to apply for a highlight or a disabled rule: refused before anything is created
	for _, body := range []map[string]any{
		{"action": "highlight", "terms": []string{"x"}, "apply_existing": map[string]any{}},
		{"action": "mute", "terms": []string{"x"}, "enabled": false, "apply_existing": map[string]any{}},
	} {
		code, out = h.postFilter(c, body)
		require.Equal(t, 400, code)
		require.Equal(t, "apply_existing", out["field"])
	}
	require.Equal(t, 1, h.count("SELECT count(*) FROM filters"))
}

// ---- delete with un-muting ----

func TestFilterDeleteUnmute(t *testing.T) {
	for _, tc := range []struct {
		query        string
		wantChanged  int
		wantUnread   int // spam items unread afterwards
		wantOrphaned int // spam items still muted_by the deleted id
		read         any // "read" field of items.state, nil when absent
	}{
		{"", 700, 0, 0, nil},
		{"?unmute=read", 700, 0, 0, nil},
		{"?unmute=unread", 700, 700, 0, false},
		{"?unmute=1", 700, 0, 0, nil},    // the alias means the safe mode: unread must be spelled out
		{"?unmute=true", 700, 0, 0, nil}, //
		{"?unmute=keep", 0, 0, 700, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			h := newHarness(t, func(o *Options) { o.CountsInterval = 20 * time.Millisecond })
			c := h.login()
			feed := h.addFeed("A", 0)
			h.bulkItems(feed, 1400)
			id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
			sub := h.hub.Subscribe(0)
			defer sub.Close()
			code, _, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
			require.Equal(t, http.StatusAccepted, code)
			collect(t, sub, "run.done", 10*time.Second)
			drain(sub, "", 300*time.Millisecond)

			code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+tc.query, "")
			require.Equal(t, 200, code, "%v", out)
			require.EqualValues(t, tc.wantChanged, out["changed"])
			require.Equal(t, 0, h.count("SELECT count(*) FROM filters"))
			require.Equal(t, tc.wantUnread, h.count("SELECT count(*) FROM items WHERE title LIKE 'spam%' AND read = 0"))
			require.Equal(t, tc.wantOrphaned, h.count("SELECT count(*) FROM items WHERE muted_by = ?", id))
			require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL AND read = 0"))

			evs := drain(sub, "", 400*time.Millisecond)
			states := ofType(evs, "items.state")
			total, unreadEvents := 0, 0
			for _, s := range states {
				if s["muted"] != nil { // the un-mute batch; a separate event marks the items that went back to unread
					total += len(s["ids"].([]any))
					require.Equal(t, false, s["muted"])
					require.Nil(t, s["read"])
				} else {
					unreadEvents += len(s["ids"].([]any))
					require.Equal(t, tc.read, s["read"])
				}
			}
			require.Equal(t, tc.wantChanged, total)
			if tc.read == false {
				require.Equal(t, tc.wantUnread, unreadEvents)
			} else {
				require.Zero(t, unreadEvents)
			}
			require.NotEmpty(t, ofType(evs, "filters.changed"))
			if tc.wantChanged > 0 {
				require.NotEmpty(t, ofType(evs, "counts"))
			}
		})
	}
	h := newHarness(t)
	c := h.login()
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"x"}})
	code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=maybe", "")
	require.Equal(t, 400, code)
	require.Equal(t, "unmute", out["field"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM filters"))
}

// ---- the Muted view, counts, search, mark-read ----

func TestMutedViewAndCounts(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	other := h.addFeed("B", 0)
	id := h.mustFilter(c, map[string]any{"name": "Spam filter", "action": "mute", "terms": []string{"spam"}})
	var muted []int64
	for i := 0; i < 5; i++ {
		it := h.addItem(feed, seedItem{Title: fmt.Sprintf("spam %d", i), SortAt: baseID/1_000_000 + int64(i), Text: "zebra story"})
		h.exec("UPDATE items SET read = 1, muted_by = ? WHERE id = ?", id, it)
		muted = append(muted, it)
	}
	orphan := h.addItem(other, seedItem{Title: "orphaned", SortAt: baseID/1_000_000 + 10, Text: "zebra story"})
	h.exec("UPDATE items SET read = 1, muted_by = 9999 WHERE id = ?", orphan)
	plain := h.addItem(feed, seedItem{Title: "plain", Text: "zebra story"})
	readOnly := h.addItem(feed, seedItem{Title: "read only", Read: true, Text: "zebra story"})

	// the Muted view: newest first, paged, with the muting rule
	code, out, _ := h.api(c, "GET", "/api/items?view=muted&limit=4", "")
	require.Equal(t, 200, code, "%v", out)
	items := out["items"].([]any)
	require.Len(t, items, 4)
	require.NotNil(t, out["next_cursor"])
	first := items[0].(map[string]any)
	require.Equal(t, sid(orphan), first["id"])
	require.Equal(t, "9999", first["muted_by"])
	require.Nil(t, first["muted_by_name"], "a deleted filter has no name")
	second := items[1].(map[string]any)
	require.Equal(t, id, second["muted_by"])
	require.Equal(t, "Spam filter", second["muted_by_name"])
	require.Equal(t, true, second["read"])
	code, page2, _ := h.api(c, "GET", "/api/items?view=muted&limit=4&cursor="+out["next_cursor"].(string), "")
	require.Equal(t, 200, code)
	require.Len(t, page2["items"], 2)
	require.Nil(t, page2["next_cursor"])

	// feed and folder narrowing
	_, out, _ = h.api(c, "GET", "/api/items?view=muted&feed="+sid(other), "")
	require.Equal(t, []string{sid(orphan)}, itemIDs(t, out))

	// All and Unread hide muted items; Starred and ids lookups are unaffected
	_, out, _ = h.api(c, "GET", "/api/items?view=all", "")
	require.ElementsMatch(t, []string{sid(plain), sid(readOnly)}, itemIDs(t, out))
	require.Nil(t, out["items"].([]any)[0].(map[string]any)["muted_by"])
	_, out, _ = h.api(c, "GET", "/api/items?view=unread", "")
	require.Equal(t, []string{sid(plain)}, itemIDs(t, out))
	_, out, _ = h.api(c, "GET", "/api/items?ids="+sid(muted[0]), "")
	require.Equal(t, []string{sid(muted[0])}, itemIDs(t, out), "an explicit id lookup still finds muted items")

	// search skips muted items unless view=muted
	_, out, _ = h.api(c, "GET", "/api/items?q=zebra", "")
	require.ElementsMatch(t, []string{sid(plain), sid(readOnly)}, itemIDs(t, out))
	_, out, _ = h.api(c, "GET", "/api/items?q=zebra&view=muted", "")
	require.Len(t, out["items"], 6)
	require.NotNil(t, out["items"].([]any)[0].(map[string]any)["muted_by"])

	// the detail carries the muted fields
	_, out, _ = h.api(c, "GET", "/api/items/"+sid(muted[0]), "")
	require.Equal(t, id, out["muted_by"])
	require.Equal(t, "Spam filter", out["muted_by_name"])

	// bootstrap counts
	_, out, _ = h.api(c, "GET", "/api/bootstrap", "")
	counts := out["counts"].(map[string]any)
	require.EqualValues(t, 6, counts["muted"])
	require.EqualValues(t, 1, counts["unread"], "muted items are read, so unread counts do not change")

	// mark-read on the muted view is accepted and changes nothing (they are read already)
	code, out, _ = h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"scope": map[string]any{"all": true, "view": "muted"}, "read": true}))
	require.Equal(t, 200, code, "%v", out)
	require.EqualValues(t, 0, out["count"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE read = 0"))
	code, _, _ = h.api(c, "GET", "/api/items?view=nope", "")
	require.Equal(t, 400, code)
}

func TestUnmuteThroughWebActions(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
	a := h.addItem(feed, seedItem{Title: "spam a"})
	b := h.addItem(feed, seedItem{Title: "spam b"})
	d := h.addItem(feed, seedItem{Title: "spam d"})
	for _, it := range []int64{a, b, d} {
		h.exec("UPDATE items SET read = 1, muted_by = ? WHERE id = ?", id, it)
	}

	// mark unread is the restore
	code, out, _ := h.api(c, "POST", "/api/items/mark-read", jsonStr(map[string]any{"ids": []string{sid(a)}, "read": false}))
	require.Equal(t, 200, code, "%v", out)
	require.Equal(t, 0, h.count("SELECT count(*) FROM items WHERE id = ? AND (read = 1 OR muted_by IS NOT NULL)", a))
	// starring is the un-mute: stays read, appears in All
	code, _, _ = h.api(c, "PUT", "/api/items/"+sid(b)+"/star", `{"starred":true}`)
	require.Equal(t, 200, code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND read = 1 AND starred = 1 AND muted_by IS NULL", b))
	// opening a muted item reads it (already read) and leaves the mute alone
	code, _, _ = h.api(c, "POST", "/api/items/"+sid(d)+"/open", "")
	require.Equal(t, 200, code)
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE id = ? AND muted_by IS NOT NULL", d))
	_, out, _ = h.api(c, "GET", "/api/items?view=all", "")
	require.ElementsMatch(t, []string{sid(a), sid(b)}, itemIDs(t, out))
	_, out, _ = h.api(c, "GET", "/api/items?view=muted", "")
	require.Equal(t, []string{sid(d)}, itemIDs(t, out))
}

func TestBootstrapHighlights(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	on := h.mustFilter(c, map[string]any{"action": "highlight", "terms": []string{"kernel", "release notes"}, "fields": []string{"title", "content"},
		"case_sensitive": true, "whole_word": false, "scope": "feed", "feed_id": sid(feed)})
	h.mustFilter(c, map[string]any{"action": "highlight", "terms": []string{"off"}, "enabled": false})
	badCode, bad := h.postFilter(c, map[string]any{"action": "highlight", "terms": []string{"absent"}, "invert": true})
	require.Equal(t, 400, badCode, "an inverted highlight can never show, so it is refused")
	require.Equal(t, "bad_filter", bad["error"])
	require.Equal(t, "action", bad["field"])
	h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"muteme"}})
	code, out, _ := h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, 200, code)
	hl := out["highlights"].([]any)
	require.Len(t, hl, 1, "enabled, non-inverted highlight rules only")
	e := hl[0].(map[string]any)
	require.Equal(t, on, e["id"])
	require.Equal(t, "feed", e["scope"])
	require.Equal(t, sid(feed), e["feed_id"])
	require.Nil(t, e["folder_id"])
	require.Equal(t, []any{"kernel", "release notes"}, e["terms"])
	require.Equal(t, []any{"title", "content"}, e["fields"])
	require.Equal(t, true, e["case_sensitive"])
	require.Equal(t, false, e["whole_word"])
	require.Equal(t, true, e["fold_diacritics"])
	require.NotContains(t, e, "action")
	require.NotContains(t, e, "hits")
}
