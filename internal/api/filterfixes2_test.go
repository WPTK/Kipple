package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Review finding: a shutdown during an apply's candidate count made startApply return the raw
// context error, and createFilter answered 500 although the filter had been committed. It now
// answers 201 with the apply refused as busy, like any other start that could not run.
func TestCreateWithApplyDuringShutdownIsCreatedNot500(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.login()
	feed := h.addFeed("A", 0)
	h.bulkItems(feed, 10)
	h.srv.applyCounting = func() { h.srv.apply.stop() }
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
	t.Parallel()
	h := newHarness(t)
	h.srv.tm.deleteFilterBudget, h.srv.tm.deleteFilterSlack = 50*time.Millisecond, 300*time.Millisecond
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
