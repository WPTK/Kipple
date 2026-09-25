package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// status is GET /api/status: the SSE fallback.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	runs, inflight := s.opt.Sched.Status()
	unread, err := s.db.UnreadTotal(r.Context())
	if err != nil {
		s.log.Error("api: unread total", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "inflight": inflight, "unread_total": unread})
}

// healthFeed is store.FeedHealth plus derived fields.
type healthFeed struct {
	store.FeedHealth
	// Status is disabled reason ("user", "gone"), "failing" while
	// consecutive_failures > 0, else "ok".
	Status string `json:"status"`
	// Migrated is true after a permanent (301) redirect moved the feed's URL.
	Migrated bool     `json:"migrated"`
	Notices  []string `json:"notices"`
}

func decorate(h store.FeedHealth) healthFeed {
	f := healthFeed{FeedHealth: h, Status: "ok", Notices: []string{}}
	switch {
	case h.DisabledReason != nil:
		f.Status = *h.DisabledReason
	case !h.Enabled:
		f.Status = "disabled"
	case h.ConsecutiveFailures > 0:
		f.Status = "failing"
	}
	if h.RedirectKind != nil && *h.RedirectKind == "permanent" && h.RedirectTo != nil {
		f.Migrated = true
		f.Notices = append(f.Notices, "moved permanently (301) to "+*h.RedirectTo)
	} else if h.RedirectKind != nil && *h.RedirectKind == "temporary" && h.RedirectTo != nil {
		f.Notices = append(f.Notices, "temporary redirect to "+*h.RedirectTo)
	}
	if h.TrimmedUnreadCount > 0 {
		f.Notices = append(f.Notices, fmt.Sprintf("%d unread items trimmed before you read them", h.TrimmedUnreadCount))
	}
	if h.LastErrorAt != nil && h.LastSuccessAt != nil && *h.LastErrorAt < *h.LastSuccessAt {
		f.Notices = append(f.Notices, "last error resolved")
	}
	return f
}

// healthFeeds is GET /api/health/feeds.
func (s *Server) healthFeeds(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.FeedHealth(r.Context())
	if err != nil {
		s.log.Error("api: feed health", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	feeds := make([]healthFeed, len(rows))
	for i, h := range rows {
		feeds[i] = decorate(h)
	}
	unread, err := s.db.UnreadTotal(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	type client struct {
		Family     string `json:"family"`
		LastSeenAt int64  `json:"last_seen_at"`
	}
	clients := []client{}
	if s.opt.Clients != nil {
		for fam, t := range s.opt.Clients() {
			clients = append(clients, client{fam, t.Unix()})
		}
		sort.Slice(clients, func(i, j int) bool { return clients[i].Family < clients[j].Family })
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": feeds, "clients": clients, "unread_total": unread})
}

// refresh is POST /api/refresh: a manual run over every enabled feed, or the
// active manual run joined (design §4.9).
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	info, err := s.opt.Sched.RefreshAll()
	if err != nil {
		if errors.Is(err, sched.ErrStopped) {
			writeError(w, http.StatusServiceUnavailable, "shutting_down")
			return
		}
		s.log.Error("api: refresh", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": fmt.Sprint(info.RunID), "total": info.Total, "joined": info.Joined})
}

// feedIcon is GET /api/feeds/{id}/icon. The ?h=<hash> in bootstrap's URL only
// busts the cache, so the week-long max-age is safe.
func (s *Server) feedIcon(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	data, ct, ok, err := s.db.FeedIconAny(r.Context(), id)
	if err != nil {
		s.serverError(w, "feed icon", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !isImageType(ct) {
		ct = http.DetectContentType(data)
		if !isImageType(ct) {
			ct = "application/octet-stream"
		}
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "private, max-age=604800")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'none'") // an SVG icon must never run script
	_, _ = w.Write(data)
}

// isImageType reports an image/* type; SVG is allowed here because the CSP above sandboxes it.
func isImageType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	return strings.HasPrefix(ct, "image/")
}
