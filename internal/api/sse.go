package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// events is GET /api/events (design §4.10). The server's WriteTimeout (60 s)
// and ReadTimeout would kill a long-lived stream, so the handler clears the
// read deadline and gives every write its own short deadline through
// http.ResponseController instead.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})

	// A native EventSource resends Last-Event-ID itself; a client that recreates
	// its EventSource (a watchdog reconnect) cannot set the header, so the same
	// cursor is accepted as ?last_event_id=. The header wins when both are sent.
	var last uint64
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("last_event_id")
	}
	if v != "" {
		last, _ = strconv.ParseUint(v, 10, 64)
	}
	var session string
	if c, err := r.Cookie(cookieName); err == nil {
		session = sessionID(c.Value)
	}
	sub := s.opt.Hub.Subscribe(last)
	if sub == nil {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "too many event streams", http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")

	write := func(format string, args ...any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeSlack))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write("retry: 3000\n: connected\n\n") {
		return
	}

	// An open stream must not outlive what admitted it: its session ("Sign out
	// other sessions" deletes rows) and, in open mode, the open gate (a device
	// that left the tailnet). Checked at every
	// heartbeat, and at once when the mode or a security setting changes here.
	allowed := func() bool {
		if ok, err := s.db.SessionActive(r.Context(), session, s.now().Unix()); err == nil && !ok {
			return false
		}
		if snap := s.snapshot(r.Context()); snap.mode == store.AuthOpen || snap.failed {
			return s.gateRefusal(r, snap, false) == ""
		}
		return true
	}
	changed := s.modeChanged()

	tick := time.NewTicker(s.opt.Heartbeat)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			if !write("event: %s\nid: %d\ndata: %s\n\n", ev.Type, ev.ID, ev.Data) {
				return
			}
		case <-changed:
			changed = s.modeChanged()
			if !allowed() {
				return
			}
		case <-tick.C:
			if !allowed() {
				return
			}
			// The comment keeps proxies open; the named event (no id, so it never
			// moves Last-Event-ID) is what an EventSource listener can observe, so
			// a client watchdog can tell a hung stream from a quiet one.
			if !write(": ping\n\nevent: heartbeat\ndata: {\"t\":%d}\n\n", s.now().Unix()) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
