package api

import (
	"net/http"
	"net/url"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// maxStatsRangeDays is the longest custom range in days, both ends included (to minus from is at most one less).
const maxStatsRangeDays = 3660

// statsSummary answers GET /api/stats/summary (design §8): read-only, reader pool only, and no
// read is held while the response is written.
func (s *Server) statsSummary(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	ctx := r.Context()
	rd := s.db.Reader()
	_, loc, _, weekStart, err := store.StatsSettings(ctx, rd)
	if err != nil {
		s.serverError(w, "stats summary", err)
		return
	}
	p, ok := statsRangeParams(qv, s.now(), loc, weekStart, "month")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	// One computation at a time process-wide, so two tabs or a refetch during a range change never ask
	// the four-connection reader pool for more than three connections. A waiter that goes away leaves.
	select {
	case s.statsGate <- struct{}{}:
		defer func() { <-s.statsGate }()
	case <-ctx.Done():
		return
	}
	out, err := store.StatsSummaryFor(ctx, rd, p)
	if err != nil {
		s.serverError(w, "stats summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// statsRangeParams resolves the range query (range=week|month|year|all, or from= and to= local
// dates) shared by the summary and the export. defKey is the range used when the query names none.
// ok is false for an invalid range. Key "all" leaves From and To empty for the caller to resolve.
func statsRangeParams(qv url.Values, now time.Time, loc *time.Location, weekStart, defKey string) (p store.StatsSummaryParams, ok bool) {
	p = store.StatsSummaryParams{Now: now, IncludeInferred: qv.Get("include_inferred") == "1"}
	td := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, time.UTC)
	fromQ, toQ := qv.Get("from"), qv.Get("to")
	if fromQ != "" || toQ != "" {
		f, e1 := time.Parse("2006-01-02", fromQ)
		t, e2 := time.Parse("2006-01-02", toQ)
		if e1 != nil || e2 != nil || t.Before(f) || t.Sub(f) > (maxStatsRangeDays-1)*24*time.Hour {
			return p, false
		}
		p.Key, p.From, p.To = "custom", fromQ, toQ
		return p, true
	}
	p.Key = qv.Get("range")
	if p.Key == "" {
		p.Key = defKey
	}
	p.To = td.Format("2006-01-02")
	switch p.Key {
	case "month":
		p.From = td.AddDate(0, 0, -29).Format("2006-01-02")
	case "week":
		off := int(td.Weekday())
		if weekStart == "monday" {
			off = (off + 6) % 7
		}
		p.From = td.AddDate(0, 0, -off).Format("2006-01-02")
	case "year":
		p.From = td.AddDate(0, 0, -364).Format("2006-01-02")
	case "all":
	default:
		return p, false
	}
	return p, true
}
