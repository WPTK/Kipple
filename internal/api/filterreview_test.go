package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// GET /api/status carries the active filter apply run and the muted count, like bootstrap.
func TestStatusHasMutedCountAndApplyRun(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 20)
	h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
	require.Eventually(t, func() bool {
		return h.srv.applyStatus() == nil && h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL") == 10
	},
		5*time.Second, 20*time.Millisecond)

	code, out, _ := h.api(c, "GET", "/api/status", "")
	require.Equal(t, 200, code)
	require.EqualValues(t, 10, out["muted"])
	require.Len(t, out["runs"], 1, "the scheduler's (fake) run only")

	h.srv.apply.mu.Lock()
	h.srv.apply.run = &applyRun{ID: 7, Kind: runKindFilterApply, FilterID: 3, Total: 9}
	h.srv.apply.mu.Unlock()
	defer func() {
		h.srv.apply.mu.Lock()
		h.srv.apply.run = nil
		h.srv.apply.mu.Unlock()
	}()
	_, out, _ = h.api(c, "GET", "/api/status", "")
	runs := out["runs"].([]any)
	require.Len(t, runs, 2)
	require.Equal(t, "filter_apply", runs[1].(map[string]any)["kind"])
}

// Deleting or editing a rule stops its running apply first and waits for it: nothing is written for
// the old rule afterwards.
func TestDeleteAndPatchCancelRunningApply(t *testing.T) {
	for _, tc := range []string{"delete", "patch"} {
		t.Run(tc, func(t *testing.T) {
			h := newHarness(t)
			c := h.login()
			feed := h.addFeed("A", 0)
			h.bulkItems(feed, 4000)
			id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}})
			code, _, _ := h.api(c, "POST", "/api/filters/"+id+"/apply", "")
			require.Equal(t, http.StatusAccepted, code)
			if tc == "delete" {
				code, _, _ = h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
			} else {
				code, _, _ = h.api(c, "PATCH", "/api/filters/"+id, `{"terms":["ham"]}`)
			}
			require.Equal(t, 200, code)
			require.Nil(t, h.srv.applyStatus(), "the run has finished when the request returns")
			n := h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL")
			time.Sleep(200 * time.Millisecond)
			require.Equal(t, n, h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL"), "no writes after the rule changed")
			if tc == "delete" {
				require.Zero(t, n)
			}
		})
	}
}

// A retry of a delete whose row is already gone still restores the orphans.
func TestDeleteRetryRestoresOrphans(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 40)
	id := h.mustFilter(c, map[string]any{"action": "mute", "terms": []string{"spam"}, "apply_existing": map[string]any{}})
	require.Eventually(t, func() bool { return h.count("SELECT count(*) FROM items WHERE muted_by IS NOT NULL") == 20 }, 5*time.Second, 20*time.Millisecond)
	code, _, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=keep", "")
	require.Equal(t, 200, code)
	code, out, _ := h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
	require.Equal(t, 200, code, "%v", out)
	require.EqualValues(t, 20, out["changed"])
	code, _, _ = h.api(c, "DELETE", "/api/filters/"+id+"?unmute=read", "")
	require.Equal(t, 404, code)
}
