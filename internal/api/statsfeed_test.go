package api

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// feed= scopes the summary to one feed; anything but a positive id is refused.
func TestStatsSummaryOneFeed(t *testing.T) {
	h := newHarness(t)
	f1 := h.addFeed("Alpha", 0)
	f2 := h.addFeed("Beta", 0)
	h.stat("open", "2026-09-20", 9, 1, f1, "s1", nil, "Alpha")
	h.stat("read_time", "2026-09-20", 9, 1, f1, "s1", 40, "Alpha")
	h.stat("open", "2026-09-21", 9, 2, f2, "s2", nil, "Beta")
	h.stat("read_time", "2026-09-21", 9, 2, f2, "s2", 15, "Beta")

	code, out := h.summary(nil, "?range=month&feed="+strconv.FormatInt(f1, 10))
	require.Equal(t, 200, code, "%v", out)
	tot := out["totals"].(map[string]any)
	require.EqualValues(t, 1, num(tot["items_read"]))
	require.EqualValues(t, 40, num(tot["active_seconds"]))
	src := out["sources"].([]any)
	require.Len(t, src, 1)
	require.Equal(t, "Alpha", src[0].(map[string]any)["feed_title"])
	require.Equal(t, "2026-09-20", out["covered_from"], "coverage is that of all statistics")
	// No arrival was ever counted: the rate is null (a dash), never 0.
	require.Equal(t, "2026-09-20", out["read_rate_from"])
	require.NotNil(t, out["read_rate_to"])
	rr := out["read_rate"].(map[string]any)
	require.Contains(t, rr, "rate")
	require.Nil(t, rr["rate"])
	require.EqualValues(t, 0, num(rr["items_read"]), "only reads of counted items")
	require.Nil(t, src[0].(map[string]any)["read_rate"].(map[string]any)["rate"])

	// Four counted arrivals on the 20th, items 1 to 4: one of them read.
	require.NoError(t, h.db.WithWrite(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO feed_daily_new (local_date, feed_id, first_item, last_item, new_items)
			VALUES ('2026-09-20', ?, 1, 4, 4)`, f1)
		return err
	}))
	_, out = h.summary(nil, "?range=month&feed="+strconv.FormatInt(f1, 10))
	rr = out["read_rate"].(map[string]any)
	require.EqualValues(t, 4, num(rr["new_items"]))
	require.InDelta(t, 0.25, num(rr["rate"]), 1e-9)

	for _, bad := range []string{"0", "-3", "abc", "1.5"} {
		code, _ := h.summary(nil, "?range=month&feed="+bad)
		require.Equal(t, 400, code, bad)
	}
}
