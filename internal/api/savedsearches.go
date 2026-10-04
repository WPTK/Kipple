package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// Saved searches (backend plan F4, design 7.1d). The list is the setting library.saved_searches;
// these routes edit it atomically (store.EditSavedSearches) and add a live unread count.

const (
	// savedSearchCap is where a count stops: the UI shows "999+".
	savedSearchCap = 999
)

// savedSearchBudget bounds one search's count; savedSearchTotalBudget bounds a whole list, so a
// hundred slow searches cannot hold the request. A count that runs out of budget is null. New copies
// them into each server's timing, which a test may widen per server.
const (
	savedSearchBudget      = 200 * time.Millisecond
	savedSearchTotalBudget = 2 * time.Second
)

// savedSearchView is a saved search with its live unread count. Unread is null when the count was
// skipped (?counts=0) or ran out of budget; Capped is true when the real number is above 999
// (Unread is then 999).
type savedSearchView struct {
	store.SavedSearch
	Unread *int `json:"unread"`
	Capped bool `json:"unread_capped"`
}

// countSavedSearch counts the unread articles a saved search finds, through the same card search
// GET /api/items uses (so it can never disagree with the list the user opens), page by page until
// savedSearchCap or the end. A search that found nothing exact (the prefix/OR fallback) counts 0.
func (s *Server) countSavedSearch(ctx context.Context, ss store.SavedSearch) (n int, capped, ok bool) {
	ctx, cancel := s.tm.savedSearchCountCtx(ctx, s.tm.savedSearchBudget)
	defer cancel()
	q := store.CardQuery{Query: searchText(ss.Q), View: "unread", Limit: store.CardMaxLimit}
	starred := false
	if sc := ss.Scope; sc != nil {
		switch {
		case sc.View == "starred":
			q.View, starred = "starred", true
		case sc.FeedID != "":
			q.FeedID, _ = strconv.ParseInt(sc.FeedID, 10, 64)
		case sc.FolderID != "":
			q.FolderID, _ = strconv.ParseInt(sc.FolderID, 10, 64)
		}
	}
	if q.Query == "" {
		return 0, false, true
	}
	for {
		cards, next, fallback, err := s.db.ListCardsFB(ctx, q)
		if err != nil {
			return 0, false, false
		}
		if fallback {
			return 0, false, true
		}
		for _, c := range cards {
			if !starred || !c.Read {
				n++
			}
		}
		if n > savedSearchCap {
			return savedSearchCap, true, true
		}
		if next == nil {
			return n, false, true
		}
		q.Cursor = next
	}
}

func (s *Server) savedSearchViews(ctx context.Context, list []store.SavedSearch, counts bool) []savedSearchView {
	out := make([]savedSearchView, 0, len(list))
	deadline := s.tm.savedSearchNow().Add(s.tm.savedSearchTotalBudget)
	for _, ss := range list {
		v := savedSearchView{SavedSearch: ss}
		if counts && s.tm.savedSearchNow().Before(deadline) {
			if n, capped, ok := s.countSavedSearch(ctx, ss); ok {
				v.Unread, v.Capped = &n, capped
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) savedSearchError(w http.ResponseWriter, what string, err error) {
	var ve *store.SavedSearchError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_saved_search", "field": ve.Field, "message": ve.Message})
	case errors.Is(err, store.ErrSavedSearchNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, store.ErrTooManySavedSearches):
		writeErrorMsg(w, http.StatusConflict, "too_many", "at most 100 saved searches")
	default:
		s.serverError(w, what, err)
	}
}

func (s *Server) publishSavedSearchesChanged() {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("saved_searches.changed", map[string]any{})
	}
}

// ---- GET /api/saved-searches ----

// listSavedSearches answers {saved_searches:[...]}; ?counts=0 skips the unread counts.
func (s *Server) listSavedSearches(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.SavedSearches(r.Context())
	if err != nil {
		s.serverError(w, "saved searches", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"saved_searches": s.savedSearchViews(r.Context(), list, r.URL.Query().Get("counts") != "0")})
}

// searchIn is a create body.
type savedSearchIn struct {
	Name  string                  `json:"name"`
	Q     string                  `json:"q"`
	Scope *store.SavedSearchScope `json:"scope"`
	Order string                  `json:"order"`
}

// decodeStrict reads a JSON object body and rejects unknown fields.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return false
	}
	return true
}

// ---- POST /api/saved-searches ----

// createSavedSearch adds one ({name, q, scope?, order?}); the id is made by the server. 201 with
// the saved search and its count.
func (s *Server) createSavedSearch(w http.ResponseWriter, r *http.Request) {
	var in savedSearchIn
	if !decodeStrict(w, r, &in) {
		return
	}
	ss, err := store.SavedSearch{Name: in.Name, Q: in.Q, Scope: in.Scope, Order: in.Order}.Normalize()
	if err != nil {
		s.savedSearchError(w, "create saved search", err)
		return
	}
	ss.ID = store.NewSavedSearchID()
	_, err = s.db.EditSavedSearches(r.Context(), func(list []store.SavedSearch) ([]store.SavedSearch, error) {
		if len(list) >= store.MaxSavedSearches {
			return nil, store.ErrTooManySavedSearches
		}
		return append(list, ss), nil
	})
	if err != nil {
		s.savedSearchError(w, "create saved search", err)
		return
	}
	s.publishSavedSearchesChanged()
	writeJSON(w, http.StatusCreated, s.savedSearchViews(r.Context(), []store.SavedSearch{ss}, true)[0])
}

// ---- PATCH /api/saved-searches/{id} ----

// patchSavedSearch changes name, q, scope (null clears it) or order (null clears it).
func (s *Server) patchSavedSearch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, ok := readObject(w, r, "name", "q", "scope", "order")
	if !ok {
		return
	}
	var newScope *store.SavedSearchScope
	if raw, has := m["scope"]; has && !isNull(raw) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		newScope = new(store.SavedSearchScope)
		if err := dec.Decode(newScope); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_saved_search", "field": "scope", "message": "must be {feed_id}, {folder_id} or {view}"})
			return
		}
	}
	var out store.SavedSearch
	_, err := s.db.EditSavedSearches(r.Context(), func(list []store.SavedSearch) ([]store.SavedSearch, error) {
		for i := range list {
			if list[i].ID != id {
				continue
			}
			c := list[i]
			for k, raw := range m {
				var v string
				switch k {
				case "name", "q":
					str, ok := rawString(raw)
					if !ok {
						return nil, &store.SavedSearchError{Field: k, Message: "must be text"}
					}
					v = str
					if k == "name" {
						c.Name = v
					} else {
						c.Q = v
					}
				case "order":
					if isNull(raw) {
						c.Order = ""
					} else if str, ok := rawString(raw); ok {
						c.Order = str
					} else {
						return nil, &store.SavedSearchError{Field: k, Message: `must be "date", "oldest" or "rank"`}
					}
				case "scope":
					c.Scope = newScope
				}
			}
			n, err := c.Normalize()
			if err != nil {
				return nil, err
			}
			list[i], out = n, n
			return list, nil
		}
		return nil, store.ErrSavedSearchNotFound
	})
	if err != nil {
		s.savedSearchError(w, "patch saved search", err)
		return
	}
	s.publishSavedSearchesChanged()
	writeJSON(w, http.StatusOK, s.savedSearchViews(r.Context(), []store.SavedSearch{out}, true)[0])
}

// ---- DELETE /api/saved-searches/{id} ----

func (s *Server) deleteSavedSearch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := s.db.EditSavedSearches(r.Context(), func(list []store.SavedSearch) ([]store.SavedSearch, error) {
		for i := range list {
			if list[i].ID == id {
				return append(list[:i], list[i+1:]...), nil
			}
		}
		return nil, store.ErrSavedSearchNotFound
	})
	if err != nil {
		s.savedSearchError(w, "delete saved search", err)
		return
	}
	s.publishSavedSearchesChanged()
	w.WriteHeader(http.StatusNoContent)
}

// ---- POST /api/saved-searches/reorder ----

// reorderSavedSearches sets the order: {ids:[...]} must name every saved search exactly once.
func (s *Server) reorderSavedSearches(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if !decodeStrict(w, r, &in) {
		return
	}
	list, err := s.db.EditSavedSearches(r.Context(), func(cur []store.SavedSearch) ([]store.SavedSearch, error) {
		byID := make(map[string]store.SavedSearch, len(cur))
		for _, c := range cur {
			byID[c.ID] = c
		}
		if len(in.IDs) != len(cur) {
			return nil, &store.SavedSearchError{Field: "ids", Message: "must name every saved search exactly once"}
		}
		out := make([]store.SavedSearch, 0, len(cur))
		for _, id := range in.IDs {
			c, ok := byID[id]
			if !ok {
				return nil, &store.SavedSearchError{Field: "ids", Message: "must name every saved search exactly once"}
			}
			delete(byID, id)
			out = append(out, c)
		}
		return out, nil
	})
	if err != nil {
		s.savedSearchError(w, "reorder saved searches", err)
		return
	}
	s.publishSavedSearchesChanged()
	writeJSON(w, http.StatusOK, map[string]any{"saved_searches": s.savedSearchViews(r.Context(), list, false)})
}
