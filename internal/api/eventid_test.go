package api

import (
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

// An answer that lists runs says which event it was read after (issue #536): a client compares that id with the run
// events it has seen to tell an answer that predates them from one that does not. It is the hub's last event id.
func TestStatusAndBootstrapSayWhichEventTheyFollow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()

	_, st, _ := h.api(c, "GET", "/api/status", "")
	require.Equal(t, h.hub.LastID(), eventIDOf(t, st))
	_, bs, _ := h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, h.hub.LastID(), eventIDOf(t, bs))

	before := eventIDOf(t, st)
	h.hub.Publish("run.done", map[string]any{"run_id": "42", "kind": "manual", "new_items": 0, "errors": 0})
	_, st, _ = h.api(c, "GET", "/api/status", "")
	require.Equal(t, before+1, eventIDOf(t, st), "an answer read after an event names that event or a later one")
	_, bs, _ = h.api(c, "GET", "/api/bootstrap", "")
	require.Equal(t, before+1, eventIDOf(t, bs))

	_, ref, _ := h.api(c, "POST", "/api/refresh", "")
	require.Equal(t, h.hub.LastID(), eventIDOf(t, ref))
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
