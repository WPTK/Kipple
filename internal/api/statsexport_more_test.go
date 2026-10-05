package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatsExportMetadataAndCSVOptions(t *testing.T) {
	h := newHarness(t)
	h.setSetting("tz", `"UTC"`)
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, item_title)
		VALUES (1780000100, '2026-09-21', 9, 1, 'open', 'web', 2, 1, 'F', ?)`, "a\rb")
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title, item_title)
		VALUES (1780000101, '2026-09-21', 9, 1, 'open', 'web', 3, 1, 'F', ?)`, "c\r\nd\ne")
	rec := h.export("?titles=0&include_inferred=1")
	require.Equal(t, `attachment; filename="kipple-stats-20260924-no-titles.csv"`, rec.Header().Get("Content-Disposition"))
	require.Equal(t, "0", rec.Header().Get("X-Kipple-Titles-Included"))
	require.Equal(t, "UTC", rec.Header().Get("X-Kipple-TZ"))
	require.Equal(t, "1", rec.Header().Get("X-Kipple-Include-Inferred"))
	require.Equal(t, "2", rec.Header().Get("X-Kipple-Rows"))
	rec = h.export("?format=jsonl")
	require.Equal(t, "1", rec.Header().Get("X-Kipple-Titles-Included"))
	require.Equal(t, "0", rec.Header().Get("X-Kipple-Include-Inferred"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), `kipple-stats-20260924.jsonl"`)
	require.Contains(t, h.export("?content=summary&titles=0").Header().Get("Content-Disposition"), "kipple-stats-summary-20260924-no-titles.json")

	// Line breaks: CRLF, LF and a lone CR each stay one break.
	recs := readCSV(t, h.export("").Body.String())
	require.Equal(t, "a\nb", recs[1][13])
	require.Equal(t, "c\nd\ne", recs[2][13])

	// BOM: off by default, on with bom=1, csv only, and the file still parses after it.
	require.False(t, bytes.HasPrefix(h.export("").Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	bom := h.export("?bom=1").Body.Bytes()
	require.True(t, bytes.HasPrefix(bom, []byte{0xEF, 0xBB, 0xBF}))
	require.Len(t, readCSV(t, string(bom[3:])), 3)
	require.False(t, bytes.HasPrefix(h.export("?format=jsonl&bom=1").Body.Bytes(), []byte{0xEF, 0xBB, 0xBF}))
	require.Equal(t, 400, h.export("?bom=2").Code)
}

func TestStatsExportAllRangeToIsToday(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	h.stat("open", "2026-09-10", 9, 1, f, "a", nil)
	h.stat("open", "2026-12-31", 9, 2, f, "b", nil) // after today (2026-09-24): a zone change or a clock that moved back
	var doc struct {
		Range struct {
			Key, From, To string
			LastEventDate *string `json:"last_event_date"`
		}
		Count int `json:"event_count"`
	}
	require.NoError(t, json.Unmarshal(h.export("?format=json").Body.Bytes(), &doc))
	require.Equal(t, "all", doc.Range.Key)
	require.Equal(t, "2026-09-10", doc.Range.From)
	require.Equal(t, "2026-09-24", doc.Range.To, "to is today, never the newest date present")
	require.NotNil(t, doc.Range.LastEventDate)
	require.Equal(t, "2026-12-31", *doc.Range.LastEventDate)
	require.Equal(t, 2, doc.Count, "the raw all export is unbounded and includes the future-dated row")

	_, sum := h.summary(nil, "?range=all")
	rg := sum["range"].(map[string]any)
	require.Equal(t, "2026-09-24", rg["to"])
	require.Equal(t, "2026-12-31", rg["last_event_date"])
	require.EqualValues(t, 15, num(rg["days"]))
	require.EqualValues(t, 1, num(sum["totals"].(map[string]any)["opens"]), "the all summary covers first..today only")

	// Nothing recorded yet: today on both ends, and no last event.
	h2 := newHarness(t)
	require.NoError(t, json.Unmarshal(h2.export("?format=json").Body.Bytes(), &doc))
	require.Equal(t, "2026-09-24", doc.Range.From)
	require.Equal(t, "2026-09-24", doc.Range.To)
	require.Nil(t, doc.Range.LastEventDate)
	_, sum = h2.summary(nil, "?range=all")
	require.Nil(t, sum["range"].(map[string]any)["last_event_date"])
}

// The header count is the number of records, kind by kind, whatever the range and filters.
func TestStatsExportRowsHeaderMatchesRecords(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	for i, k := range []string{"open", "read_time", "scroll", "star", "unstar", "open_original", "share"} {
		h.stat(k, "2026-09-20", 9, int64(i+1), f, "s"+k, 5)
		h.stat(k, "2026-09-21", 9, int64(i+10), f, "t"+k, 5)
	}
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id, feed_title)
		VALUES (1780000900, '2026-09-21', 9, 1, 'open', 'api', 1, 50, ?, 'F'), (1780000901, '2026-09-21', 9, 1, 'star', 'api', 1, 51, ?, 'F')`, f, f)
	for _, q := range []string{"", "?include_inferred=1", "?from=2026-09-21&to=2026-09-21", "?from=2026-09-21&to=2026-09-21&include_inferred=1", "?range=week", "?from=2020-01-01&to=2020-01-02"} {
		rec := h.export(q)
		recs := readCSV(t, rec.Body.String())
		require.Equal(t, fmt.Sprint(len(recs)-1), rec.Header().Get("X-Kipple-Rows"), q)
		require.Equal(t, fmt.Sprint(len(recs)-1), rec.Result().Trailer.Get("X-Kipple-Rows-Sent"), q)
	}
}

func TestStatsExportRowsSentTrailer(t *testing.T) {
	h := newHarness(t)
	h.bulkStats(5001, "2026-09-20")
	for _, f := range []string{"csv", "json", "jsonl"} {
		rec := h.export("?format=" + f)
		require.Equal(t, "X-Kipple-Rows-Sent", rec.Header().Get("Trailer"), f)
		require.Equal(t, "5001", rec.Result().Trailer.Get("X-Kipple-Rows-Sent"), f)
		require.Equal(t, "5001", rec.Header().Get("X-Kipple-Rows"), f)
	}
	head := h.do("HEAD", "/api/stats/export", "", withCookie(h.login()), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Empty(t, head.Header().Get("Trailer"))
}

// Deleting old data must not change how the data that stays is counted: the legacy-open cutoff is
// the earliest timed event ever recorded, remembered across deletes.
func TestStatsDeleteKeepsLegacyCutoff(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	h.stat("open", "2026-09-01", 9, 1, f, "s1", nil)
	h.stat("read_time", "2026-09-01", 9, 1, f, "s1", 30) // the earliest timed event
	h.stat("open", "2026-09-10", 9, 2, f, "s2", nil)     // no timed rows: a bounce, since it is after the first timed event
	h.stat("open", "2026-09-20", 9, 3, f, "s3", nil)
	h.stat("read_time", "2026-09-20", 9, 3, f, "s3", 30)
	q := "?from=2026-09-05&to=2026-09-24"
	_, before := h.summary(nil, q)
	tot := before["totals"].(map[string]any)
	require.EqualValues(t, 1, num(tot["items_read"]))
	require.EqualValues(t, 2, num(tot["opens"]))

	code, out := h.del(`{"from":"2026-09-01","to":"2026-09-01"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 2, num(out["deleted"]))
	_, after := h.summary(nil, q)
	require.Equal(t, before["totals"], after["totals"], "kept opens are classified as before")
	require.Equal(t, before["sources"], after["sources"])
	require.EqualValues(t, 1, num(after["sources"].([]any)[0].(map[string]any)["bounces"]))

	// Delete everything, then record a short session that began before its read time: it stays a
	// bounce, because the cutoff is still the old one and not that new read_time's ts.
	code, _ = h.del(`{"all":true,"confirm":"DELETE ALL"}`)
	require.Equal(t, 200, code)
	h.stat("open", "2026-09-24", 8, 9, f, "s9", nil)
	h.stat("read_time", "2026-09-24", 9, 9, f, "s9", 3)
	_, sum := h.summary(nil, "?from=2026-09-01&to=2026-09-24")
	require.EqualValues(t, 0, num(sum["totals"].(map[string]any)["items_read"]))
	require.EqualValues(t, 1, num(sum["sources"].([]any)[0].(map[string]any)["bounces"]))
}

// A delete that fails after earlier windows committed says how many rows went.
func TestStatsDeletePartialFailureReportsTheCount(t *testing.T) {
	h := newHarness(t)
	h.bulkStats(25000, "2026-09-20")
	h.exec(`CREATE TRIGGER stop_delete BEFORE DELETE ON stats_events WHEN OLD.id > 10000 BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	code, out := h.del(`{"from":"2026-09-20","to":"2026-09-20"}`)
	require.Equal(t, 500, code)
	require.Equal(t, "internal", out["error"])
	require.Equal(t, false, out["complete"])
	require.EqualValues(t, 10000, num(out["deleted"]))
	require.Contains(t, out["message"], "10000")
	require.Equal(t, 15000, h.count("SELECT count(*) FROM stats_events"), "the first window stayed committed")
	h.exec(`DROP TRIGGER stop_delete`)
	code, out = h.del(`{"from":"2026-09-20","to":"2026-09-20"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 15000, num(out["deleted"]), "a rerun finishes")
}
