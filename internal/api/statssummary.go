package api

import (
	"net/http"
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
	now := s.now()
	p := store.StatsSummaryParams{Now: now, IncludeInferred: qv.Get("include_inferred") == "1"}
	today := now.In(loc)
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }
	td := day(today)

	fromQ, toQ := qv.Get("from"), qv.Get("to")
	switch {
	case fromQ != "" || toQ != "":
		f, e1 := time.Parse("2006-01-02", fromQ)
		t, e2 := time.Parse("2006-01-02", toQ)
		if e1 != nil || e2 != nil || t.Before(f) || t.Sub(f) > (maxStatsRangeDays-1)*24*time.Hour {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		p.Key, p.From, p.To = "custom", fromQ, toQ
	default:
		p.Key = qv.Get("range")
		p.To = td.Format("2006-01-02")
		switch p.Key {
		case "":
			p.Key = "month"
			fallthrough
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
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	out, err := store.StatsSummaryFor(ctx, rd, p)
	if err != nil {
		s.serverError(w, "stats summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
