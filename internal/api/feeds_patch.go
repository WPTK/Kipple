package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/discover"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// ---- PATCH /api/feeds/{id} ----

var patchKeys = []string{"custom_title", "folder_id", "position", "interval_minutes", "retention", "auto_read_days", "fulltext", "dedup_mode",
	"user_agent", "http_auth", "ignore_http_cache", "disable_http2", "allow_insecure_tls", "allow_private_net", "enabled", "url", "expected_url"}

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
			if t, ok = titleInput(t, ok); !ok {
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
		case "auto_read_days":
			if null {
				p.Cols[k] = nil
				continue
			}
			n, ok := rawInt(raw)
			if !ok || n < 0 || n > 365 {
				return p, "auto_read_days must be null (use the library setting) or 0 to 365 (0 = off for this feed)"
			}
			p.Cols[k] = n
		case "retention":
			if null {
				p.Cols[k] = nil
				continue
			}
			n, ok := rawInt(raw)
			if !ok || !isRetentionChoice(n) {
				return p, "retention must be null, 0 (unlimited), " + retentionChoicesText()
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
		case "expected_url":
			v, ok := rawString(raw)
			if !ok || v == "" {
				return p, "expected_url must be a non-empty string"
			}
			p.ExpectURL = &v
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
	if p.URL != nil {
		if code, msg := s.resolveEditedURL(r.Context(), id, &p); code != "" {
			status := http.StatusUnprocessableEntity
			if code == "url_exists" {
				status = http.StatusConflict // the same answer as the patch's own collision
			}
			writeErrorMsg(w, status, code, msg)
			return
		}
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
	case errors.Is(err, store.ErrURLChanged):
		writeErrorMsg(w, http.StatusConflict, "url_changed", "the feed's address is not the one you saw; nothing was changed")
		return
	case errors.Is(err, store.ErrFolderNotFound):
		writeErrorMsg(w, http.StatusBadRequest, "folder_not_found", "no such folder")
		return
	case errors.As(err, &bad) && bad.Kind == store.URLPrivate:
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", msgPrivateAddressEdit)
		return
	case errors.As(err, &bad):
		writeErrorMsg(w, http.StatusBadRequest, "invalid_url", bad.Reason)
		return
	case errors.As(err, &clash):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "url_exists", "feed_id": idStr(clash.Other),
			"message": s.existingFeedMessage(r.Context(), clash.Other, "That address cannot be used for this feed too.")})
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
		if _, moved := p.Cols["folder_id"]; moved {
			s.publishFolderChanged(0)
		}
	}
	fd, _, err := s.db.FeedDetail(r.Context(), id, s.statusEnv())
	if err != nil {
		s.serverError(w, "patch feed", err)
		return
	}
	writeJSON(w, http.StatusOK, fd)
}

// resolveEditedURL runs the add dialog's discovery on a feed's edited URL, so an edit to a site or
// page address gets the feed that page links, as adding it would (the first-fetch discovery is
// only for a URL as it was given). A page that links one feed puts that feed's URL in p; a page
// that links several, or none, is refused with a code and message for the editor. Anything else
// (a feed, a site that cannot be reached now, an address the patch refuses anyway) leaves the URL
// as typed, as an edit always did: the fetch that follows reports any problem.
func (s *Server) resolveEditedURL(ctx context.Context, id int64, p *store.FeedPatch) (code, msg string) {
	fd, ok, err := s.db.FeedDetail(ctx, id, s.statusEnv())
	if err != nil || !ok {
		return "", ""
	}
	flag := func(key string, cur bool) bool {
		switch v := p.Cols[key].(type) {
		case bool:
			return v
		case int:
			return v != 0
		case int64:
			return v != 0
		}
		return cur
	}
	// A move to another site drops the exceptions the patch does not set itself (store.PatchFeed), so a
	// probe runs without them: no request goes where the saved feed may not go. That holds for the
	// address typed and, separately, for the feed a page links, which may be on yet another site.
	oldHost := fd.Host // the stored host, as PatchFeed compares it, so the probe and the patch agree
	keepExceptions := func(h string) (private, insecure bool) {
		private, insecure = flag("allow_private_net", fd.AllowPrivateNet), flag("allow_insecure_tls", fd.AllowInsecureTLS)
		if !fetch.SameSite(oldHost, h) {
			if _, set := p.Cols["allow_private_net"]; !set {
				private = false
			}
			if _, set := p.Cols["allow_insecure_tls"]; !set {
				insecure = false
			}
		}
		return private, insecure
	}
	norm, _, host, err := store.ValidateFeedURL(*p.URL, flag("allow_private_net", fd.AllowPrivateNet))
	if err != nil || norm == fd.URL {
		return "", ""
	}
	private, insecure := keepExceptions(host)
	// The feed a page links keeps those exceptions only on its own terms: one the patch sets is for the
	// address typed and its site, as on Add; one kept from the feed is for the old address's site.
	linkedExceptions := func(h string) (private, insecure bool) {
		private, insecure = keepExceptions(h)
		if !fetch.SameSite(host, h) {
			if _, set := p.Cols["allow_private_net"]; set {
				private = false
			}
			if _, set := p.Cols["allow_insecure_tls"]; set {
				insecure = false
			}
		}
		return private, insecure
	}
	if norm, _, _, err = store.ValidateFeedURL(*p.URL, private); err != nil {
		return "", ""
	}
	if _, found, err := s.db.FindFeedID(ctx, norm); err != nil || found {
		return "", "" // the patch answers url_exists (or it is this feed's own old address)
	}
	dctx, cancel := context.WithTimeout(ctx, discoverWait)
	defer cancel()
	ua, retryUA := store.ResolveUserAgent(s.db.FetchSettings(ctx), "", false)
	if ua == "" {
		ua = s.outgoingUA()
	}
	rt := fetch.ScopedTransport(s.opt.Guard, host, private, insecure, flag("disable_http2", fd.DisableHTTP2))
	found, err := discover.Find(dctx, rt, ua, retryUA, norm, private)
	switch {
	case errors.Is(err, discover.ErrNoFeed):
		return discoveryError(err)
	case err == nil && (found.IsFeed || len(found.Candidates) == 1):
		// The same two checks Add makes, in the same order: the linked address itself may be a feed you
		// have, and a feed that answers through a redirect to one you have is a duplicate whether it was
		// typed or linked from a page.
		if !found.IsFeed && len(found.Candidates) > 0 {
			if other, exists, ferr := s.db.FindFeedID(ctx, found.Candidates[0].URL); ferr == nil && exists && other != id {
				return "url_exists", s.existingFeedMessage(ctx, other, "The page you entered links it.")
			}
		}
		owner, refused, oerr := s.redirectOwner(ctx, found, ua, retryUA, func(h string) (http.RoundTripper, bool) {
			priv, ins := linkedExceptions(h)
			return fetch.ScopedTransport(s.opt.Guard, h, priv, ins, flag("disable_http2", fd.DisableHTTP2)), priv
		})
		if oerr == nil && refused != "" {
			return "private_address", privateRefusal(host, found.Candidates[0].URL, refused, true)
		}
		if oerr == nil && owner != 0 && owner != id {
			return "url_exists", s.existingFeedMessage(ctx, owner, redirectTail(found.IsFeed))
		}
		if !found.IsFeed {
			u := found.Candidates[0].URL
			p.URL = &u
			// An exception the patch sets is saved only if the linked feed keeps it (linkedExceptions);
			// one it does not set is reset by the store when the feed moves to another site.
			priv, ins := linkedExceptions(linkedHost(found))
			for key, keep := range map[string]bool{"allow_private_net": priv, "allow_insecure_tls": ins} {
				if _, set := p.Cols[key]; set && !keep {
					p.Cols[key] = false
				}
			}
		}
		return "", ""
	case err != nil || found.IsFeed || len(found.Candidates) == 0:
		return "", ""
	}
	urls := make([]string, len(found.Candidates))
	for i, c := range found.Candidates {
		urls[i] = c.URL
	}
	return "several_feeds", "That address is a web page that links several feeds: " + strings.Join(urls, ", ") +
		". Enter the address of the one you want."
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
	// A large feed is emptied in many short batches (store.DeleteFeed); a client
	// that goes away mid-delete must not leave it half emptied, so the delete
	// runs to the end, bounded by store.DeleteContext (each batch still has the
	// writer's own deadline; a delete cut short leaves the feed marked, never
	// fetched, and the next delete finishes it).
	dctx, cancel := store.DeleteContext(r.Context())
	defer cancel()
	err := s.db.DeleteFeed(dctx, id, r.URL.Query().Get("delete_starred") == "1")
	if errors.Is(err, store.ErrFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, store.ErrArchiveHasStarred) {
		writeErrorMsg(w, http.StatusConflict, "archive_has_starred", "the archive feed holds starred items; pass ?delete_starred=1 to really delete them")
		return
	}
	if err != nil {
		// Some batches may have committed: the counts changed either way.
		s.publishFeedChanged(id)
		s.noteCounts()
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

// keepRedirect is POST /api/feeds/{id}/redirect/keep: the user keeps a feed that redirects to a feed they
// already have, so Feed Health stops flagging it (store.KeepRedirect).
func (s *Server) keepRedirect(w http.ResponseWriter, r *http.Request) {
	id, ok := pathItemID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	err := s.db.KeepRedirect(r.Context(), id)
	if errors.Is(err, store.ErrFeedNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, store.ErrNoRedirectToKeep) {
		writeErrorMsg(w, http.StatusConflict, "no_redirect", "This feed does not redirect to another feed you have, so there is nothing to keep.")
		return
	}
	if err != nil {
		s.serverError(w, "keep redirect", err)
		return
	}
	s.publishFeedChanged(id)
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

// maxBulkDelete bounds one POST /api/feeds/delete (the id list, not the data it removes).
const maxBulkDelete = 20000

// feedIDList reads the ids of a bulk feed request: between 1 and maxBulkDelete, each a positive integer, a repeat
// once. It answers 400 itself and reports false when the list is not that.
func feedIDList(w http.ResponseWriter, raw []json.RawMessage) ([]int64, bool) {
	if len(raw) == 0 || len(raw) > maxBulkDelete {
		writeErrorMsg(w, http.StatusBadRequest, "bad_request", "give between 1 and "+strconv.Itoa(maxBulkDelete)+" feed ids")
		return nil, false
	}
	ids := make([]int64, 0, len(raw))
	seen := make(map[int64]bool, len(raw))
	for _, r := range raw {
		id, ok := parseID(r)
		if !ok {
			writeErrorMsg(w, http.StatusBadRequest, "bad_request", "ids is a list of feed ids")
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, true
}

// POST /api/feeds/delete-preview {ids}: how many articles deleting these feeds would remove, for the confirmation to
// say so. It changes nothing.
func (s *Server) deletePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []json.RawMessage `json:"ids"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	ids, ok := feedIDList(w, body.IDs)
	if !ok {
		return
	}
	n, err := s.db.FeedArticleCount(r.Context(), ids)
	if err != nil {
		s.serverError(w, "count feed articles", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"articles": n})
}

// POST /api/feeds/delete {ids, delete_starred}: delete many feeds as one server-side commitment. The whole set is
// marked in one transaction, then removed here without the request's cancellation: closing the tab part-way
// cannot leave the library half deleted (see store.DeleteFeeds). Answers {"deleted": n}, the feeds marked: ids that
// are already gone are not counted, and neither is the archive feed, which is never marked (it is removed last, and
// only when the other feeds' starred items did not fill it).
func (s *Server) deleteFeeds(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs           []json.RawMessage `json:"ids"`
		DeleteStarred bool              `json:"delete_starred"`
	}
	if !decodeBody(w, r, &body, false) {
		return
	}
	ids, ok := feedIDList(w, body.IDs)
	if !ok {
		return
	}
	present, marked, err := s.db.MarkFeedsDeleting(r.Context(), ids, body.DeleteStarred)
	if errors.Is(err, store.ErrArchiveHasStarred) {
		writeErrorMsg(w, http.StatusConflict, "archive_has_starred", "the archive feed holds starred items; set delete_starred to really delete them")
		return
	}
	if err != nil {
		s.serverError(w, "delete feeds", err)
		return
	}
	// The feeds are gone from every list now. Purging their items can take minutes for thousands of feeds, longer than a
	// request may last, so the server finishes it on its own; a stop in the middle is finished by the next start.
	s.announceFeedsChanged(present)
	s.noteCounts()
	if len(present) > 0 && s.bgStart() {
		go func() {
			defer s.apply.wg.Done()
			ctx, cancel := context.WithTimeout(s.apply.ctx, time.Hour)
			defer cancel()
			if _, err := s.db.FinishFeedDeletes(ctx, present, body.DeleteStarred); err != nil && ctx.Err() == nil {
				s.log.Warn("finishing a bulk feed delete", "err", err)
			}
			s.noteCounts()
		}()
	} else if len(present) > 0 {
		s.log.Warn("api: bulk feed delete: shutting down, the purge of the marked feeds finishes at the next start", "feeds", len(present))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": marked})
}

// bulkAnnounceMax is the most feeds announced one event each: a client's event buffer holds 64 (events.subBuffer), so
// a larger set is one resync instead.
const bulkAnnounceMax = 32

// announceFeedsChanged tells every client that the feeds in ids changed: one event each, or a single resync for a set
// larger than the hub's batch (the clients refetch anyway, and a burst would overflow their buffers).
func (s *Server) announceFeedsChanged(ids []int64) {
	if s.opt.Hub == nil {
		return
	}
	if len(ids) > bulkAnnounceMax {
		s.opt.Hub.Publish("resync", map[string]any{})
		return
	}
	for _, id := range ids {
		s.publishFeedChanged(id)
	}
}
