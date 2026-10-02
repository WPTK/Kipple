package api

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/WPTK/kipple/internal/sched"
	"github.com/WPTK/kipple/internal/store"
)

// status is GET /api/status: the SSE fallback.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	schedRuns, inflight := s.opt.Sched.Status()
	unread, err := s.db.UnreadTotal(r.Context())
	if err != nil {
		s.serverError(w, "unread total", err)
		return
	}
	muted, err := s.db.MutedCount(r.Context())
	if err != nil {
		s.serverError(w, "muted count", err)
		return
	}
	// Like bootstrap `runs`: the scheduler's runs plus the active filter apply run.
	runs := make([]any, 0, len(schedRuns)+1)
	for _, run := range schedRuns {
		runs = append(runs, run)
	}
	if ar := s.applyStatus(); ar != nil {
		runs = append(runs, ar)
	}
	if ar := s.autoReadStatus(); ar != nil {
		runs = append(runs, ar)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs, "inflight": inflight, "unread_total": unread, "muted": muted})
}

// healthFeed is store.FeedHealth plus derived fields.
type healthFeed struct {
	store.FeedHealth
	// Status is store.FeedStatus (design §4.6), the same value bootstrap carries.
	Status string `json:"status"`
	// RedirectPending is true while a permanent (301) redirect is seen but not yet
	// acted on; the migration itself clears it and leaves a fetch_log note.
	RedirectPending bool `json:"redirect_pending"`
	// HostThrottledUntil is the host's politeness deadline (unix s), when in the future.
	HostThrottledUntil *int64   `json:"host_throttled_until"`
	Notices            []string `json:"notices"`
}

// statusEnv is the clock and host deadlines FeedStatus needs.
func (s *Server) statusEnv() store.StatusEnv {
	return store.StatusEnv{Now: s.now(), HostUntil: s.opt.Sched.HostHolds()}
}

func decorate(h store.FeedHealth, env store.StatusEnv) healthFeed {
	last := h.CreatedAt
	if h.LastNewItemsAt != nil {
		last = *h.LastNewItemsAt
	}
	until := env.HostUntil[h.Host]
	f := healthFeed{FeedHealth: h, Notices: []string{}}
	f.Status = store.FeedStatus(store.StatusRow{DisabledReason: h.DisabledReason, Enabled: h.Enabled,
		ConsecutiveFailures: h.ConsecutiveFailures, RedirectKind: h.RedirectKind, RedirectTo: h.RedirectTo,
		LastNewItemsAt: last}, until, env.Now)
	if until.After(env.Now) {
		u := until.Unix()
		f.HostThrottledUntil = &u
	}
	if h.RedirectKind != nil && *h.RedirectKind == "permanent" && h.RedirectTo != nil {
		f.RedirectPending = true
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
		s.serverError(w, "feed health", err)
		return
	}
	env := s.statusEnv()
	feeds := make([]healthFeed, len(rows))
	for i, h := range rows {
		feeds[i] = decorate(h, env)
	}
	unread, err := s.db.UnreadTotal(r.Context())
	if err != nil {
		s.serverError(w, "unread total", err)
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
	snap := s.db.SnapshotStatus(r.Context())
	type snapshot struct {
		LastAt    *int64  `json:"last_at"`
		LastError *string `json:"last_error"`
	}
	sn := snapshot{}
	if snap.LastAt > 0 {
		sn.LastAt = &snap.LastAt
	}
	if snap.LastError != "" {
		sn.LastError = &snap.LastError
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"feeds": feeds, "clients": clients, "unread_total": unread,
		"snapshot": sn,
		"clock":    map[string]int64{"ahead_s": int64(s.db.IDs().Skew() / time.Second)},
		"db":       s.diskUsage(),
	})
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
		s.serverError(w, "refresh", err)
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
