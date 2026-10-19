package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Review finding: a shutdown during an apply's candidate count made startApply return the raw
// context error, and createFilter answered 500 although the filter had been committed. It now
// answers 201 with the apply refused as busy, like any other start that could not run.
func TestCreateWithApplyDuringShutdownIsCreatedNot500(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 10)
	testApplyCountHook = func() { h.srv.apply.stop() }
	t.Cleanup(func() { testApplyCountHook = nil })
	code, out := h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
	require.Equal(t, http.StatusCreated, code, "%v", out)
	require.Equal(t, map[string]any{"error": "busy"}, out["applied"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM filters"))
	require.Nil(t, h.srv.applyStatus())
}

// Review finding: a filter-delete batch that waited on the commit gate past the request budget ran
// into the hard backstop and answered 500 after earlier work had committed, so the web loop stopped
// with the rule disabled and items still muted. It now answers 202 {done:false}, and the repeat
// finishes once the gate is free.
func TestFilterDeleteGateWaitPastBudgetIsResumable(t *testing.T) {
	oldB, oldS := deleteFilterBudget, deleteFilterSlack
	deleteFilterBudget, deleteFilterSlack = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { deleteFilterBudget, deleteFilterSlack = oldB, oldS })
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 40)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
	require.Eventually(t, func() bool {
		return h.srv.applyStatus() == nil && h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL") == 20
	}, 10*time.Second, 20*time.Millisecond)

	release, err := h.db.AcquireGate(context.Background()) // a long fetch commit holds the gate
	require.NoError(t, err)
	code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
	require.Equal(t, http.StatusAccepted, code, "%v", out)
	require.Equal(t, false, out["done"])
	require.Equal(t, 1, h.count("SELECT count(*) FROM filters WHERE enabled = 0"), "the rule stays, disabled")
	release()

	code, out, _ = h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
	require.Equal(t, http.StatusOK, code, "%v", out)
	require.Equal(t, true, out["done"])
	require.Zero(t, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"))
	require.Zero(t, h.count("SELECT count(*) FROM filters"))
}

// A run is listed (bootstrap, /api/status) while it counts its candidates, so it ends with a run.done even when the
// count is cancelled by an edit of the rule or by a shutdown: a client showing it would otherwise wait for an end that
// never comes.
func TestApplyThatFailsDuringItsCountPublishesRunDone(t *testing.T) {
	cases := map[string]func(h *harness){
		"cancelled by an edit": func(h *harness) {
			h.srv.apply.mu.Lock()
			cancel := h.srv.apply.cancelRun
			h.srv.apply.mu.Unlock()
			cancel(errApplyCancelled)
		},
		"shutdown": func(h *harness) { h.srv.apply.stop() },
	}
	for name, interrupt := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c := h.login()
			feed := h.addFeed("A", 0)
			h.bulkItems(feed, 10)
			sub := h.hub.Subscribe(h.hub.LastID())
			defer sub.Close()
			var listed string
			testApplyCountHook = func() {
				listed = idStr(h.srv.applyStatus().ID) // the run a client can already see
				interrupt(h)
			}
			t.Cleanup(func() { testApplyCountHook = nil })
			h.postFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
			require.Nil(t, h.srv.applyStatus())
			deadline := time.After(5 * time.Second)
			for {
				select {
				case ev := <-sub.C:
					if ev.Type != "run.done" {
						continue
					}
					var m map[string]any
					require.NoError(t, json.Unmarshal(ev.Data, &m))
					require.Equal(t, listed, m["run_id"])
					require.Equal(t, "cancelled", m["error"])
					return
				case <-deadline:
					t.Fatal("no run.done for the run that was listed")
				}
			}
		})
	}
}
