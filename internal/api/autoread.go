package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// Auto-read (backend plan F5, design 7.1d): the preview and the explicit catch-up run. The nightly
// step lives in internal/maint; both use store.RunAutoRead.

// runKindAutoRead is the `kind` of a catch-up run in run.* events and bootstrap `runs`.
const runKindAutoRead = "auto_read"

// autoReadConfirmAbove is the preview total above which a run needs `confirm:true`.
const autoReadConfirmAbove = 100

// autoReadRun is the run the API reports and publishes.
type autoReadRun struct {
	ID       int64  `json:"id,string"`
	Kind     string `json:"kind"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Changed  int    `json:"changed"`
	NewItems int    `json:"new_items"`
	Errors   int    `json:"errors"`
}

type autoReadState struct {
	mu  sync.Mutex
	run *autoReadRun // the active run, nil when idle
}

// autoReadStatus is the active catch-up run for bootstrap `runs`, or nil.
func (s *Server) autoReadStatus() *autoReadRun {
	s.autoRead.mu.Lock()
	defer s.autoRead.mu.Unlock()
	if s.autoRead.run == nil {
		return nil
	}
	c := *s.autoRead.run
	return &c
}

// PublishAutoRead publishes one committed batch of auto-read marks: items.state
// {ids, read:true, source:"auto_read"} (a `resync` above 500 ids) and a coalesced `counts`.
// The nightly job calls it (internal/maint OnAutoRead).
func (s *Server) PublishAutoRead(res store.StateResult) {
	s.publishState(res, map[string]any{"read": true, "source": "auto_read"})
}

// autoReadReq is the body of preview and run: feed_id limits it to one feed, days previews or runs
// with that value instead of the stored one (0 = off) without saving it.
type autoReadReq struct {
	feedID  int64
	days    *int
	confirm bool
}

func (s *Server) readAutoReadReq(w http.ResponseWriter, r *http.Request, allowConfirm bool) (autoReadReq, bool) {
	var req autoReadReq
	keys := []string{"feed_id", "days"}
	if allowConfirm {
		keys = append(keys, "confirm")
	}
	var m map[string]json.RawMessage
	if !decodeBody(w, r, &m, true) {
		return req, false
	}
	for k := range m {
		if !slices.Contains(keys, k) {
			writeErrorMsg(w, http.StatusBadRequest, "unknown_field", "unknown field "+k)
			return req, false
		}
	}
	bad := func(msg string) (autoReadReq, bool) {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", msg)
		return req, false
	}
	if raw, ok := m["feed_id"]; ok && !isNull(raw) {
		id, ok := parseID(raw)
		if !ok {
			return bad("feed_id must be a feed id")
		}
		req.feedID = id
	}
	if raw, ok := m["days"]; ok && !isNull(raw) {
		n, ok := rawInt(raw)
		if !ok || n < 0 || n > 365 {
			return bad("days must be null or 0 to 365")
		}
		v := int(n)
		req.days = &v
	}
	if raw, ok := m["confirm"]; ok && !isNull(raw) {
		b, ok := rawBool(raw)
		if !ok {
			return bad("confirm must be true or false")
		}
		req.confirm = b
	}
	return req, true
}

// previewBody is the preview answer: what a catch-up run would mark read right now.
func (s *Server) autoReadPreview(ctx context.Context, req autoReadReq) (store.AutoReadPreview, error) {
	return s.db.PreviewAutoRead(ctx, s.now(), req.feedID, req.days)
}

// ---- POST /api/library/auto-read/preview ----

func (s *Server) previewAutoRead(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readAutoReadReq(w, r, false)
	if !ok {
		return
	}
	if !s.autoReadFeedOK(w, r.Context(), req.feedID) {
		return
	}
	pv, err := s.autoReadPreview(r.Context(), req)
	if err != nil {
		s.serverError(w, "auto-read preview", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": pv.Total, "feeds": pv.Feeds, "global_days": s.db.GlobalAutoReadDays(r.Context()),
		"confirm_above": autoReadConfirmAbove,
	})
}

func (s *Server) autoReadFeedOK(w http.ResponseWriter, ctx context.Context, feedID int64) bool {
	if feedID == 0 {
		return true
	}
	var n int
	if err := s.db.Reader().QueryRowContext(ctx, "SELECT count(*) FROM feeds WHERE id = ? AND disabled_reason IS NOT 'archive'", feedID).Scan(&n); err != nil {
		s.serverError(w, "auto-read feed", err)
		return false
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return false
	}
	return true
}

// ---- POST /api/library/auto-read/run ----

var errAutoReadBusy = errors.New("api: an auto-read run is already running")

// runAutoReadRoute starts the catch-up: it marks read every unread, unstarred, unmuted article
// older than the feed's threshold, no matter when it crossed it. Over 100 articles it needs
// `confirm:true` (409 confirm_required with the count), so switching the feature on never marks a
// backlog silently. One run at a time (409 busy); progress goes out as run.* events.
func (s *Server) runAutoReadRoute(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readAutoReadReq(w, r, true)
	if !ok {
		return
	}
	if !s.autoReadFeedOK(w, r.Context(), req.feedID) {
		return
	}
	pv, err := s.autoReadPreview(r.Context(), req)
	if err != nil {
		s.serverError(w, "auto-read run", err)
		return
	}
	if pv.Total > autoReadConfirmAbove && !req.confirm {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "confirm_required", "total": pv.Total, "confirm_above": autoReadConfirmAbove,
			"message": "this marks more than 100 articles read; send confirm:true to go ahead"})
		return
	}
	run, err := s.startAutoRead(req, pv.Total)
	if errors.Is(err, errAutoReadBusy) {
		writeError(w, http.StatusConflict, "busy")
		return
	}
	if err != nil {
		s.serverError(w, "auto-read run", err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) startAutoRead(req autoReadReq, total int) (*autoReadRun, error) {
	s.autoRead.mu.Lock()
	defer s.autoRead.mu.Unlock()
	if s.autoRead.run != nil {
		return nil, errAutoReadBusy
	}
	if s.apply.ctx == nil || s.apply.ctx.Err() != nil {
		return nil, errAutoReadBusy // shutting down
	}
	run := &autoReadRun{ID: s.now().UnixMicro(), Kind: runKindAutoRead, Total: total}
	s.autoRead.run = run
	snapshot := *run
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("run.start", map[string]any{"run_id": idStr(run.ID), "kind": run.Kind, "total": total})
	}
	s.apply.wg.Add(1)
	go s.runAutoRead(s.apply.ctx, run, req, s.now())
	return &snapshot, nil
}

func (s *Server) runAutoRead(ctx context.Context, run *autoReadRun, req autoReadReq, now time.Time) {
	defer s.apply.wg.Done()
	var lastProgress time.Time
	res, err := s.db.RunAutoRead(ctx, store.AutoReadOptions{
		Now: now, FeedID: req.feedID, Days: req.days, Pause: 25 * time.Millisecond,
		OnBatch: s.PublishAutoRead,
		Progress: func(done int) {
			s.autoRead.mu.Lock()
			run.Done, run.Changed = done, done
			run.Total = max(run.Total, done)
			total := run.Total
			s.autoRead.mu.Unlock()
			if s.opt.Hub != nil && time.Since(lastProgress) >= 500*time.Millisecond {
				lastProgress = time.Now()
				s.opt.Hub.Publish("run.progress", map[string]any{"run_id": idStr(run.ID), "done": done, "total": total,
					"new_items": 0, "errors": 0, "changed": done})
			}
		},
	})
	nErr := 0
	if err != nil {
		nErr = 1
		if ctx.Err() == nil {
			s.log.Error("api: auto-read run", "err", err)
		}
	}
	s.autoRead.mu.Lock()
	s.autoRead.run = nil
	s.autoRead.mu.Unlock()
	if s.opt.Hub != nil {
		ev := map[string]any{"run_id": idStr(run.ID), "kind": runKindAutoRead, "new_items": 0, "errors": nErr,
			"changed": res.Items, "scanned": res.Items, "ledger": res.Ledger}
		if err != nil {
			ev["error"] = "auto_read_failed"
		}
		s.opt.Hub.Publish("run.done", ev)
	}
	s.noteCounts()
}
