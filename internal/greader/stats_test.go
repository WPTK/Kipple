package greader

import (
	"context"
	"database/sql"
	"runtime/debug"
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
	agents := []string{"SyncApp/5.4", "OtherReader/2 (https://reader.example)", ""}
	calls := 0
	post := func(body string) { // each call from a different client: they all record as api
		w := h.do("POST", base+rd+"edit-tag", body, map[string]string{"User-Agent": agents[calls%len(agents)]})
		calls++
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

	require.Equal(t, 3, q[int](h, "SELECT count(*) FROM stats_events WHERE client = 'api' AND inferred = 0 AND session_key IS NULL"))
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
	if testing.Short() || raceEnabled() { // the full 10k under the race detector is slower than the deadline itself
		n = 1000
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
	w := h.do("POST", base+rd+"edit-tag", body, map[string]string{"User-Agent": "SyncApp/5.4"})
	took := time.Since(start)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, "OK", w.Body.String())
	limit := 3 * time.Second
	if raceEnabled() {
		limit = 8 * time.Second // shared CI runners under the race detector measured 3.0-3.3 s for 1000 ids
	}
	require.Less(t, took, limit, "bulk star of %d ids took %s", n, took)
	require.Equal(t, n, q[int](h, "SELECT count(*) FROM stats_events WHERE kind = 'star' AND client = 'api'"))
	require.Equal(t, n, q[int](h, "SELECT count(*) FROM items WHERE starred = 1"))
	t.Logf("bulk star of %d ids: %s", n, took)
}

// batchRecorder counts which stats entry point edit-tag star/unstar uses.
type batchRecorder struct{ singles, batches, ids int }

func (b *batchRecorder) Record(*sql.Tx, stats.Event) error { b.singles++; return nil }
func (b *batchRecorder) RecordMany(_ *sql.Tx, evs []stats.Event) error {
	b.singles += len(evs)
	return nil
}
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

// raceEnabled reports whether the test binary was built with -race (build info,
// no build tag needed): the detector is ~10x slower, so batch sizes shrink there.
func raceEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// With stats.enabled off, star edits still succeed and record nothing.
func TestEditTagStarWithStatsOff(t *testing.T) {
	h := newHarness(t)
	h.api.opt.Stats = stats.New(h.clk.Now)
	require.NoError(t, h.db.SetSettings(context.Background(), map[string]any{"stats.enabled": false}))
	f := h.addFeed("https://a.example/f", "A", "")
	ids := seedN(h, f, 2, nil)
	w := h.do("POST", base+rd+"edit-tag", editBody("a="+starred, FormatDecimal(ids[0]), FormatDecimal(ids[1])),
		map[string]string{"User-Agent": "SyncApp/5.4"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, 2, q[int](h, "SELECT count(*) FROM items WHERE starred = 1"))
	require.Equal(t, 0, q[int](h, "SELECT count(*) FROM stats_events"))
}
