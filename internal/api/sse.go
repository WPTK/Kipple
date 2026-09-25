package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// events is GET /api/events (design §4.10). The server's WriteTimeout (60 s)
// and ReadTimeout would kill a long-lived stream, so the handler clears the
// read deadline and gives every write its own short deadline through
// http.ResponseController instead.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})

	var last uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, _ = strconv.ParseUint(v, 10, 64)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")

	sub := s.opt.Hub.Subscribe(last)
	defer sub.Close()

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
		case <-tick.C:
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
