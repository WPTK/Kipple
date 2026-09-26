package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Deleting with unmute=unread reports how many items went back to unread, which is fewer than it
// restored when some were read before the rule muted them.
func TestFilterDeleteReportsMadeUnread(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 20)
	h.exec("UPDATE items SET read = 1 WHERE title IN ('spam 2', 'spam 4', 'spam 6', 'spam 8')")
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	code, _, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", `{"include_read":true}`)
	require.Equal(t, http.StatusAccepted, code)
	collect(t, sub, "run.done", 5*time.Second)
	require.Equal(t, 10, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))

	code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=unread", "")
	require.Equal(t, 200, code, "%v", out)
	require.EqualValues(t, 10, out["changed"])
	require.EqualValues(t, 6, out["made_unread"])
	require.Equal(t, true, out["done"])
}

// A restore larger than one request's budget answers 202 {done:false} with the rule disabled; the
// client repeats the DELETE until done, and each answer comes back well inside the WriteTimeout.
func TestFilterDeleteIsBoundedAndResumable(t *testing.T) {
	old := deleteFilterBudget
	deleteFilterBudget = 0 // one batch per request
	t.Cleanup(func() { deleteFilterBudget = old })
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 2400)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
	require.Eventually(t, func() bool {
		return h.srv.applyStatus() == nil && h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL") == 1200
	}, 10*time.Second, 20*time.Millisecond)

	var codes []int
	var changed []float64
	for i := 0; i < 6; i++ {
		code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
		codes = append(codes, code)
		changed = append(changed, out["changed"].(float64))
		if code == 200 {
			require.Equal(t, true, out["done"])
			break
		}
		require.Equal(t, false, out["done"])
		require.Equal(t, 1, h.count("SELECT count(*) FROM filters WHERE enabled = 0"), "the rule stays, disabled, until the restore is done")
	}
	require.Equal(t, []int{202, 202, 200}, codes)
	require.Equal(t, []float64{500, 500, 200}, changed)
	require.Zero(t, h.count("SELECT count(*) FROM filters"))
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
}

// A PATCH that does not change how the rule matches or acts (a rename, a move) leaves a running apply
// alone; one that does cancels it, and run.done says "cancelled", not a failure.
func TestPatchCancelsApplyOnlyOnMatchingChanges(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		cancels    bool
	}{
		{"rename", `{"name":"Renamed","position":4}`, false},
		{"terms", `{"terms":["ham"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			c := h.login()
			feed := h.addFeed("A", 0)
			h.bulkItems(feed, 20000)
			id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
			sub := h.hub.Subscribe(0)
			defer sub.Close()
			code, _, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
			require.Equal(t, http.StatusAccepted, code)
			code, out, _ := h.api(c, "PATCH", "/api/filters/"+id, tc.body)
			require.Equal(t, 200, code, "%v", out)
			done := ofType(collect(t, sub, "run.done", 30*time.Second), "run.done")[0]
			if tc.cancels {
				require.Equal(t, "cancelled", done["error"])
				require.EqualValues(t, 0, done["errors"], "an intended stop is not a failure")
				return
			}
			require.Nil(t, done["error"], "%v", done)
			require.EqualValues(t, 10000, done["changed"])
		})
	}
}

// An apply that runs past applyBudget ends with its own reason, counted as a failure.
func TestApplyBudgetEndsTheRun(t *testing.T) {
	old := applyBudget
	applyBudget = time.Nanosecond
	t.Cleanup(func() { applyBudget = old })
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 50)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
	sub := h.hub.Subscribe(0)
	defer sub.Close()
	code, _, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
	require.Equal(t, http.StatusAccepted, code)
	done := ofType(collect(t, sub, "run.done", 5*time.Second), "run.done")[0]
	require.Equal(t, "timed_out", done["error"])
	require.EqualValues(t, 1, done["errors"])
}

// The candidate count runs outside the apply lock: while it runs, bootstrap's run list and a cancel
// from an edit do not wait on it, and a cancel during it ends the start with 409 cancelled.
func TestStartApplyCountsOutsideTheLock(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 10)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})

	counting, release := make(chan struct{}), make(chan struct{})
	testApplyCountHook = func() { close(counting); <-release }
	t.Cleanup(func() { testApplyCountHook = nil })

	type result struct {
		code int
		out  map[string]any
	}
	started := make(chan result, 1)
	go func() {
		code, out, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
		started <- result{code, out}
	}()
	<-counting

	status := make(chan *applyRun, 1)
	go func() { status <- h.srv.applyStatus() }()
	select {
	case st := <-status:
		require.NotNil(t, st, "the starting run is listed")
		require.Zero(t, st.Total, "its total is not known yet")
	case <-time.After(2 * time.Second):
		t.Fatal("applyStatus waited on the candidate count")
	}

	// An edit of the rule cancels the starting run; the count finishes, and the start reports it.
	patched := make(chan int, 1)
	go func() {
		code, _, _ := h.api(c, "PATCH", "/api/filters/"+id, `{"terms":["ham"]}`)
		patched <- code
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)
	res := <-started
	require.Equal(t, http.StatusConflict, res.code, "%v", res.out)
	require.Equal(t, "cancelled", res.out["error"])
	require.Equal(t, 200, <-patched)
	require.Nil(t, h.srv.applyStatus())
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
}

// An empty field list is title only: stored and served as ["title"], so the client draws the highlight.
func TestHighlightWithEmptyFieldsIsDrawnOnTitles(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	code, out := h.postFilter(c, map[string]any{"action": "highlight", "terms": []string{"go"}, "fields": []string{}})
	require.Equal(t, 201, code, "%v", out)
	require.Equal(t, []any{"title"}, out["filter"].(map[string]any)["fields"])
	h.exec("UPDATE filters SET fields = '[]'") // a row from before the fix
	_, out, _ = h.api(c, "GET", "/api/bootstrap", "")
	hs := out["highlights"].([]any)
	require.Len(t, hs, 1)
	require.Equal(t, []any{"title"}, hs[0].(map[string]any)["fields"])
}

// sendBeacon cannot set X-Kipple-Client, so a flush carries its client in the body: "web" or "pwa";
// anything else falls back to the header, then to web.
func TestStatsEventsClientFromBody(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	id := h.addItem(f, seedItem{})
	beacon := func(body map[string]any) {
		body["events"] = []any{map[string]any{"kind": "share", "item_id": sid(id)}}
		rec := h.do("POST", "/api/stats/events", jsonStr(body), withCookie(c), func(r *http.Request) {
			r.Header.Del("X-Kipple-Client")
			r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		})
		require.Equal(t, 204, rec.Code)
	}
	beacon(map[string]any{"client": "pwa"})
	beacon(map[string]any{"client": "web"})
	beacon(map[string]any{"client": "reeder"}) // not a web client: ignored
	beacon(map[string]any{})
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE kind = 'share' AND client = 'pwa'"))
	require.Equal(t, 3, h.count("SELECT count(*) FROM stats_events WHERE kind = 'share' AND client = 'web'"))
}
