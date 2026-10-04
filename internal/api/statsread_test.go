package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/WPTK/kipple/internal/store"
	"github.com/stretchr/testify/require"
)

// The read rule end to end (issue #120): opens from POST /open, time and scroll from POST
// /api/stats/events, then the summary, the summary export and the streaks agree.
func TestStatsReadRuleThroughIngest(t *testing.T) {
	h := newHarness(t)
	c := h.login()
	f := h.addFeed("A", 0)
	// A timed row long before today, so none of the sessions below is a legacy open.
	h.stat("read_time", "2026-08-01", 9, 999, f, "warmup", 5)

	type tc struct {
		name   string
		secs   []float64
		scroll float64 // < 0: none sent
		read   bool
	}
	cases := []tc{
		{"2.9 s + 100 %", []float64{2.9}, 100, false}, // truncated to 2 at ingest
		{"3 s + 25 %", []float64{3}, 25, true},
		{"10 s + 0 %", []float64{10}, 0, true},
		{"9 s + 24 %", []float64{9}, 24, false},
		{"flick, 100 % and no time", nil, 100, false},
	}
	reads := 0
	for _, x := range cases {
		id := h.addItem(f, seedItem{})
		_, opened, _ := h.api(c, "POST", "/api/items/"+sid(id)+"/open", `{"via":"tap"}`)
		key := opened["session_key"].(string)
		h.clk.Advance(30 * time.Second)
		var evs []any
		for _, s := range x.secs {
			evs = append(evs, map[string]any{"kind": "read_time", "item_id": sid(id), "session_key": key, "value": s})
		}
		if x.scroll >= 0 {
			evs = append(evs, map[string]any{"kind": "scroll", "item_id": sid(id), "session_key": key, "value": x.scroll})
		}
		if len(evs) > 0 {
			code, _, _ := h.api(c, "POST", "/api/stats/events", jsonStr(map[string]any{"events": evs}))
			require.Equal(t, 204, code, x.name)
		}
		if x.read {
			reads++
		}
	}
	require.Equal(t, 2, h.count("SELECT value FROM stats_events WHERE kind = 'read_time' AND value < 3"), "2.9 is stored as 2")

	_, out := h.summary(nil, "?range=week")
	tot := out["totals"].(map[string]any)
	require.EqualValues(t, len(cases), num(tot["opens"]))
	require.EqualValues(t, reads, num(tot["items_read"]))
	require.EqualValues(t, 0, num(tot["legacy_opens"]))
	src := out["sources"].([]any)[0].(map[string]any)
	require.EqualValues(t, len(cases)-reads, num(src["bounces"]))
	require.EqualValues(t, len(cases), num(src["tracked_opens"]))
	st := out["streaks"].(map[string]any)
	require.EqualValues(t, 1, num(st["current"]), "today has a read")

	// The summary export computes exactly the same numbers.
	rec := h.export("?content=summary&range=week")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var exp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &exp))
	require.Equal(t, out["totals"], exp["totals"])
	require.Equal(t, out["streaks"], exp["streaks"])
	require.Equal(t, out["sources"], exp["sources"])
}

// Only bounces today: the streak does not include today, and the export agrees.
func TestStatsReadScrollOnlyIsNoStreakDay(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	h.stat("read_time", "2026-08-01", 9, 999, f, "warmup", 5)
	h.stat("open", "2026-09-23", 9, 1, f, "y", nil) // yesterday: 3 s + 25 %, a read
	h.stat("read_time", "2026-09-23", 9, 1, f, "y", 3)
	h.stat("scroll", "2026-09-23", 9, 1, f, "y", 25)
	h.stat("open", "2026-09-24", 9, 2, f, "t", nil) // today: 2 s + 100 %, a bounce
	h.stat("read_time", "2026-09-24", 9, 2, f, "t", 2)
	h.stat("scroll", "2026-09-24", 9, 2, f, "t", 100)
	_, out := h.summary(nil, "?range=week")
	st := out["streaks"].(map[string]any)
	require.EqualValues(t, 1, num(st["current"]), "yesterday only")
	require.Equal(t, "2026-09-23", st["longest_end"])
	require.EqualValues(t, 1, num(out["totals"].(map[string]any)["days_active"]))

	var exp map[string]any
	require.NoError(t, json.Unmarshal(h.export("?content=summary&range=week").Body.Bytes(), &exp))
	require.Equal(t, out["streaks"], exp["streaks"])
	require.Equal(t, out["totals"], exp["totals"])
}

// Legacy opens are reported in totals.legacy_opens and counted as reads.
func TestStatsReadLegacyOpensReported(t *testing.T) {
	h := newHarness(t)
	f := h.addFeed("A", 0)
	h.stat("open", "2026-09-20", 9, 1, f, "a", nil) // no timed row at all: every open is legacy
	h.stat("open", "2026-09-21", 9, 2, f, "b", nil)
	_, out := h.summary(nil, "?range=week")
	tot := out["totals"].(map[string]any)
	require.EqualValues(t, 2, num(tot["legacy_opens"]))
	require.EqualValues(t, 2, num(tot["items_read"]))
	require.Nil(t, out["sources"].([]any)[0].(map[string]any)["bounce_rate"], "no tracked opens")
}

// The dictionary states the rule with the store's thresholds, in JSON, in Markdown and in the
// dictionary embedded in an export, and says legacy opens are unverified.
func TestStatsDictionaryReadRule(t *testing.T) {
	h := newHarness(t)
	cc := h.login()
	rule := fmt.Sprintf("at least %d seconds of read_time in total, or a scroll value of at least %d together with at least %d seconds of read_time",
		store.StatsReadSeconds, store.StatsReadScroll, store.StatsReadScrollSeconds)
	require.Contains(t, rule, "at least 10 seconds of read_time in total, or a scroll value of at least 25 together with at least 3 seconds")

	rec := h.do("GET", "/api/stats/dictionary", "", withCookie(cc))
	require.Equal(t, 200, rec.Code)
	var d struct {
		Concepts      map[string]string
		SummaryFields map[string]string `json:"summary_fields"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &d))
	require.Contains(t, d.Concepts["read"], rule)
	require.Contains(t, d.Concepts["read"], "A scroll alone is never a read")
	require.Contains(t, d.Concepts["legacy_open"], "unknowable")
	require.Contains(t, d.Concepts["legacy_open"], "overstate")
	require.Contains(t, d.Concepts["bounce"], "under 10 seconds of read_time, and either under 3 seconds or a scroll under 25")
	require.Contains(t, d.SummaryFields, "totals.legacy_opens")

	md := h.do("GET", "/api/stats/dictionary?format=md", "", withCookie(cc)).Body.String()
	require.Contains(t, md, "- **read.** An open counts as a read when its session has "+rule)
	require.Contains(t, md, "- **legacy open.** ")
	require.Contains(t, md, "`totals.legacy_opens`")
	require.NotContains(t, md, "or a scroll value of at least 25.", "the old rule text is gone")

	var exp struct {
		Dictionary struct{ Concepts map[string]string }
	}
	require.NoError(t, json.Unmarshal(h.export("?content=summary").Body.Bytes(), &exp))
	require.Equal(t, d.Concepts["read"], exp.Dictionary.Concepts["read"])
	require.True(t, strings.Contains(exp.Dictionary.Concepts["legacy_open"], "overstate"))
}
