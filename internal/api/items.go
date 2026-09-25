package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/stats"
	"github.com/WPTK/kipple/internal/store"
)

const (
	maxBodyBytes   = 1 << 20
	maxMarkIDs     = 10000
	maxIDsQuery    = store.CardMaxLimit
	maxSearchQuery = 1000
	maxStatsBatch  = 200
	statsBodyMax   = 64 << 10
)

// decodeBody reads a JSON body into v. An empty body leaves v untouched and
// counts as success when allowEmpty is set. It writes 400 on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v)
	if err == nil || (allowEmpty && errors.Is(err, io.EOF)) {
		return true
	}
	writeError(w, http.StatusBadRequest, "bad_request")
	return false
}

// parseID parses a decimal id given as a JSON string or number.
func parseID(raw json.RawMessage) (int64, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}

func pathItemID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func idStrings(ids []int64) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out
}

// client is the stats attribution: pwa when the app says so, else web.
func client(r *http.Request) string {
	if r.Header.Get("X-Kipple-Client") == "pwa" {
		return "pwa"
	}
	return "web"
}

// ---- GET /api/items ----

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	search := qv.Get("q")
	var q store.CardQuery
	switch qv.Get("order") {
	case "", "date":
	case "rank":
		q.Rank = true
	default:
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(search) > maxSearchQuery || (q.Rank && strings.TrimSpace(search) == "") {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	q.Query = strings.TrimSpace(search)
	if v := qv.Get("ids"); v != "" {
		for _, p := range strings.Split(v, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil || id <= 0 || len(q.IDs) >= maxIDsQuery {
				writeError(w, http.StatusBadRequest, "bad_request")
				return
			}
			q.IDs = append(q.IDs, id)
		}
	} else {
		q.View = qv.Get("view")
		switch q.View {
		case "":
			q.View = "unread"
			if q.Query != "" {
				q.View = "all" // a search spans read items unless the client narrows it
			}
		case "unread", "all", "starred":
		default:
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		var err1, err2, err3 error
		if v := qv.Get("feed"); v != "" {
			q.FeedID, err1 = strconv.ParseInt(v, 10, 64)
		}
		if v := qv.Get("folder"); v != "" {
			q.FolderID, err2 = strconv.ParseInt(v, 10, 64)
		}
		if v := qv.Get("limit"); v != "" {
			q.Limit, err3 = strconv.Atoi(v)
		}
		if err1 != nil || err2 != nil || err3 != nil || (q.FeedID != 0 && q.FolderID != 0) || q.Limit < 0 {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
		if v := qv.Get("cursor"); v != "" {
			c, err := store.ParseCursor(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request")
				return
			}
			if c.ByRank != q.Rank {
				writeError(w, http.StatusBadRequest, "bad_request") // cursor from another ordering
				return
			}
			q.Cursor = &c
		}
	}
	cards, next, err := s.db.ListCards(r.Context(), q)
	if err != nil {
		s.log.Error("api: list items", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	s.proxyCards(r.Context(), cards)
	var cur any
	if next != nil {
		cur = next.Encode()
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": cards, "next_cursor": cur})
}

// ---- GET /api/items/{id} ----

// getItem is prefetch-safe: it never records a stat.
func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	det, found, err := s.db.GetItem(r.Context(), id, s.now().Unix())
	if err != nil {
		s.log.Error("api: get item", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	s.proxyDetail(r.Context(), &det)
	writeJSON(w, http.StatusOK, det)
}

// ---- POST /api/items/{id}/open ----

func (s *Server) openItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body struct {
		Via string `json:"via"`
	}
	if !decodeBody(w, r, &body, true) {
		return
	}
	switch body.Via {
	case "", "tap", "key", "nav":
	default:
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	now := s.now().Unix()
	if known, err := s.db.ItemKnown(r.Context(), id, now); err != nil {
		s.serverError(w, "open item", err)
		return
	} else if !known {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	key := stats.NewSessionKey()
	var res store.StateResult
	err := s.db.WithWrite(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if res, err = store.SetRead(ctx, tx, []int64{id}, true, now); err != nil {
			return err
		}
		err = s.rec.Record(tx, stats.Event{Kind: stats.KindOpen, Client: client(r), ItemID: id, SessionKey: key})
		if errors.Is(err, stats.ErrDropped) {
			return nil
		}
		return err
	})
	if err != nil {
		s.serverError(w, "open item", err)
		return
	}
	s.publishState(res, map[string]any{"read": true})
	det, found, err := s.db.GetItem(r.Context(), id, now)
	if err != nil {
		s.serverError(w, "open item", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	s.proxyDetail(r.Context(), &det)
	writeJSON(w, http.StatusOK, map[string]any{"session_key": key, "item": det})
}

// ---- PUT /api/items/{id}/star ----

func (s *Server) starItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var body struct {
		Starred *bool `json:"starred"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	if body.Starred == nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	now := s.now().Unix()
	if known, err := s.db.ItemKnown(r.Context(), id, now); err != nil {
		s.serverError(w, "star item", err)
		return
	} else if !known {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	kind := stats.KindUnstar
	if *body.Starred {
		kind = stats.KindStar
	}
	var res store.StateResult
	err := s.db.WithWrite(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if res, err = store.SetStarred(ctx, tx, []int64{id}, *body.Starred, now); err != nil {
			return err
		}
		for _, cid := range res.Changed { // only when RETURNING shows a change
			err := s.rec.Record(tx, stats.Event{Kind: kind, Client: client(r), ItemID: cid})
			if err != nil && !errors.Is(err, stats.ErrDropped) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.serverError(w, "star item", err)
		return
	}
	s.publishState(res, map[string]any{"starred": *body.Starred})
	writeJSON(w, http.StatusOK, map[string]any{"starred": *body.Starred, "restored": len(res.Restored) > 0})
}

// ---- POST /api/items/mark-read ----

type markReadRequest struct {
	IDs   []json.RawMessage `json:"ids"`
	Scope *struct {
		FeedID   json.RawMessage `json:"feed_id"`
		FolderID json.RawMessage `json:"folder_id"`
		All      bool            `json:"all"`
		View     string          `json:"view"`
	} `json:"scope"`
	MaxID  json.RawMessage `json:"max_id"`
	Read   *bool           `json:"read"`
	Reason string          `json:"reason"`
}

// markRead never receives the Recorder: read-state changes are not stats
// (design §7.2, §8).
func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	var req markReadRequest
	if !decodeBody(w, r, &req, false) {
		return
	}
	bad := func() { writeError(w, http.StatusBadRequest, "bad_request") }
	switch req.Reason {
	case "", "swipe", "key", "scroll", "bulk":
	default:
		bad()
		return
	}
	if req.Read == nil || (req.IDs != nil) == (req.Scope != nil) {
		bad()
		return
	}
	read := *req.Read
	now := s.now().Unix()

	var scope store.MarkScope
	var ids []int64
	var maxID int64
	if req.Scope != nil {
		sc := req.Scope
		targets := 0
		if len(sc.FeedID) > 0 && string(sc.FeedID) != "null" {
			targets++
			var ok bool
			if scope.FeedID, ok = parseID(sc.FeedID); !ok {
				bad()
				return
			}
		}
		if len(sc.FolderID) > 0 && string(sc.FolderID) != "null" {
			targets++
			var ok bool
			if scope.FolderID, ok = parseID(sc.FolderID); !ok {
				bad()
				return
			}
		}
		if sc.All {
			targets++
		}
		switch sc.View {
		case "", "unread", "all":
		case "starred":
			scope.Starred = true
		default:
			bad()
			return
		}
		if targets != 1 || !read { // scope marks read only; mark-unread is by id
			bad()
			return
		}
		if len(req.MaxID) > 0 && string(req.MaxID) != "null" {
			var ok bool
			if maxID, ok = parseID(req.MaxID); !ok {
				bad()
				return
			}
		} else {
			var err error
			if maxID, err = s.db.MaxCommittedID(r.Context()); err != nil {
				s.serverError(w, "mark-read", err)
				return
			}
		}
	} else {
		if len(req.IDs) > maxMarkIDs {
			bad()
			return
		}
		for _, raw := range req.IDs {
			id, ok := parseID(raw)
			if !ok {
				bad()
				return
			}
			ids = append(ids, id)
		}
	}

	var res store.StateResult
	err := s.db.WithWrite(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if req.Scope != nil {
			res, err = store.MarkScopeRead(ctx, tx, scope, maxID, now)
		} else {
			res, err = store.SetRead(ctx, tx, ids, read, now)
		}
		return err
	})
	if err != nil {
		s.serverError(w, "mark-read", err)
		return
	}
	s.log.Debug("api: mark-read", "reason", req.Reason, "read", read, "changed", len(res.Changed))
	s.publishState(res, map[string]any{"read": read})
	writeJSON(w, http.StatusOK, map[string]any{"changed": idStrings(nonNil(res.Changed)), "restored": idStrings(nonNil(res.Restored))})
}

func nonNil(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

func (s *Server) serverError(w http.ResponseWriter, what string, err error) {
	s.log.Error("api: "+what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

// ---- SSE ----

// publishState sends items.state (or resync when too many ids) and schedules a
// counts event. extra carries read/starred.
func (s *Server) publishState(res store.StateResult, extra map[string]any) {
	if len(res.Changed) == 0 {
		return
	}
	if s.opt.Hub != nil {
		if len(res.Changed) > events.MaxStateIDs {
			s.opt.Hub.Publish("resync", map[string]any{})
		} else {
			ev := map[string]any{"ids": idStrings(res.Changed), "source": "web"}
			for k, v := range extra {
				ev[k] = v
			}
			if len(res.Restored) > 0 {
				ev["restored"] = idStrings(res.Restored)
			}
			s.opt.Hub.Publish("items.state", ev)
		}
	}
	s.noteCounts()
}

// noteCounts publishes a `counts` event at most once per CountsInterval: the
// first call goes out at once, later ones inside the interval collapse into one
// trailing event.
func (s *Server) noteCounts() {
	if s.opt.Hub == nil {
		return
	}
	s.cmu.Lock()
	if s.ctimer != nil || s.closed {
		s.cmu.Unlock()
		return
	}
	wait := s.opt.CountsInterval - time.Since(s.clast)
	if wait <= 0 {
		s.clast = time.Now()
		s.cmu.Unlock()
		s.publishCounts()
		return
	}
	s.ctimer = time.AfterFunc(wait, func() {
		s.cmu.Lock()
		s.ctimer = nil
		if s.closed {
			s.cmu.Unlock()
			return
		}
		s.clast = time.Now()
		s.cmu.Unlock()
		s.publishCounts()
	})
	s.cmu.Unlock()
}

func (s *Server) publishCounts() {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unread, _, err := s.db.Counts(ctx)
	if err != nil {
		s.log.Error("api: counts", "err", err)
		return
	}
	perFeed, err := s.db.FeedUnreadCounts(ctx)
	if err != nil {
		s.log.Error("api: counts", "err", err)
		return
	}
	feeds := make(map[string]int64, len(perFeed))
	for id, n := range perFeed {
		feeds[strconv.FormatInt(id, 10)] = n
	}
	s.opt.Hub.Publish("counts", map[string]any{"unread_total": unread, "feeds": feeds})
}

// ---- POST /api/stats/events ----

type statsEventIn struct {
	Kind       string          `json:"kind"`
	ItemID     json.RawMessage `json:"item_id"`
	SessionKey string          `json:"session_key"`
	Value      *float64        `json:"value"`
}

// statsEvents ingests web-only events. Everything invalid is dropped and the
// answer is 204 regardless (design §8): sendBeacon cannot read it anyway.
func (s *Server) statsEvents(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, statsBodyMax))
	var in struct {
		Events []statsEventIn `json:"events"`
	}
	if err != nil || json.NewDecoder(bytes.NewReader(raw)).Decode(&in) != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if len(in.Events) > maxStatsBatch {
		in.Events = in.Events[:maxStatsBatch]
	}
	cl := client(r)
	err = s.db.WithWrite(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
		for _, e := range in.Events {
			switch e.Kind {
			case stats.KindReadTime, stats.KindScroll, stats.KindOpenOriginal, stats.KindShare:
			default:
				continue // open, star and unstar have their own endpoints
			}
			id, ok := parseID(e.ItemID)
			if !ok {
				continue
			}
			ev := stats.Event{Kind: e.Kind, Client: cl, ItemID: id, SessionKey: e.SessionKey}
			if e.Value != nil {
				ev.Value, ev.HasValue = int64(*e.Value), true
			}
			if err := s.rec.Record(tx, ev); err != nil && !errors.Is(err, stats.ErrDropped) {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.serverError(w, "stats events", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Close cancels a pending trailing `counts` event.
func (s *Server) Close() {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.closed = true
	if s.ctimer != nil {
		s.ctimer.Stop()
		s.ctimer = nil
	}
}
