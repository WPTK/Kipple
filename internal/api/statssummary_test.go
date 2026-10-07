package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stat inserts one stats row with an explicit local date (noon local, ts derived from it).
func (h *harness) stat(kind, date string, hour int, item, feed int64, session string, value any, mod ...string) {
	h.t.Helper()
	d, err := time.ParseInLocation("2006-01-02", date, time.UTC)
	require.NoError(h.t, err)
	ts := d.Unix() + int64(hour)*3600 + 5*3600
	title := "Feed"
	if len(mod) > 0 {
		title = mod[0]
	}
	var sk any
	if session != "" {
		sk = session
	}
	h.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
		folder_id, folder_name, item_title, value, session_key) VALUES (?,?,?,?,?, 'web', ?,?,?, 1, 'Uncategorized', ?, ?, ?)`,
		ts, date, hour, int(d.Weekday()), kind, item, feed, title, "Title "+sid(item), value, sk)
}

func (h *harness) summary(c interface{}, q string) (int, map[string]any) {
	h.t.Helper()
	cc := h.login()
	rec := h.do("GET", "/api/stats/summary"+q, "", withCookie(cc))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func num(v any) float64 { f, _ := v.(float64); return f }

func (h *harness) setSetting(key, jsonVal string) {
	h.exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, jsonVal)
}

func TestStatsSummaryDefinitions(t *testing.T) {
	h := newHarness(t)
	f1 := h.addFeed("Alpha", 0)
	f2 := h.addFeed("Beta", 0)
	// A legacy open: before the first read_time or scroll event. Counts as read.
	h.stat("open", "2026-09-01", 9, 1, f1, "old1", nil)
	h.stat("open", "2026-09-20", 8, 2, f1, "s9", nil)
	h.stat("read_time", "2026-09-20", 8, 2, f1, "s9", 9) // 9 s: not a read
	h.stat("open", "2026-09-20", 9, 3, f1, "s10", nil)
	h.stat("read_time", "2026-09-20", 9, 3, f1, "s10", 10) // 10 s: a read
	h.stat("open", "2026-09-21", 10, 4, f2, "sc24", nil)
	h.stat("scroll", "2026-09-21", 10, 4, f2, "sc24", 24) // 24: not a read
	h.stat("open", "2026-09-21", 11, 5, f2, "sc25", nil)
	h.stat("scroll", "2026-09-21", 11, 5, f2, "sc25", 25) // 25 with 3 s: a read
	h.stat("read_time", "2026-09-21", 11, 5, f2, "sc25", 3)
	h.stat("star", "2026-09-21", 11, 5, f2, "", nil)
	h.stat("open_original", "2026-09-21", 11, 5, f2, "", nil)

	code, out := h.summary(nil, "?range=month")
	require.Equal(t, 200, code, "%v", out)
	require.Equal(t, true, out["enabled"])
	require.Equal(t, "America/New_York", out["tz"])
	require.Equal(t, "sunday", out["week_start"])
	require.Equal(t, "2026-09-01", out["first_event_date"])
	require.Equal(t, "2026-09-01", out["covered_from"])
	require.NotNil(t, out["timed_from"])
	rg := out["range"].(map[string]any)
	require.Equal(t, "month", rg["key"])
	require.Equal(t, "2026-08-26", rg["from"])
	require.Equal(t, "2026-09-24", rg["to"])
	require.EqualValues(t, 30, num(rg["days"]))
	tot := out["totals"].(map[string]any)
	require.EqualValues(t, 3, num(tot["items_read"]))
	require.EqualValues(t, 5, num(tot["opens"]))
	require.EqualValues(t, 22, num(tot["active_seconds"]))
	require.EqualValues(t, 3, num(tot["days_active"]))
	require.EqualValues(t, 1, num(tot["legacy_opens"]))

	daily := out["daily"].([]any)
	require.Len(t, daily, 30)
	require.Equal(t, "2026-08-26", daily[0].(map[string]any)["date"])
	require.Equal(t, "2026-09-24", daily[29].(map[string]any)["date"])
	byDate := map[string]map[string]any{}
	for _, d := range daily {
		m := d.(map[string]any)
		byDate[m["date"].(string)] = m
	}
	require.EqualValues(t, 1, num(byDate["2026-09-01"]["items_read"]))
	require.EqualValues(t, 1, num(byDate["2026-09-20"]["items_read"]))
	require.EqualValues(t, 19, num(byDate["2026-09-20"]["active_seconds"]))
	require.EqualValues(t, 1, num(byDate["2026-09-21"]["items_read"]))
	require.EqualValues(t, 3, num(byDate["2026-09-21"]["active_seconds"]))
	require.EqualValues(t, 0, num(byDate["2026-09-22"]["items_read"]))

	src := out["sources"].([]any)
	require.Len(t, src, 2)
	a, b := src[0].(map[string]any), src[1].(map[string]any)
	require.Equal(t, sid(f1), a["feed_id"])
	require.EqualValues(t, 2, num(a["items_read"]))
	require.EqualValues(t, 3, num(a["opens"]))
	require.EqualValues(t, 19, num(a["active_seconds"]))
	require.InDelta(t, 0.5, num(a["bounce_rate"]), 1e-9, "one of two post-sender opens bounced")
	require.InDelta(t, 0.0, num(a["open_original_rate"]), 1e-9)
	require.Equal(t, true, a["subscribed"])
	require.Equal(t, "Uncategorized", a["folder_name"])
	for k, v := range map[string]float64{"tracked_opens": 2, "bounces": 1, "items_opened": 3, "items_original": 0} {
		require.EqualValues(t, v, num(a[k]), k)
	}
	require.Equal(t, sid(f2), b["feed_id"])
	for k, v := range map[string]float64{"tracked_opens": 2, "bounces": 1, "items_opened": 2, "items_original": 1} {
		require.EqualValues(t, v, num(b[k]), k)
	}
	require.EqualValues(t, 1, num(b["items_read"]))
	require.EqualValues(t, 1, num(b["stars"]))
	require.InDelta(t, 0.5, num(b["open_original_rate"]), 1e-9)
	require.InDelta(t, 0.5, num(b["bounce_rate"]), 1e-9)

	bh := out["behavior"].(map[string]any)
	require.EqualValues(t, 0, num(bh["busiest_weekday"].(map[string]any)["weekday"]), "2026-09-20 is a Sunday")
	require.EqualValues(t, 19, num(bh["busiest_weekday"].(map[string]any)["active_seconds"]))
	require.EqualValues(t, 9, num(bh["busiest_hour"].(map[string]any)["hour"]))
	require.InDelta(t, 6.5, num(bh["avg_read_seconds"]), 1e-9, "(10 + 3) / 2")
	lr := bh["longest_read"].(map[string]any)
	require.Equal(t, "Title 3", lr["title"])
	require.EqualValues(t, 10, num(lr["seconds"]))
	require.Equal(t, "2026-09-20", lr["date"])
	hm := out["heatmap"].([]any)
	require.NotEmpty(t, hm)
	for _, c := range hm {
		m := c.(map[string]any)
		require.True(t, num(m["active_seconds"]) > 0 || num(m["opens"]) > 0, "only non-zero cells")
	}
}

func TestStatsSummaryWeekRanges(t *testing.T) {
	h := newHarness(t) // 2026-09-24 is a Thursday
	for _, tc := range []struct {
		ws, from string
		days     float64
	}{{"sunday", "2026-09-20", 5}, {"monday", "2026-09-21", 4}} {
		h.setSetting("stats.week_start", `"`+tc.ws+`"`)
		_, out := h.summary(nil, "?range=week")
		rg := out["range"].(map[string]any)
		require.Equal(t, tc.from, rg["from"], tc.ws)
		require.Equal(t, "2026-09-24", rg["to"])
		require.Equal(t, tc.days, num(rg["days"]))
		require.Equal(t, tc.ws, out["week_start"])
	}
}

func TestStatsSummaryYearAcrossDST(t *testing.T) {
	h := newHarness(t)
	_, out := h.summary(nil, "?range=year")
	rg := out["range"].(map[string]any)
	require.EqualValues(t, 365, num(rg["days"]))
	require.Equal(t, "2025-09-25", rg["from"])
	daily := out["daily"].([]any)
	require.Len(t, daily, 365)
	seen := map[string]bool{}
	for _, d := range daily {
		ds := d.(map[string]any)["date"].(string)
		require.False(t, seen[ds], ds)
		seen[ds] = true
	}
	require.True(t, seen["2026-03-08"] && seen["2025-11-02"], "the DST change days appear once each")
	// A zone change moves "today" but never breaks the day count.
	h.setSetting("tz", `"Pacific/Auckland"`) // 2026-09-25 00:00 local at the fixed clock
	_, out = h.summary(nil, "?range=month")
	require.Equal(t, "2026-09-25", out["range"].(map[string]any)["to"])
	require.EqualValues(t, 30, num(out["range"].(map[string]any)["days"]))
}

func TestStatsSummaryAllAndCustom(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	_, out := h.summary(nil, "?range=all")
	require.Nil(t, out["first_event_date"])
	require.EqualValues(t, 1, num(out["range"].(map[string]any)["days"]), "no events: just today")
	h.stat("open", "2026-09-10", 9, 1, f, "a", nil)
	_, out = h.summary(nil, "?range=all")
	rg := out["range"].(map[string]any)
	require.Equal(t, "2026-09-10", rg["from"])
	require.EqualValues(t, 15, num(rg["days"]))
	code, out := h.summary(nil, "?from=2026-09-10&to=2026-09-11")
	require.Equal(t, 200, code)
	require.Equal(t, "custom", out["range"].(map[string]any)["key"])
	require.EqualValues(t, 1, num(out["totals"].(map[string]any)["opens"]))
	for _, q := range []string{"?from=2026-09-11&to=2026-09-10", "?from=2026-9-1&to=2026-09-10", "?from=2026-09-10", "?to=2026-09-10",
		"?from=2026-13-01&to=2026-13-02", "?range=decade", "?from=1900-01-01&to=2026-09-10"} {
		code, _ := h.summary(nil, q)
		require.Equal(t, 400, code, q)
	}
}

func TestStatsSummaryOff(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	h.stat("open", "2026-09-20", 9, 1, f, "a", nil)
	h.setSetting("stats.enabled", "false")
	code, out := h.summary(nil, "")
	require.Equal(t, 200, code)
	require.Equal(t, false, out["enabled"])
	require.Equal(t, "America/New_York", out["tz"])
	require.Equal(t, "sunday", out["week_start"])
	require.Empty(t, out["daily"])
	require.Empty(t, out["sources"])
	require.Empty(t, out["never_opened"])
	require.Empty(t, out["heatmap"])
}

func TestStatsSummaryStreaks(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	n := int64(0)
	read := func(d string) { n++; h.stat("open", d, 9, n, f, "", nil) } // legacy opens count as reads
	for _, d := range []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-22", "2026-09-23"} {
		read(d)
	}
	_, out := h.summary(nil, "?range=week") // streaks are not range-limited
	st := out["streaks"].(map[string]any)
	require.EqualValues(t, 2, num(st["current"]), "yesterday counts")
	require.EqualValues(t, 3, num(st["longest"]))
	require.Equal(t, "2026-09-03", st["longest_end"])
	read("2026-09-24")
	_, out = h.summary(nil, "?range=week")
	st = out["streaks"].(map[string]any)
	require.EqualValues(t, 3, num(st["current"]))
	require.EqualValues(t, 3, num(st["longest"]))
	require.Equal(t, "2026-09-24", st["longest_end"], "a tie goes to the latest run")

	h2 := newHarness(t)
	f = h2.addFeed("A", 0)
	h2.stat("open", "2026-09-20", 9, 1, f, "", nil)
	_, out = h2.summary(nil, "")
	st = out["streaks"].(map[string]any)
	require.EqualValues(t, 0, num(st["current"]), "the last read was three days ago")
	require.EqualValues(t, 1, num(st["longest"]))
}

func TestStatsSummaryNeverOpenedAndSnapshots(t *testing.T) {
	h := newHarness(t)
	fo := h.addFolder("News")
	opened := h.addFeed("Opened", 0)
	quiet := h.addFeed("Quiet", fo)
	arch := h.addFeed("Archive", 0)
	h.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive', retention = 0 WHERE id = ?", arch)
	h.exec("UPDATE feeds SET created_at = 1700000000 WHERE id = ?", quiet)
	h.exec("UPDATE feeds SET created_at = 1700000000 WHERE id = ?", opened)
	fresh := h.addFeed("Fresh", 0)
	h.exec("UPDATE feeds SET created_at = ? WHERE id = ?", time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC).Unix(), fresh)
	h.stat("open", "2026-09-20", 9, 1, opened, "a", nil, "Old Name")
	h.stat("open", "2026-09-22", 9, 2, opened, "b", nil, "New Name")
	_, out := h.summary(nil, "?range=month")
	nv := out["never_opened"].([]any)
	require.Len(t, nv, 2)
	first, second := nv[0].(map[string]any), nv[1].(map[string]any)
	require.Equal(t, sid(quiet), first["feed_id"], "oldest subscription first")
	require.Equal(t, "News", first["folder_name"])
	require.Equal(t, "2023-11-14", first["subscribed_on"])
	require.Equal(t, sid(fresh), second["feed_id"])
	require.Equal(t, "New Name", out["sources"].([]any)[0].(map[string]any)["feed_title"], "latest snapshot wins")
	// Outside the range the opened feed becomes never-opened too.
	_, out = h.summary(nil, "?from=2026-09-23&to=2026-09-24")
	require.Len(t, out["never_opened"], 3)
}

// 3660 days, both ends included, is the longest custom range: to minus from is at most 3659.
func TestStatsSummaryCustomRangeCap(t *testing.T) {
	h := newHarness(t)
	from := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		diff int
		code int
	}{{3659, 200}, {3660, 400}, {3661, 400}} {
		to := from.AddDate(0, 0, tc.diff)
		code, out := h.summary(nil, "?from="+from.Format("2006-01-02")+"&to="+to.Format("2006-01-02"))
		require.Equal(t, tc.code, code, "to-from = %d days", tc.diff)
		if code == 200 {
			require.EqualValues(t, 3660, num(out["range"].(map[string]any)["days"]))
		}
	}
}

// A feed subscribed after the end of the range was not there to be opened.
func TestStatsSummaryNeverOpenedSkipsLaterSubscriptions(t *testing.T) {
	h := newHarness(t)
	early := h.addFeed("Early", 0)
	late := h.addFeed("Late", 0)
	h.exec("UPDATE feeds SET created_at = ? WHERE id = ?", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Unix(), early)
	h.exec("UPDATE feeds SET created_at = ? WHERE id = ?", time.Date(2026, 9, 25, 3, 0, 0, 0, time.UTC).Unix(), late) // 2026-09-24 23:00 in New York
	ids := func(q string) []string {
		_, out := h.summary(nil, q)
		var r []string
		for _, n := range out["never_opened"].([]any) {
			r = append(r, n.(map[string]any)["feed_id"].(string))
		}
		return r
	}
	require.Equal(t, []string{sid(early), sid(late)}, ids("?from=2026-09-20&to=2026-09-24"), "the last local day of the range counts")
	require.Equal(t, []string{sid(early)}, ids("?from=2026-09-10&to=2026-09-23"))
	require.Empty(t, ids("?from=2026-08-01&to=2026-08-31"))
}

func TestStatsSummarySourceTimedFieldsAndTruncationFlag(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	h.stat("open", "2026-09-19", 9, 1, f, "old", nil)
	h.stat("open", "2026-09-20", 9, 1, f, "a", nil)
	h.stat("read_time", "2026-09-20", 9, 1, f, "a", 30)
	h.stat("open", "2026-09-20", 9, 2, f, "b", nil)
	h.stat("read_time", "2026-09-20", 9, 2, f, "b", 10)
	_, out := h.summary(nil, "?range=month")
	require.Equal(t, false, out["sources_truncated"])
	s := out["sources"].([]any)[0].(map[string]any)
	require.EqualValues(t, 40, num(s["timed_seconds"]))
	require.EqualValues(t, 2, num(s["timed_items"]))
	require.InDelta(t, 20, num(s["avg_read_seconds"]), 1e-9)
}

// Only one summary is computed at a time; a waiter whose request is cancelled returns without querying.
func TestStatsSummaryGateSerializes(t *testing.T) {
	h := newHarness(t)
	cc := h.login()
	h.srv.statsGate <- struct{}{} // a computation in flight

	done := make(chan int, 1)
	go func() { done <- h.do("GET", "/api/stats/summary", "", withCookie(cc)).Code }()
	select {
	case <-done:
		t.Fatal("the second request must wait for the first")
	case <-time.After(150 * time.Millisecond):
	}
	<-h.srv.statsGate // the first finishes
	select {
	case code := <-done:
		require.Equal(t, 200, code)
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never ran")
	}

	h.srv.statsGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan struct{})
	go func() {
		defer close(cancelled)
		h.do("GET", "/api/stats/summary", "", withCookie(cc), func(r *http.Request) { *r = *r.WithContext(ctx) })
	}()
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled waiter must return")
	}
	<-h.srv.statsGate
}
