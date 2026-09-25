package greader

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/stats"
)

// edit-tag star/unstar record one row per id that actually changed (design §8);
// read changes and no-op replays record nothing.
func TestEditTagRecordsStarStats(t *testing.T) {
	h := newHarness(t)
	h.api.opt.Stats = stats.New(h.clk.Now)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 3, nil)
	reeder := map[string]string{"User-Agent": "Reeder/5.4"}
	post := func(body string) {
		w := h.do("POST", base+rd+"edit-tag", body, reeder)
		require.Equal(t, 200, w.Code)
	}
	count := func(kind string) int {
		return q[int](h, "SELECT count(*) FROM stats_events WHERE kind = ?", kind)
	}

	post(editBody("a="+starred, FormatDecimal(ids[0]), FormatDecimal(ids[1])))
	require.Equal(t, 2, count("star"))
	post(editBody("a="+starred, FormatDecimal(ids[0]))) // replay: RETURNING shows no change
	require.Equal(t, 2, count("star"))
	post(editBody("r="+starred, FormatDecimal(ids[0]), FormatDecimal(ids[2]))) // ids[2] was never starred
	require.Equal(t, 1, count("unstar"))
	post(editBody("a="+readSt, FormatDecimal(ids[2])))
	require.Equal(t, 3, q[int](h, "SELECT count(*) FROM stats_events"))

	require.Equal(t, 3, q[int](h, "SELECT count(*) FROM stats_events WHERE client = 'reeder' AND inferred = 0 AND session_key IS NULL"))
	require.Equal(t, "A", q[string](h, "SELECT feed_title FROM stats_events WHERE kind = 'unstar'"))
}

// A star that restores a trimmed item is a change and records a row.
func TestEditTagStarRestoreRecordsStat(t *testing.T) {
	h := newHarness(t)
	h.api.opt.Stats = stats.New(h.clk.Now)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 1, nil)
	h.trim(ids[0], true)
	h.post(rd+"edit-tag", editBody("a="+starred, FormatDecimal(ids[0])))
	require.Equal(t, 1, q[int](h, "SELECT count(*) FROM stats_events WHERE kind = 'star' AND item_id = ?", ids[0]))
}
