package greader

import (
	"database/sql"
	"testing"
	"time"

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

// A bulk star of ~10k ids must finish well inside the 10 s write deadline: the
// stats path loads the time zone once and looks each feed up once per batch.
func TestEditTagBulkStarFinishesWellInsideWriteDeadline(t *testing.T) {
	n := 10000
	if testing.Short() {
		n = 2000
	}
	h := newHarness(t)
	h.api.opt.Stats = stats.New(h.clk.Now)
	f := h.addFeed("https://a.example/f", "A", "")
	ids := make([]string, n)
	for i, id := range seedN(h, f, n, nil) {
		ids[i] = FormatDecimal(id)
	}
	body := editBody("a="+starred, ids...)

	start := time.Now()
	w := h.do("POST", base+rd+"edit-tag", body, map[string]string{"User-Agent": "Reeder/5.4"})
	took := time.Since(start)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "OK", w.Body.String())
	require.Less(t, took, 3*time.Second, "bulk star of %d ids took %s", n, took)
	require.Equal(t, n, q[int](h, "SELECT count(*) FROM stats_events WHERE kind = 'star' AND client = 'reeder'"))
	require.Equal(t, n, q[int](h, "SELECT count(*) FROM items WHERE starred = 1"))
	t.Logf("bulk star of %d ids: %s", n, took)
}

// batchRecorder counts which stats entry point edit-tag star/unstar uses.
type batchRecorder struct{ singles, batches, ids int }

func (b *batchRecorder) Record(*sql.Tx, stats.Event) error { b.singles++; return nil }
func (b *batchRecorder) RecordStars(_ *sql.Tx, _, _ string, ids []int64) error {
	b.batches++
	b.ids += len(ids)
	return nil
}

// Star/unstar go through the batched RecordStars path, one call per operation,
// never one Record per id.
func TestEditTagStarUsesBatchedStatsPath(t *testing.T) {
	h := newHarness(t)
	rec := &batchRecorder{}
	h.api.opt.Stats = rec
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 50, nil)
	var s []string
	for _, id := range ids {
		s = append(s, FormatDecimal(id))
	}
	w := h.post(rd+"edit-tag", editBody("a="+starred, s...))
	require.Equal(t, 200, w.Code)
	require.Equal(t, batchRecorder{singles: 0, batches: 1, ids: 50}, *rec)
	w = h.post(rd+"edit-tag", editBody("a="+readSt, s...)) // read changes are not stats events
	require.Equal(t, 200, w.Code)
	require.Equal(t, batchRecorder{singles: 0, batches: 1, ids: 50}, *rec)
}
