package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// The two waits are variables so tests can shorten them.
var (
	addWait     = 8 * time.Second  // POST /api/feeds waits this long for the first fetch
	refreshWait = 15 * time.Second // POST /api/feeds/{id}/refresh
)

const (
	discoverWait  = 10 * time.Second
	maxTitleRunes = 200
	maxFolderName = 100
	maxUAAuthLen  = 500
)

// retentionChoices are the values feeds.retention accepts (0 = unlimited).
var retentionChoices = map[int64]bool{0: true, 50: true, 100: true, 250: true, 500: true, 1000: true}

func writeErrorMsg(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]string{"error": kind, "message": msg})
}

// readObject decodes a JSON object body into raw fields and rejects any key not
// in allowed (400 unknown_field).
func readObject(w http.ResponseWriter, r *http.Request, allowed ...string) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if !decodeBody(w, r, &m, false) {
		return nil, false
	}
	if m == nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return nil, false
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range m {
		if !ok[k] {
			writeErrorMsg(w, http.StatusBadRequest, "unknown_field", "unknown field "+k)
			return nil, false
		}
	}
	return m, true
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func rawBool(raw json.RawMessage) (bool, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil || isNull(raw) {
		return false, false
	}
	return b, true
}

func rawInt(raw json.RawMessage) (int64, bool) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] == '"' { // a JSON string is not a number
		return 0, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var num json.Number
	if err := d.Decode(&num); err != nil {
		return 0, false
	}
	n, err := num.Int64()
	return n, err == nil
}

func rawString(raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || isNull(raw) {
		return "", false
	}
	return s, true
}

func hasControl(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// awaitReply waits for a priority job's reply, the timer, scheduler shutdown or
// the client going away.
func (s *Server) awaitReply(r *http.Request, ch <-chan sched.Reply, d time.Duration) (rep sched.Reply, got bool, err error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case rep = <-ch:
		return rep, true, nil
	case <-t.C:
		return rep, false, nil
	case <-s.opt.Sched.Shutdown():
		return rep, false, sched.ErrStopped
	case <-r.Context().Done():
		return rep, false, r.Context().Err()
	}
}

func (s *Server) publishFeedChanged(id int64) {
	if s.opt.Hub != nil {
		s.opt.Hub.Publish("feed.changed", map[string]any{"feed_id": idStr(id)})
	}
}

func idStr(id int64) string { return idStrings([]int64{id})[0] }

type fetchOutcome struct {
	Pending  bool   `json:"pending,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	NewItems int    `json:"new_items"`
	ErrClass string `json:"error_class,omitempty"`
	Error    string `json:"error,omitempty"`
}

func outcomeOf(rep sched.Reply) fetchOutcome {
	return fetchOutcome{Outcome: rep.Outcome, NewItems: rep.NewItems, ErrClass: rep.ErrClass, Error: rep.ErrMsg}
}

// ---- POST /api/feeds ----

func (s *Server) addFeed(w http.ResponseWriter, r *http.Request) {
	m, ok := readObject(w, r, "url", "folder_id", "title")
	if !ok {
		return
	}
	rawURL, ok := rawString(m["url"])
	if !ok || strings.TrimSpace(rawURL) == "" {
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", "url is required")
		return
	}
	opts := store.SubscribeOpts{}
	if raw, present := m["folder_id"]; present && !isNull(raw) {
		id, ok := parseID(raw)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "folder_id must be a folder id")
			return
		}
		if exists, err := s.db.FolderExists(r.Context(), id); err != nil {
			s.serverError(w, "add feed", err)
			return
		} else if !exists {
			writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such folder")
			return
		}
		opts.FolderID = id
	}
	if raw, present := m["title"]; present && !isNull(raw) {
		t, ok := rawString(raw)
		t = strings.TrimSpace(t)
		if !ok || utf8.RuneCountInString(t) > maxTitleRunes || hasControl(t) {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "title must be text up to 200 characters")
			return
		}
		opts.Title = t
	}
	norm, _, _, err := store.ValidateFeedURL(rawURL, false)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	ctx := r.Context()
	if id, found, err := s.db.FindFeedID(ctx, norm); err != nil {
		s.serverError(w, "add feed", err)
		return
	} else if found {
		s.writeExisting(w, r, id)
		return
	}

	dctx, cancel := context.WithTimeout(ctx, discoverWait)
	// Same User-Agent policy as a feed fetch (fetch.user_agent_mode, a custom UA),
	// minus the per-feed switches a new feed does not have yet.
	ua, retryUA := store.ResolveUserAgent(s.db.FetchSettings(ctx), "", false)
	if ua == "" {
		ua = s.outgoingUA()
	}
	found, err := discover.Find(dctx, s.opt.Guard(false, false, false), ua, retryUA, norm)
	cancel()
	if err != nil {
		if errors.Is(err, discover.ErrNoFeed) {
			writeErrorMsg(w, http.StatusUnprocessableEntity, "no_feed", err.Error())
		} else {
			writeErrorMsg(w, http.StatusUnprocessableEntity, "discovery_failed", err.Error())
		}
		return
	}
	if len(found.Candidates) > 1 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "choose", "candidates": found.Candidates})
		return
	}
	target := found.Candidates[0].URL
	if id, exists, err := s.db.FindFeedID(ctx, target); err != nil {
		s.serverError(w, "add feed", err)
		return
	} else if exists {
		s.writeExisting(w, r, id)
		return
	}

	opts.URL = target
	res, err := s.db.Subscribe(ctx, opts)
	var bad *store.InvalidURLError
	switch {
	case errors.As(err, &bad):
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", bad.Reason)
		return
	case err != nil:
		s.serverError(w, "add feed", err)
		return
	case res.Existed:
		s.writeExisting(w, r, res.FeedID)
		return
	}
	s.publishFeedChanged(res.FeedID)
	ch, err := s.opt.Sched.Submit(sched.Priority{FeedID: res.FeedID, Full: true, Trigger: fetch.TriggerSubscribe})
	fo := fetchOutcome{Pending: true}
	if err != nil {
		s.log.Warn("api: add feed: submit first fetch", "err", err)
		s.opt.Sched.Wake() // the feed is due; the tick picks it up
	} else if rep, got, werr := s.awaitReply(r, ch, addWait); werr == nil && got && rep.Err == nil {
		fo = outcomeOf(rep)
	}
	fd, _, err := s.db.FeedDetail(ctx, res.FeedID, s.statusEnv())
	if err != nil {
		s.serverError(w, "add feed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "feed": fd, "fetch": fo})
}

func (s *Server) writeExisting(w http.ResponseWriter, r *http.Request, id int64) {
	fd, found, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil || !found {
		if err == nil {
			err = store.ErrFeedNotFound
		}
		s.serverError(w, "add feed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "exists", "feed": fd})
}

// ---- PATCH /api/feeds/{id} ----

var patchKeys = []string{"custom_title", "folder_id", "position", "interval_minutes", "retention", "fulltext", "dedup_mode",
	"user_agent", "http_auth", "ignore_http_cache", "disable_http2", "allow_insecure_tls", "allow_private_net", "enabled", "url"}

// parsePatch validates every field of a PATCH body; msg is non-empty on failure.
func parsePatch(m map[string]json.RawMessage) (p store.FeedPatch, msg string) {
	p = store.FeedPatch{Cols: map[string]any{}}
	for k, raw := range m {
		null := isNull(raw)
		switch k {
		case "custom_title":
			if null {
				p.Cols[k] = nil
				continue
			}
			t, ok := rawString(raw)
			t = strings.TrimSpace(t)
			if !ok || utf8.RuneCountInString(t) > maxTitleRunes || hasControl(t) {
				return p, "custom_title must be null or text up to 200 characters"
			}
			if t == "" {
				p.Cols[k] = nil
			} else {
				p.Cols[k] = t
			}
		case "folder_id":
			id, ok := parseID(raw)
			if !ok || null {
				return p, "folder_id must be a folder id"
			}
			p.Cols[k] = id
		case "position":
			n, ok := rawInt(raw)
			if !ok || n < 0 || n > 1_000_000 {
				return p, "position must be an integer from 0 to 1000000"
			}
			p.Cols[k] = n
		case "interval_minutes":
			if null {
				p.Cols[k] = nil
				continue
			}
			n, ok := rawInt(raw)
			if !ok || n < 5 || n > 10080 {
				return p, "interval_minutes must be null or 5 to 10080"
			}
			p.Cols[k] = n
		case "retention":
			if null {
				p.Cols[k] = nil
				continue
			}
			n, ok := rawInt(raw)
			if !ok || !retentionChoices[n] {
				return p, "retention must be null, 0, 50, 100, 250, 500 or 1000"
			}
			p.Cols[k] = n
		case "dedup_mode":
			v, ok := rawString(raw)
			if !ok || (v != fetch.DedupAuto && v != "link" && v != "link_title") {
				return p, "dedup_mode must be auto, link or link_title"
			}
			p.Cols[k] = v
		case "user_agent", "http_auth":
			if null {
				p.Cols[k] = nil
				continue
			}
			v, ok := rawString(raw)
			v = strings.TrimSpace(v)
			if !ok || len(v) > maxUAAuthLen || hasControl(v) {
				return p, k + " must be null or text up to 500 characters without control characters"
			}
			if k == "http_auth" && v != "" && !strings.Contains(v, ":") {
				return p, "http_auth must look like user:password"
			}
			if v == "" {
				p.Cols[k] = nil
			} else {
				p.Cols[k] = v
			}
		case "fulltext", "ignore_http_cache", "disable_http2", "allow_insecure_tls", "allow_private_net":
			b, ok := rawBool(raw)
			if !ok {
				return p, k + " must be true or false"
			}
			p.Cols[k] = boolToInt(b)
		case "enabled":
			b, ok := rawBool(raw)
			if !ok {
				return p, "enabled must be true or false"
			}
			p.Enabled = &b
		case "url":
			v, ok := rawString(raw)
			if !ok || strings.TrimSpace(v) == "" {
				return p, "url must be a non-empty string"
			}
			p.URL = &v
		}
	}
	return p, ""
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) patchFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	m, ok := readObject(w, r, patchKeys...)
	if !ok {
		return
	}
	p, msg := parsePatch(m)
	if msg != "" {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	res, err := s.db.PatchFeed(r.Context(), id, p)
	var bad *store.InvalidURLError
	var clash *store.URLCollisionError
	switch {
	case errors.Is(err, store.ErrFeedNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, store.ErrArchiveFeed):
		writeErrorMsg(w, http.StatusConflict, "archive_feed", "the archive feed cannot be edited")
		return
	case errors.Is(err, store.ErrFolderNotFound):
		writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such folder")
		return
	case errors.As(err, &bad):
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", bad.Reason)
		return
	case errors.As(err, &clash):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "url_exists", "message": err.Error(), "feed_id": idStr(clash.Other)})
		return
	case err != nil:
		s.serverError(w, "patch feed", err)
		return
	}
	if res.RetentionChanged {
		// fire and forget: the trim is not the caller's business (a disabled feed answers ErrDisabled)
		if _, err := s.opt.Sched.Submit(sched.Priority{FeedID: id, Kind: sched.PriorityTrim}); err != nil {
			s.log.Warn("api: patch feed: trim", "err", err)
		}
	}
	if res.NeedsFetch {
		// A full job replays the fetch on the new URL even when one on the old URL
		// is in flight (its commit is dropped as stale). Wake is the fallback.
		if _, err := s.opt.Sched.Submit(sched.Priority{FeedID: id, Full: true}); err != nil {
			s.log.Warn("api: patch feed: submit fetch", "err", err)
			s.opt.Sched.Wake()
		}
	}
	if res.Notify {
		s.publishFeedChanged(id)
	}
	fd, _, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil {
		s.serverError(w, "patch feed", err)
		return
	}
	writeJSON(w, http.StatusOK, fd)
}

// ---- DELETE /api/feeds/{id}, archive purge ----

func (s *Server) deleteFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	switch r.URL.Query().Get("delete_starred") {
	case "", "0", "1":
	default:
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "delete_starred must be 0 or 1")
		return
	}
	err := s.db.DeleteFeed(r.Context(), id, r.URL.Query().Get("delete_starred") == "1")
	if errors.Is(err, store.ErrFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.serverError(w, "delete feed", err)
		return
	}
	s.publishFeedChanged(id)
	s.noteCounts()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) purgeArchive(w http.ResponseWriter, r *http.Request) {
	n, archive, err := s.db.PurgeArchiveUnstarred(r.Context())
	if err != nil {
		s.serverError(w, "purge archive", err)
		return
	}
	if n > 0 {
		s.publishFeedChanged(archive)
		s.noteCounts()
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// ---- refresh, mark-fetch-read, trimmed reset, log ----

func (s *Server) refreshFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var full bool
	switch r.URL.Query().Get("full") {
	case "", "0":
	case "1":
		full = true
	default:
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "full must be 0 or 1")
		return
	}
	fd, found, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil {
		s.serverError(w, "refresh feed", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if fd.IsArchive {
		writeErrorMsg(w, http.StatusConflict, "archive_feed", "the archive feed cannot be refreshed")
		return
	}
	ch, err := s.opt.Sched.Submit(sched.Priority{FeedID: id, Full: full})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "shutting_down")
		return
	}
	rep, got, err := s.awaitReply(r, ch, refreshWait)
	switch {
	case errors.Is(err, sched.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, "shutting_down")
		return
	case err != nil:
		return // the client went away
	case !got:
		writeJSON(w, http.StatusAccepted, map[string]any{"pending": true})
		return
	}
	switch {
	case errors.Is(rep.Err, sched.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(rep.Err, sched.ErrDisabled):
		writeErrorMsg(w, http.StatusConflict, "disabled", "the feed is disabled; enable it first")
	case errors.Is(rep.Err, sched.ErrStopped):
		writeError(w, http.StatusServiceUnavailable, "shutting_down")
	case rep.Err != nil:
		s.serverError(w, "refresh feed", rep.Err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"outcome": rep.Outcome, "new_items": rep.NewItems,
			"error_class": nilIfEmpty(rep.ErrClass), "error": nilIfEmpty(rep.ErrMsg)})
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) markFetchRead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	m, ok := readObject(w, r, "fetch_log_id")
	if !ok {
		return
	}
	logID, ok := parseID(m["fetch_log_id"])
	if !ok {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "fetch_log_id is required")
		return
	}
	res, err := s.db.MarkFetchRead(r.Context(), id, logID)
	if errors.Is(err, store.ErrLogNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.serverError(w, "mark-fetch-read", err)
		return
	}
	s.publishState(res, map[string]any{"read": true})
	writeJSON(w, http.StatusOK, map[string]any{"changed": len(res.Changed)})
}

func (s *Server) resetTrimmedUnread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	err := s.db.ResetTrimmedUnread(r.Context(), id)
	if errors.Is(err, store.ErrFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.serverError(w, "trimmed-unread reset", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) feedLog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	rows, found, err := s.db.FetchLog(r.Context(), id)
	if err != nil {
		s.serverError(w, "fetch log", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": rows})
}

// ---- folders ----

func (s *Server) folderJSON(r *http.Request, id int64) (store.UIFolder, error) {
	fs, err := s.db.UIFolders(r.Context())
	for _, f := range fs {
		if f.ID == id {
			return f, err
		}
	}
	return store.UIFolder{}, err
}

func parseFolderName(raw json.RawMessage) (string, bool) {
	n, ok := rawString(raw)
	n = strings.TrimSpace(n)
	return n, ok && n != "" && utf8.RuneCountInString(n) <= maxFolderName && !hasControl(n)
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	m, ok := readObject(w, r, "name", "position")
	if !ok {
		return
	}
	name, ok := parseFolderName(m["name"])
	if !ok {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "name must be 1 to 100 characters")
		return
	}
	pos := int64(-1)
	if raw, present := m["position"]; present && !isNull(raw) {
		if pos, ok = rawInt(raw); !ok || pos < 0 || pos > 1_000_000 {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "position must be an integer from 0 to 1000000")
			return
		}
	}
	f, err := s.db.CreateFolder(r.Context(), name, pos)
	if errors.Is(err, store.ErrFolderExists) {
		writeErrorMsg(w, http.StatusConflict, "folder_exists", "a folder with that name exists")
		return
	}
	if err != nil {
		s.serverError(w, "create folder", err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) patchFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	m, ok := readObject(w, r, "name", "position")
	if !ok {
		return
	}
	var name *string
	var pos *int64
	if raw, present := m["name"]; present {
		n, ok := parseFolderName(raw)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "name must be 1 to 100 characters")
			return
		}
		name = &n
	}
	if raw, present := m["position"]; present {
		n, ok := rawInt(raw)
		if !ok || isNull(raw) || n < 0 || n > 1_000_000 {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "position must be an integer from 0 to 1000000")
			return
		}
		pos = &n
	}
	_, err := s.db.UpdateFolder(r.Context(), id, name, pos)
	switch {
	case errors.Is(err, store.ErrFolderNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, store.ErrFolderExists):
		writeErrorMsg(w, http.StatusConflict, "folder_exists", "a folder with that name exists")
		return
	case err != nil:
		s.serverError(w, "patch folder", err)
		return
	}
	f, err := s.folderJSON(r, id)
	if err != nil {
		s.serverError(w, "patch folder", err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	moved, err := s.db.DeleteFolder(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrFolderNotFound):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case errors.Is(err, store.ErrDefaultFolder):
		writeErrorMsg(w, http.StatusConflict, "default_folder", "the default folder cannot be deleted")
		return
	case err != nil:
		s.serverError(w, "delete folder", err)
		return
	}
	for _, fid := range moved {
		s.publishFeedChanged(fid)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- GET /api/feeds/{id} ----

// getFeed returns the same FeedDetail object PATCH returns, so the editor can
// load it without a no-op PATCH. The archive feed answers like PATCH and refresh
// do (409 archive_feed): it has no editor.
func (s *Server) getFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	fd, found, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil {
		s.serverError(w, "get feed", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if fd.IsArchive {
		writeErrorMsg(w, http.StatusConflict, "archive_feed", "the archive feed cannot be edited")
		return
	}
	writeJSON(w, http.StatusOK, fd)
}

// ---- POST /api/reorder ----

const reorderMaxIDs = 20000

// reorder is POST /api/reorder: {folders?:[ids in order], feeds?:[{folder_id, ids}]}.
func (s *Server) reorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Folders []json.RawMessage `json:"folders"`
		Feeds   []struct {
			FolderID json.RawMessage   `json:"folder_id"`
			IDs      []json.RawMessage `json:"ids"`
		} `json:"feeds"`
	}
	m, ok := readObject(w, r, "folders", "feeds")
	if !ok {
		return
	}
	raw, _ := json.Marshal(m)
	if err := json.Unmarshal(raw, &body); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "folders is a list of ids and feeds a list of {folder_id, ids}")
		return
	}
	bad := func(msg string) { writeErrorMsg(w, http.StatusBadRequest, "bad_request", msg) }
	if len(body.Folders) == 0 && len(body.Feeds) == 0 {
		bad("give folders and/or feeds")
		return
	}
	total := len(body.Folders)
	var folders []int64
	for _, rw := range body.Folders {
		id, ok := parseID(rw)
		if !ok {
			bad("folders must be folder ids")
			return
		}
		folders = append(folders, id)
	}
	var feeds []store.FeedOrder
	for _, g := range body.Feeds {
		fid, ok := parseID(g.FolderID)
		if !ok {
			bad("feeds[].folder_id must be a folder id")
			return
		}
		fo := store.FeedOrder{FolderID: fid}
		for _, rw := range g.IDs {
			id, ok := parseID(rw)
			if !ok {
				bad("feeds[].ids must be feed ids")
				return
			}
			fo.IDs = append(fo.IDs, id)
		}
		total += len(fo.IDs)
		feeds = append(feeds, fo)
	}
	if total > reorderMaxIDs {
		bad("too many ids")
		return
	}
	res, err := s.db.Reorder(r.Context(), folders, feeds)
	var re *store.ErrReorder
	switch {
	case errors.As(err, &re):
		bad(re.Reason)
		return
	case errors.Is(err, store.ErrArchiveFeed):
		writeErrorMsg(w, http.StatusConflict, "archive_feed", "the archive feed cannot be reordered")
		return
	case err != nil:
		s.serverError(w, "reorder", err)
		return
	}
	for _, id := range res.Feeds {
		s.publishFeedChanged(id)
	}
	// There is no folder-level event: the UI re-reads folders with the bootstrap
	// after feed.changed, and the response below carries the new order.
	writeJSON(w, http.StatusOK, map[string]any{"changed_feeds": idStrings(res.Feeds), "changed_folders": idStrings(res.Folders)})
}
