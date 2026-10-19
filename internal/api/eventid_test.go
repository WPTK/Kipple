package api

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func eventIDOf(t *testing.T, out map[string]any) uint64 {
	t.Helper()
	s, ok := out["event_id"].(string)
	require.True(t, ok, "event_id is a string id: %v", out["event_id"])
	n, err := strconv.ParseUint(s, 10, 64)
	require.NoError(t, err)
	return n
}

// An answer that lists runs says which event it was read before (issue #536): every run event up to that id has
// already changed what the answer lists. The id is read before the runs, so an event published while the scheduler
// looks is newer than the answer, and a client must not treat the answer as knowing it.
func TestStatusAndBootstrapNameAnEventBeforeTheyLookAtRuns(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()

	var during uint64
	h.sched.onStatus = func() bool {
		h.hub.Publish("run.done", map[string]any{"run_id": "42", "kind": "manual", "new_items": 0, "errors": 0})
		during = h.hub.LastID() // the event that arrived while Status was looking
		return true
	}
	for _, path := range []string{"/api/status", "/api/bootstrap"} {
		before := h.hub.LastID()
		_, out, _ := h.api(c, "GET", path, "")
		require.NotZero(t, during)
		require.Equal(t, before, eventIDOf(t, out), "%s names the last event before it looked", path)
		require.Less(t, eventIDOf(t, out), during, "%s: an event published during the call is newer than the answer", path)
	}
}

// A scheduler that does not answer gives no runs, which is not "nothing is running": the answer carries event_id "0"
// (no ordering), so a client never ends a run from it (issue #539).
func TestStatusAndBootstrapWithoutAnAnswerFromTheSchedulerCarryNoOrdering(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	h.hub.Publish("run.done", map[string]any{"run_id": "42", "kind": "manual", "new_items": 0, "errors": 0})
	require.NotZero(t, h.hub.LastID())
	h.sched.onStatus = func() bool { return false }
	for _, path := range []string{"/api/status", "/api/bootstrap"} {
		code, out, _ := h.api(c, "GET", path, "")
		require.Equal(t, 200, code)
		require.Equal(t, "0", out["event_id"], path)
	}
}

// A 202 that names a run it started reads the event id after the start: the id is at or above the run's own
// run.start, so a client seeding from it is not older than the start.
func TestRefreshAndImportAcceptedNameAnEventAtOrAfterTheirStart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	var startID uint64
	h.sched.onRun = func() {
		h.hub.Publish("run.start", map[string]any{"run_id": "42", "kind": "manual", "total": 1})
		startID = h.hub.LastID()
	}

	startID = 0
	_, ref, _ := h.api(c, "POST", "/api/refresh", "")
	require.NotZero(t, startID)
	require.GreaterOrEqual(t, eventIDOf(t, ref), startID, "refresh")

	startID = 0
	doc := `<?xml version="1.0"?><opml version="2.0"><head/><body><outline type="rss" text="Alpha" xmlUrl="https://a.example/feed.xml"/></body></opml>`
	rec := h.do("POST", "/api/opml", doc, withCookie(c))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var imp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imp))
	require.NotZero(t, startID)
	require.GreaterOrEqual(t, eventIDOf(t, imp), startID, "import")
}

// The 202 of the auto-read catch-up names an event at or after its run.start, so a client seeding the run from it is
// not older than the run's own start.
func TestAutoReadAcceptedNamesAnEventAtOrAfterItsStart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	sub := h.hub.Subscribe(h.hub.LastID())
	defer sub.Close()

	code, out, _ := h.api(c, "POST", "/api/library/auto-read/run", `{"days":30}`)
	require.Equal(t, 202, code, out)
	var startID uint64
	deadline := time.After(5 * time.Second)
	for startID == 0 {
		select {
		case ev := <-sub.C:
			if ev.Type == "run.start" {
				startID = ev.ID
			}
		case <-deadline:
			t.Fatal("no run.start")
		}
	}
	require.GreaterOrEqual(t, eventIDOf(t, out), startID)
	h.waitAutoReadIdle()
}
