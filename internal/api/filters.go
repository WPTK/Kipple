package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/filter"
	"github.com/WPTK/kipple/internal/store"
)

// The filters API (backend-additions-round2 1.7, design 7.1): CRUD, the dry-run preview, the
// retroactive apply as a run, and deletion with un-muting. All of it sits behind authed, so it
// has the session and same-origin rules of every other state-changing route.

// previewBudget bounds one preview scan.
const previewBudget = 5 * time.Second

// runKindFilterApply is the `kind` of an apply run in run.* events and bootstrap `runs`.
const runKindFilterApply = "filter_apply"

// filterJSON is a filter with the number of items it currently mutes.
type filterJSON struct {
	store.Filter
	MutedItems int64 `json:"muted_items"`
}

// writeBadFilter answers a validation failure: 400 {"error":"bad_filter","field","message"}.
func writeBadFilter(w http.ResponseWriter, field, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_filter", "field": field, "message": message})
}

// filterError maps an error from the store to a response; it reports whether it handled err.
func (s *Server) filterError(w http.ResponseWriter, what string, err error) {
	var fe *filter.Error
	if errors.As(err, &fe) {
		writeBadFilter(w, fe.Field, fe.Message)
		return
	}
	s.serverError(w, what, err)
}

// filterIn is a create or patch body. Pointers tell "absent" from "false" or "empty".
type filterIn struct {
	Name           *string         `json:"name"`
	Enabled        *bool           `json:"enabled"`
	Scope          *string         `json:"scope"`
	FolderID       json.RawMessage `json:"folder_id"`
	FeedID         json.RawMessage `json:"feed_id"`
	Kind           *string         `json:"kind"`
	Terms          *[]string       `json:"terms"`
	Fields         *[]string       `json:"fields"`
	CaseSensitive  *bool           `json:"case_sensitive"`
	WholeWord      *bool           `json:"whole_word"`
	FoldDiacritics *bool           `json:"fold_diacritics"`
	Invert         *bool           `json:"invert"`
	Action         *string         `json:"action"`
	Position       *int            `json:"position"`
}

// newFilterDefaults are the spec defaults for a rule created without those fields.
func newFilterDefaults() store.Filter {
	return store.Filter{Enabled: true, Scope: "global", Kind: "text", Terms: []string{}, Fields: []string{"title"},
		WholeWord: true, FoldDiacritics: true}
}

// idField reads an optional id (string, number or null). set reports that the key was present.
func idField(raw json.RawMessage, name string) (id *int64, set bool, err *filter.Error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	v, ok := parseID(raw)
	if !ok {
		return nil, true, &filter.Error{Field: name, Message: "must be an id"}
	}
	return &v, true, nil
}

// applyTo overlays the fields present in in onto f.
func (in filterIn) applyTo(f *store.Filter) *filter.Error {
	if in.Name != nil {
		f.Name = *in.Name
	}
	if in.Enabled != nil {
		f.Enabled = *in.Enabled
	}
	if in.Kind != nil {
		f.Kind = *in.Kind
	}
	if in.Terms != nil {
		f.Terms = *in.Terms
	}
	if in.Fields != nil {
		f.Fields = *in.Fields
	}
	if in.CaseSensitive != nil {
		f.CaseSensitive = *in.CaseSensitive
	}
	if in.WholeWord != nil {
		f.WholeWord = *in.WholeWord
	}
	if in.FoldDiacritics != nil {
		f.FoldDiacritics = *in.FoldDiacritics
	}
	if in.Invert != nil {
		f.Invert = *in.Invert
	}
	if in.Action != nil {
		f.Action = *in.Action
	}
	if in.Position != nil {
		f.Position = *in.Position
	}
	folder, folderSet, e1 := idField(in.FolderID, "folder_id")
	if e1 != nil {
		return e1
	}
	feed, feedSet, e2 := idField(in.FeedID, "feed_id")
	if e2 != nil {
		return e2
	}
	if folderSet {
		f.FolderID = folder
	}
	if feedSet {
		f.FeedID = feed
	}
	if in.Scope != nil {
		f.Scope = *in.Scope
		// Changing the scope drops the target that no longer fits unless the request names it.
		switch f.Scope {
		case "global":
			if !folderSet {
				f.FolderID = nil
			}
			if !feedSet {
				f.FeedID = nil
			}
		case "folder":
			if !feedSet {
				f.FeedID = nil
			}
		case "feed":
			if !folderSet {
				f.FolderID = nil
			}
		}
	}
	return nil
}

func (s *Server) filterViews(ctx context.Context, fs ...store.Filter) ([]filterJSON, error) {
	counts, err := s.db.MutedByFilter(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]filterJSON, len(fs))
	for i, f := range fs {
		out[i] = filterJSON{Filter: f, MutedItems: counts[f.ID]}
	}
	return out, nil
}

func (s *Server) publishFiltersChanged() {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("filters.changed", map[string]any{})
	}
}

// ---- GET /api/filters ----

func (s *Server) listFilters(w http.ResponseWriter, r *http.Request) {
	fs, err := s.db.ListFilters(r.Context())
	if err != nil {
		s.serverError(w, "list filters", err)
		return
	}
	views, err := s.filterViews(r.Context(), fs...)
	if err != nil {
		s.serverError(w, "list filters", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filters": views})
}

// ---- POST /api/filters ----

type applyExisting struct {
	IncludeRead bool `json:"include_read"`
}

func (s *Server) createFilter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		filterIn
		ApplyExisting *applyExisting `json:"apply_existing"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	f := newFilterDefaults()
	if e := body.applyTo(&f); e != nil {
		writeBadFilter(w, e.Field, e.Message)
		return
	}
	if body.Action == nil {
		writeBadFilter(w, "action", "is required (mute, mark_read, star or highlight)")
		return
	}
	if ae := body.ApplyExisting; ae != nil {
		if f.Action == string(filter.ActionHighlight) || !f.Enabled {
			writeBadFilter(w, "apply_existing", "only an enabled mute, mark_read or star filter can be applied to stored articles")
			return
		}
	}
	created, err := s.db.CreateFilter(r.Context(), f)
	if err != nil {
		s.filterError(w, "create filter", err)
		return
	}
	s.publishFiltersChanged()
	views, err := s.filterViews(r.Context(), created)
	if err != nil {
		s.serverError(w, "create filter", err)
		return
	}
	resp := map[string]any{"filter": views[0], "applied": nil}
	if ae := body.ApplyExisting; ae != nil {
		run, err := s.startApply(created.ID, ae.IncludeRead)
		switch {
		case errors.Is(err, errApplyBusy):
			// The filter is created; only the apply could not start. Say so instead of failing the create.
			resp["applied"] = map[string]any{"error": "busy"}
		case err != nil:
			s.filterError(w, "apply filter", err)
			return
		default:
			resp["applied"] = run
		}
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ---- PATCH /api/filters/{id} ----

func (s *Server) patchFilter(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body filterIn
	if !decodeBody(w, r, &body, false) {
		return
	}
	f, found, err := s.db.UpdateFilter(r.Context(), id, func(f *store.Filter) error {
		if e := body.applyTo(f); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		s.filterError(w, "patch filter", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	s.publishFiltersChanged()
	views, err := s.filterViews(r.Context(), f)
	if err != nil {
		s.serverError(w, "patch filter", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"filter": views[0]})
}

// ---- DELETE /api/filters/{id}?unmute= ----

func (s *Server) deleteFilter(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	mode := store.UnmuteRead
	switch v := r.URL.Query().Get("unmute"); v {
	case "":
	case store.UnmuteKeep, store.UnmuteRead, store.UnmuteUnread:
		mode = v
	case "1", "true":
		mode = store.UnmuteUnread // "restore what it muted": back to unread, as if never filtered
	default:
		writeBadFilter(w, "unmute", "must be keep, read or unread")
		return
	}
	changed, found, err := s.db.DeleteFilter(r.Context(), id, mode, func(res store.StateResult) {
		extra := map[string]any{"muted": false}
		if mode == store.UnmuteUnread {
			extra["read"] = false
		}
		s.publishState(res, extra)
	})
	if !found && err == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	s.publishFiltersChanged()
	if err != nil {
		s.serverError(w, "delete filter", err)
		return
	}
	s.noteCounts()
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed})
}

// ---- POST /api/filters/preview ----

func (s *Server) previewFilter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          json.RawMessage `json:"id"`
		Filter      *filterIn       `json:"filter"`
		IncludeRead bool            `json:"include_read"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	f := newFilterDefaults()
	hasID := len(body.ID) > 0 && string(body.ID) != "null"
	if hasID {
		id, ok := parseID(body.ID)
		if !ok {
			writeBadFilter(w, "id", "must be an id")
			return
		}
		saved, found, err := s.db.GetFilter(r.Context(), id)
		if err != nil {
			s.serverError(w, "preview filter", err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		f = saved // an edit in progress may overlay it with filter
	} else if body.Filter == nil {
		writeBadFilter(w, "filter", "send {filter:{...}} or {id}")
		return
	}
	if body.Filter != nil {
		if e := body.Filter.applyTo(&f); e != nil {
			writeBadFilter(w, e.Field, e.Message)
			return
		}
		if !hasID && body.Filter.Action == nil {
			writeBadFilter(w, "action", "is required (mute, mark_read, star or highlight)")
			return
		}
	}
	budget := s.opt.PreviewBudget
	if budget <= 0 {
		budget = previewBudget
	}
	res, err := s.db.PreviewFilter(r.Context(), f, body.IncludeRead, budget)
	if err != nil {
		s.filterError(w, "preview filter", err)
		return
	}
	sample := []store.Card{}
	if len(res.SampleIDs) > 0 {
		sample, _, err = s.db.ListCards(r.Context(), store.CardQuery{IDs: res.SampleIDs})
		if err != nil {
			s.serverError(w, "preview filter", err)
			return
		}
		s.proxyCards(r.Context(), sample)
	}
	warnings := []warning{}
	for _, fld := range f.Fields {
		if fld == string(filter.FieldCategory) {
			warnings = append(warnings, warning{"category_new_items_only",
				"Category rules only match articles fetched after filters were introduced; older articles have no stored categories."})
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": res.Matches, "scanned": res.Scanned, "truncated": res.Truncated,
		"sample": sample, "warnings": warnings})
}

// ---- POST /api/filters/{id}/apply ----

func (s *Server) applyFilterRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body struct {
		IncludeRead bool `json:"include_read"`
	}
	if !decodeBody(w, r, &body, true) {
		return
	}
	run, err := s.startApply(id, body.IncludeRead)
	switch {
	case errors.Is(err, errApplyBusy):
		writeError(w, http.StatusConflict, "busy")
	case errors.Is(err, store.ErrFilterNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case err != nil:
		s.filterError(w, "apply filter", err)
	default:
		writeJSON(w, http.StatusAccepted, run)
	}
}

// ---- the apply run ----

var errApplyBusy = errors.New("api: a filter apply is already running")

// applyRun is the run the API reports and publishes.
type applyRun struct {
	ID       int64  `json:"id,string"`
	Kind     string `json:"kind"`
	FilterID int64  `json:"filter_id,string"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Changed  int    `json:"changed"`
	NewItems int    `json:"new_items"`
	Errors   int    `json:"errors"`
}

type applyState struct {
	mu   sync.Mutex
	run  *applyRun // the active run, nil when idle
	wg   sync.WaitGroup
	stop context.CancelFunc
	ctx  context.Context
}

// applyStatus is the active apply run for bootstrap `runs`, or nil.
func (s *Server) applyStatus() *applyRun {
	s.apply.mu.Lock()
	defer s.apply.mu.Unlock()
	if s.apply.run == nil {
		return nil
	}
	c := *s.apply.run
	return &c
}

// startApply validates and starts a retroactive apply of filter id on a background goroutine. Only
// one runs at a time (errApplyBusy). Progress goes out as run.start, run.progress (at most every
// 500 ms) and run.done, item changes as items.state per batch, then counts.
func (s *Server) startApply(id int64, includeRead bool) (*applyRun, error) {
	s.apply.mu.Lock()
	defer s.apply.mu.Unlock()
	if s.apply.run != nil {
		return nil, errApplyBusy
	}
	if s.apply.ctx == nil || s.apply.ctx.Err() != nil {
		return nil, errApplyBusy // shutting down
	}
	_, total, err := s.db.RetroTotal(s.apply.ctx, id, includeRead)
	if err != nil {
		return nil, err
	}
	run := &applyRun{ID: s.now().UnixMicro(), Kind: runKindFilterApply, FilterID: id, Total: total}
	s.apply.run = run
	snapshot := *run
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("run.start", map[string]any{"run_id": idStr(run.ID), "kind": run.Kind, "total": total, "filter_id": idStr(id)})
	}
	s.apply.wg.Add(1)
	go s.runApply(run, id, includeRead, total)
	return &snapshot, nil
}

func (s *Server) runApply(run *applyRun, id int64, includeRead bool, total int) {
	defer s.apply.wg.Done()
	var lastProgress time.Time
	res, err := s.db.ApplyFilter(s.apply.ctx, id, includeRead, total,
		func(p store.ApplyProgress) {
			s.apply.mu.Lock()
			run.Done, run.Total, run.Changed = p.Done, p.Total, p.Changed
			s.apply.mu.Unlock()
			if s.opt.Hub != nil && time.Since(lastProgress) >= 500*time.Millisecond {
				lastProgress = time.Now()
				s.opt.Hub.Publish("run.progress", map[string]any{"run_id": idStr(run.ID), "done": p.Done, "total": p.Total,
					"new_items": 0, "errors": 0, "changed": p.Changed})
			}
		},
		func(b store.ApplyBatch) {
			extra := map[string]any{}
			switch b.Action {
			case filter.ActionMute:
				extra["read"], extra["muted"] = true, true
			case filter.ActionMarkRead:
				extra["read"] = true
			case filter.ActionStar:
				extra["starred"] = true
			}
			s.publishState(b.Res, extra)
		})
	nErr := 0
	if err != nil {
		nErr = 1
		if s.apply.ctx.Err() == nil {
			s.log.Error("api: apply filter", "filter", id, "err", err)
		}
	}
	s.apply.mu.Lock()
	s.apply.run = nil
	s.apply.mu.Unlock()
	if s.opt.Hub != nil {
		ev := map[string]any{"run_id": idStr(run.ID), "kind": runKindFilterApply, "filter_id": idStr(id), "new_items": 0,
			"errors": nErr, "changed": res.Changed, "scanned": res.Scanned}
		if err != nil {
			ev["error"] = "apply_failed"
		}
		s.opt.Hub.Publish("run.done", ev)
	}
	s.publishFiltersChanged()
	s.noteCounts()
}
