package api

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/WPTK/kipple/internal/store"
	"github.com/stretchr/testify/require"
)

// bulkStatsBatch is the most rows bulkStats puts in one WithWrite call. WithWrite has a fixed 10 s
// deadline (store.writeTimeout); a single huge insert-per-exec transaction can trip it under
// `-race`, which slows every exec down a lot even though the whole test runs fine without it. So
// bulkStats commits in chunks, each its own transaction, however many rows it is asked for.
const bulkStatsBatch = 5000

// bulkStats inserts n open rows on date, in chunks of bulkStatsBatch, each its own transaction.
func (h *harness) bulkStats(n int, date string) {
	h.t.Helper()
	for done := 0; done < n; {
		batch := min(n-done, bulkStatsBatch)
		start := done
		require.NoError(h.t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			st, err := tx.PrepareContext(ctx, `INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id,
				feed_title, item_title, session_key) VALUES (1780000000, ?, 9, 2, 'open', 'web', ?, 1, 'F', 'T', ?)`)
			if err != nil {
				return err
			}
			defer st.Close()
			for i := 0; i < batch; i++ {
				if _, err := st.ExecContext(ctx, date, start+i+1, fmt.Sprintf("k%d", start+i)); err != nil {
					return err
				}
			}
			return nil
		}))
		done += batch
	}
}

func (h *harness) export(q string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	h.t.Helper()
	cc := h.login()
	mods := append([]func(*http.Request){withCookie(cc), func(r *http.Request) { r.Header.Del("X-Kipple-Client") }}, mod...)
	return h.do("GET", "/api/stats/export"+q, "", mods...)
}

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err)
	return recs
}

func TestStatsExportCSV(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	h.stat("open", "2026-09-20", 8, 1, f, "s1", nil, "Feed, \"one\"\nline")
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
		item_title, item_url, value, session_key) VALUES (1780000100, '2026-09-21', 9, 1, 'read_time', 'web', 2, ?, '=SUM(A1)', 'a, b "c"
d', '@x', 12, 's1')`, f)
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, inferred, item_id, feed_id, feed_title)
		VALUES (1780000200, '2026-09-22', 10, 2, 'open', 'reeder', 1, 3, ?, 'Inf')`, f)

	rec := h.export("")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "text/csv; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	require.Equal(t, `attachment; filename="kipple-stats-20260924.csv"`, rec.Header().Get("Content-Disposition"))
	recs := readCSV(t, rec.Body.String())
	require.Equal(t, csvHeader(), recs[0])
	require.Equal(t, "id,ts,local_date,local_hour,local_weekday,kind,client,inferred,item_id,feed_id,feed_title,folder_id,folder_name,item_title,item_url,value,session_key",
		strings.Join(recs[0], ","))
	require.Len(t, recs, 3, "the inferred row is left out by default")
	col := func(r []string, name string) string {
		for i, c := range recs[0] {
			if c == name {
				return r[i]
			}
		}
		t.Fatal("no column " + name)
		return ""
	}
	require.Equal(t, "Title 1", col(recs[1], "item_title"))
	require.Equal(t, "1", col(recs[1], "folder_id"))
	require.Equal(t, "Uncategorized", col(recs[1], "folder_name"))
	require.Equal(t, "'=SUM(A1)", col(recs[2], "feed_title"), "formula guard")
	require.Equal(t, "a, b \"c\"\nd", col(recs[2], "item_title"), "commas, quotes and newlines survive")
	require.Equal(t, "'@x", col(recs[2], "item_url"))
	require.Equal(t, "12", col(recs[2], "value"))
	require.Equal(t, "", col(recs[1], "value"))
	require.Equal(t, "0", col(recs[1], "inferred"))

	rec = h.export("?include_inferred=1&from=2026-09-22&to=2026-09-22")
	recs = readCSV(t, rec.Body.String())
	require.Len(t, recs, 2)
	require.Equal(t, "1", col(recs[1], "inferred"))

	rec = h.export("?titles=0&range=all")
	recs = readCSV(t, rec.Body.String())
	for _, r := range recs[1:] {
		require.Equal(t, "", col(r, "item_title"))
		require.Equal(t, "", col(r, "item_url"))
		require.NotEqual(t, "", col(r, "feed_title"), "feed names stay")
	}
	require.Equal(t, "Uncategorized", col(recs[1], "folder_name"))

	// The whole range filter is by local date, inclusive.
	rec = h.export("?from=2026-09-21&to=2026-09-21")
	require.Len(t, readCSV(t, rec.Body.String()), 2)

	// HEAD sets the headers and sends no rows.
	head := h.do("HEAD", "/api/stats/export", "", withCookie(h.login()), func(r *http.Request) { r.Header.Del("X-Kipple-Client") })
	require.Equal(t, 200, head.Code)
	require.Zero(t, head.Body.Len())
}

func TestStatsExportPagingAndFormats(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.addFeed("Alpha", 0)
	const n = 25034 // five pages: four full and a partial one
	h.bulkStats(n, "2026-09-20")
	h.bulkStats(50, "2026-09-10")

	rec := h.export("?range=all")
	recs := readCSV(t, rec.Body.String())
	require.Len(t, recs, n+50+1)
	last := int64(0)
	for _, r := range recs[1:] {
		var id int64
		_, err := fmt.Sscan(r[0], &id)
		require.NoError(t, err)
		require.Greater(t, id, last)
		last = id
	}

	rec = h.export("?format=jsonl&from=2026-09-20&to=2026-09-20")
	require.Equal(t, "application/x-ndjson; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Contains(t, rec.Header().Get("Content-Disposition"), `.jsonl"`)
	sc := bufio.NewScanner(rec.Body)
	lines := 0
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		require.Equal(t, "open", m["kind"])
		require.IsType(t, "", m["id"], "ids are strings")
		lines++
	}
	require.Equal(t, n, lines)
	require.Equal(t, fmt.Sprint(n), rec.Header().Get("X-Kipple-Rows"))

	rec = h.export("?format=json&range=all")
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	var doc struct {
		Format           string
		ExportedAt       string `json:"exported_at"`
		TZ               string
		Version          int
		Content          string
		TitlesIncluded   bool `json:"titles_included"`
		Range            map[string]string
		Dictionary       map[string]any
		Events           []map[string]any
		EventCount       int  `json:"event_count"`
		RecordingEnabled bool `json:"recording_enabled"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), rec.Body.String()[:300])
	require.Equal(t, "kipple-stats-export", doc.Format)
	require.Equal(t, 1, doc.Version)
	require.Equal(t, "raw", doc.Content)
	require.True(t, doc.TitlesIncluded)
	require.Equal(t, "America/New_York", doc.TZ)
	_, err := time.Parse(time.RFC3339, doc.ExportedAt)
	require.NoError(t, err)
	require.Equal(t, "all", doc.Range["key"])
	require.Equal(t, "2026-09-10", doc.Range["from"])
	require.Len(t, doc.Events, n+50)
	require.Equal(t, n+50, doc.EventCount)
	require.True(t, doc.RecordingEnabled)
	require.Equal(t, fmt.Sprint(n+50), rec.Header().Get("X-Kipple-Rows"))
	for _, k := range []string{"columns", "kinds", "concepts", "summary_fields"} {
		require.Contains(t, doc.Dictionary, k, "the full dictionary is embedded")
	}
	cols := doc.Dictionary["columns"].(map[string]any)
	for _, c := range csvHeader() {
		require.Contains(t, cols, c)
		require.NotEmpty(t, cols[c].(map[string]any)["description"])
	}
	require.NotContains(t, cols, "event_id")
	require.Len(t, doc.Events[0], len(csvHeader()))

	// An empty database still gives valid JSON.
	h2 := newHarness(t)
	rec = h2.export("?format=json")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	require.Empty(t, doc.Events)
	require.Zero(t, doc.EventCount)
	require.Equal(t, "0", rec.Header().Get("X-Kipple-Rows"))
	require.Len(t, readCSV(t, h2.export("").Body.String()), 1)
}

// cancelAfterFlush cancels the request context on the first flush, as a client that went away.
type cancelAfterFlush struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (c cancelAfterFlush) Flush() { c.ResponseRecorder.Flush(); c.cancel() }

func TestStatsExportStopsWhenCancelled(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.bulkStats(12000, "2026-09-20")
	cc := h.login()

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("GET", "/api/stats/export?format=jsonl", nil).WithContext(ctx)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(&http.Cookie{Name: cc.Name, Value: cc.Value})
	w := cancelAfterFlush{httptest.NewRecorder(), cancel}
	h.mux.ServeHTTP(w, r)
	lines := strings.Count(w.Body.String(), "\n")
	require.Equal(t, 5000, lines, "one page was written, then it stopped")

}

func TestStatsExportSummary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	h.stat("open", "2026-09-20", 9, 3, f, "s10", nil)
	h.stat("read_time", "2026-09-20", 9, 3, f, "s10", 30)

	rec := h.export("?content=summary&range=all")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="kipple-stats-summary-20260924.json"`, rec.Header().Get("Content-Disposition"))
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, "summary", out["content"])
	require.Equal(t, "kipple-stats-export", out["format"])
	require.Contains(t, out, "dictionary")
	require.Contains(t, out, "exported_at")
	require.Equal(t, true, out["titles_included"])
	require.Equal(t, true, out["recording_enabled"])
	require.Contains(t, out["dictionary"], "columns")
	require.EqualValues(t, 30, num(out["totals"].(map[string]any)["active_seconds"]))
	require.NotContains(t, out, "events")
	lr := out["behavior"].(map[string]any)["longest_read"].(map[string]any)
	require.Equal(t, "Title 3", lr["title"])
	require.Equal(t, "Feed", lr["feed_title"])

	// Same numbers as the summary endpoint.
	_, sum := h.summary(nil, "?range=all")
	require.Equal(t, sum["totals"], out["totals"])
	require.Equal(t, sum["range"], out["range"])

	rec = h.export("?content=summary&range=all&titles=0")
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	lr = out["behavior"].(map[string]any)["longest_read"].(map[string]any)
	require.Equal(t, "", lr["title"])
	require.Equal(t, "Feed", lr["feed_title"])
	require.Equal(t, false, out["titles_included"])
}

func TestStatsExportBadRequests(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, q := range []string{
		"?format=xml", "?format=CSV", "?content=all", "?content=summary&format=csv", "?content=summary&format=jsonl",
		"?titles=2", "?titles=yes", "?range=decade", "?from=2026-09-01", "?to=2026-09-01", "?from=2026-09-09&to=2026-09-01",
		"?from=nope&to=2026-09-01", "?from=2000-01-01&to=2026-09-01",
	} {
		rec := h.export(q)
		require.Equal(t, 400, rec.Code, q)
		var m map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		require.Equal(t, "bad_request", m["error"], q)
		require.NotEmpty(t, m["message"], q)
	}
}

func TestStatsExportGuards(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.Equal(t, 401, h.do("GET", "/api/stats/export", "").Code)
	require.Equal(t, 401, h.do("GET", "/api/stats/dictionary", "").Code)
	require.Equal(t, 401, h.do("POST", "/api/stats/delete", `{"all":true}`).Code)
	cc := h.login()
	cross := func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }
	noClient := func(r *http.Request) { r.Header.Del("X-Kipple-Client") }
	require.Equal(t, 403, h.do("GET", "/api/stats/export", "", withCookie(cc), cross, noClient).Code)
	require.Equal(t, 403, h.do("GET", "/api/stats/export", "", withCookie(cc), func(r *http.Request) {
		r.Header.Del("Sec-Fetch-Site")
		r.Header.Set("Origin", "https://evil.example")
	}).Code)
	require.Equal(t, 200, h.do("GET", "/api/stats/export", "", withCookie(cc), noClient).Code, "a plain download needs no client header")
	body := `{"from":"2026-01-01","to":"2026-01-02","dry_run":true}`
	require.Equal(t, 403, h.do("POST", "/api/stats/delete", body, withCookie(cc), noClient).Code, "delete needs X-Kipple-Client")
	require.Equal(t, 403, h.do("POST", "/api/stats/delete", body, withCookie(cc), cross).Code)
	require.Equal(t, 200, h.do("POST", "/api/stats/delete", body, withCookie(cc)).Code)
}

func TestStatsDictionary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cc := h.login()
	rec := h.do("GET", "/api/stats/dictionary", "", withCookie(cc))
	require.Equal(t, 200, rec.Code)
	var d struct {
		Columns  map[string]map[string]string
		Kinds    map[string]string
		Concepts map[string]string
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Len(t, d.Columns, len(statsColumns))
	require.Contains(t, d.Kinds, "read_time")
	require.Contains(t, d.Concepts["read"], "10 seconds")
	require.Contains(t, d.Concepts["read"], "25")
	require.Contains(t, d.Concepts["read"], "legacy")
	require.Contains(t, d.Concepts["local_time"], "local_date")

	rec = h.do("GET", "/api/stats/dictionary?format=md", "", withCookie(cc))
	require.Equal(t, 200, rec.Code)
	require.Equal(t, "text/markdown; charset=utf-8", rec.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="kipple-stats-dictionary.md"`, rec.Header().Get("Content-Disposition"))
	for _, c := range statsColumns {
		require.Contains(t, rec.Body.String(), "`"+c.Name+"`")
	}
	require.Equal(t, 400, h.do("GET", "/api/stats/dictionary?format=csv", "", withCookie(cc)).Code)
}

func (h *harness) del(body string) (int, map[string]any) {
	h.t.Helper()
	rec := h.do("POST", "/api/stats/delete", body, withCookie(h.login()))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestStatsDelete(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	f := h.addFeed("Alpha", 0)
	h.addItem(f, seedItem{SortAt: 1000, Read: true, Starred: true})
	h.setSetting("tz", `"UTC"`)
	h.bulkStats(25000, "2026-09-20") // more than two delete windows
	h.bulkStats(10, "2026-09-19")
	h.bulkStats(10, "2026-09-21")
	h.bulkStats(10, "2026-09-25")
	var tables []string
	rows, err := h.db.Reader().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		AND name != 'stats_events' AND name NOT LIKE '%session%' AND name NOT LIKE '%lock%'`)
	require.NoError(t, err)
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		tables = append(tables, n)
	}
	require.NoError(t, rows.Close())
	require.Contains(t, tables, "items")
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = h.count("SELECT count(*) FROM " + tb)
	}
	require.Equal(t, 25030, h.count("SELECT count(*) FROM stats_events"))

	code, out := h.del(`{"from":"2026-09-19","to":"2026-09-21","dry_run":true}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 25020, num(out["count"]))
	require.EqualValues(t, 0, num(out["deleted"]))
	require.Equal(t, 25030, h.count("SELECT count(*) FROM stats_events"), "a dry run deletes nothing")

	code, out = h.del(`{"from":"2026-09-19","to":"2026-09-21"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 25020, num(out["count"]))
	require.EqualValues(t, 25020, num(out["deleted"]))
	require.Equal(t, 10, h.count("SELECT count(*) FROM stats_events"))
	require.Equal(t, 10, h.count("SELECT count(*) FROM stats_events WHERE local_date = '2026-09-25'"), "outside the range is kept")
	for _, tb := range tables {
		require.Equal(t, before[tb], h.count("SELECT count(*) FROM "+tb), tb+" is untouched")
	}
	require.Equal(t, 1, h.count("SELECT count(*) FROM items WHERE starred = 1"))

	// Nothing left in the range: zero, and a later delete works with sparse ids.
	code, out = h.del(`{"from":"2026-09-19","to":"2026-09-21"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 0, num(out["deleted"]))
	code, out = h.del(`{"from":"2026-09-25","to":"2026-09-25"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 10, num(out["deleted"]))
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
}

func TestStatsDeleteAll(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.bulkStats(30, "2026-09-20")
	for _, body := range []string{`{"all":true}`, `{"all":true,"confirm":"delete all"}`, `{"all":true,"confirm":"DELETE ALL "}`, `{"all":true,"confirm":""}`} {
		code, out := h.del(body)
		require.Equal(t, 400, code, body)
		require.Equal(t, "bad_request", out["error"])
	}
	require.Equal(t, 30, h.count("SELECT count(*) FROM stats_events"))
	code, out := h.del(`{"all":true,"dry_run":true}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 30, num(out["count"]))
	require.Equal(t, 30, h.count("SELECT count(*) FROM stats_events"))
	code, out = h.del(`{"all":true,"confirm":"DELETE ALL"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 30, num(out["deleted"]))
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
	// The counter keeps counting: ids are never reused.
	h.bulkStats(1, "2026-09-20")
	require.Equal(t, 1, h.count("SELECT count(*) FROM stats_events WHERE id > 30"))
}

func TestStatsDeleteBadRequests(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.bulkStats(3, "2026-09-20")
	for _, body := range []string{
		``, `[]`, `not json`, `{}`, `{"from":"2026-09-20"}`, `{"to":"2026-09-20"}`, `{"from":"2026-09-21","to":"2026-09-20"}`,
		`{"from":"20260920","to":"20260921"}`, `{"from":"2026-13-01","to":"2026-13-02"}`, `{"all":true,"from":"2026-09-20","confirm":"DELETE ALL"}`,
		`{"from":"2026-09-20","to":"2026-09-20","bogus":1}`, `{"from":5,"to":6}`,
	} {
		code, out := h.del(body)
		require.Equal(t, 400, code, body)
		require.Equal(t, "bad_request", out["error"], body)
	}
	require.Equal(t, 3, h.count("SELECT count(*) FROM stats_events"))
}

// Data controls work with recording off.
func TestStatsExportAndDeleteWhileOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.bulkStats(5, "2026-09-20")
	h.setSetting("stats.enabled", "false")
	require.Len(t, readCSV(t, h.export("").Body.String()), 6)
	require.Equal(t, 200, h.export("?format=json").Code)
	require.Equal(t, 200, h.export("?format=jsonl").Code)
	code, out := h.del(`{"from":"2026-09-20","to":"2026-09-20","dry_run":true}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 5, num(out["count"]))
	code, out = h.del(`{"from":"2026-09-20","to":"2026-09-20"}`)
	require.Equal(t, 200, code)
	require.EqualValues(t, 5, num(out["deleted"]))
	require.Equal(t, 0, h.count("SELECT count(*) FROM stats_events"))
	// The summary export summarises the stored rows and says recording is off; the summary
	// endpoint itself keeps its empty off shape.
	h.bulkStats(4, "2026-09-20")
	rec := h.export("?content=summary")
	require.Equal(t, 200, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Equal(t, false, out["recording_enabled"])
	require.Equal(t, false, out["enabled"])
	require.EqualValues(t, 4, num(out["totals"].(map[string]any)["opens"]))
	_, sum := h.summary(nil, "?range=all")
	require.EqualValues(t, 0, num(sum["totals"].(map[string]any)["opens"]))
}

// TestStatsExportPerf times a raw CSV export of a million rows through the handler; skipped unless
// KIPPLE_PERF=1. It discards the body, so it measures the read, encode and write path, not a disk.
func TestStatsExportPerf(t *testing.T) {
	t.Parallel()
	if testing.Short() || os.Getenv("KIPPLE_PERF") == "" {
		t.Skip("seeds a million stats rows; set KIPPLE_PERF=1")
	}
	h := newHarness(t)
	const n = 1_000_000
	t0 := time.Now()
	for base := 0; base < n; base += 100_000 { // chunks, each inside the 10 s write deadline
		require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `WITH RECURSIVE c(i) AS (SELECT ?1 + 1 UNION ALL SELECT i+1 FROM c WHERE i < ?1 + ?2)
			INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
			folder_id, folder_name, item_title, item_url, value, session_key)
			SELECT 1780000000 + i, date('2026-01-01', '+' || (i % 300) || ' days'), i % 24, i % 7,
			 CASE WHEN i % 5 = 0 THEN 'open' ELSE 'read_time' END, 'web', i % 500000, i % 200, 'Feed ' || (i % 200), 1, 'Uncategorized',
			 'An article title number ' || i, 'https://example.com/a/' || i, CASE WHEN i % 5 = 0 THEN NULL ELSE 5 + i % 55 END,
			 printf('%016x%016x', i, i * 7919) FROM c`, base, 100_000)
			return err
		}))
	}
	t.Logf("seeded in %v", time.Since(t0))
	// The start-of-export row count: the old walk of the wide rows against the index-only counts.
	for i := 0; i < 3; i++ {
		t1 := time.Now()
		var old int
		require.NoError(t, h.db.Reader().QueryRow(`SELECT COUNT(*) FROM stats_events WHERE id <= ? AND local_date BETWEEN ? AND ? AND inferred <= ?`,
			int64(1)<<40, "0000-01-01", "9999-12-31", 0).Scan(&old))
		dOld := time.Since(t1)
		t1 = time.Now()
		got, err := store.StatsCountUpTo(context.Background(), h.db.Reader(), "", "", false, int64(1)<<40)
		require.NoError(t, err)
		require.Equal(t, old, got)
		t.Logf("count of %d rows: table walk %v, index-only %v", got, dOld, time.Since(t1))
	}
	cc := h.login()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var peak uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
			}
		}
	}()
	for _, format := range []string{"csv", "jsonl"} {
		r := httptest.NewRequest("GET", "/api/stats/export?format="+format, nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.AddCookie(&http.Cookie{Name: cc.Name, Value: cc.Value})
		cw := &countingWriter{ResponseRecorder: httptest.NewRecorder()}
		t1 := time.Now()
		h.mux.ServeHTTP(cw, r)
		d := time.Since(t1)
		t.Logf("%s: %d rows, %.1f MB in %v (%.0f rows/s)", format, n, float64(cw.n)/1e6, d, float64(n)/d.Seconds())
		require.Equal(t, 200, cw.Code)
	}
	close(stop)
	runtime.ReadMemStats(&after)
	t.Logf("heap before %.1f MB, peak sampled %.1f MB", float64(before.HeapAlloc)/1e6, float64(peak)/1e6)
	t2 := time.Now()
	code, out := h.del(`{"all":true,"confirm":"DELETE ALL"}`)
	require.Equal(t, 200, code)
	t.Logf("delete all: %v rows in %v", out["deleted"], time.Since(t2))
}

// countingWriter counts and discards the body.
type countingWriter struct {
	*httptest.ResponseRecorder
	n int
}

func (c *countingWriter) Write(b []byte) (int, error) { c.n += len(b); return len(b), nil }
