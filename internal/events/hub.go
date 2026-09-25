// Package events is the SSE hub (design §4.10, §7.3): subscribers with a
// bounded buffer, a replay ring for Last-Event-ID, and no blocking publishers.
// The HTTP endpoint that drains it arrives with the web API.
package events

import (
	"encoding/json"
	"sync"

	"github.com/WPTK/kipple/internal/clock"
)

const (
	subBuffer = 64
	ringSize  = 500
)

// Event is one SSE message: `event: <Type>`, `id: <ID>`, `data: <Data>`.
type Event struct {
	ID   uint64
	Type string
	Data json.RawMessage
}

// Sub is one subscriber. Read events from C until it is closed.
type Sub struct {
	C    chan Event
	hub  *Hub
	dead bool
}

// Hub fans events out to subscribers. All methods are safe for concurrent use
// and none of them ever blocks on a subscriber.
type Hub struct {
	mu     sync.Mutex
	seq    uint64
	ring   []Event
	subs   map[*Sub]struct{}
	closed bool
}

// New returns an empty hub on the wall clock.
func New() *Hub { return NewWithClock(clock.Real{}) }

// NewWithClock returns an empty hub whose event ids start at the clock's
// current time in microseconds, so ids stay monotonic across process restarts
// and a client's Last-Event-ID from a previous run is never ahead of a new one.
func NewWithClock(clk clock.Clock) *Hub {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Hub{subs: map[*Sub]struct{}{}, seq: uint64(max(clk.Now().UnixMicro(), 0))}
}

// Publish marshals data and delivers the event to every subscriber. A
// subscriber whose buffer is full is dropped: it receives one final `resync`
// event and its channel is closed, so the client reconnects and refetches.
func (h *Hub) Publish(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = []byte("{}")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.seq++
	ev := Event{ID: h.seq, Type: typ, Data: raw}
	h.ring = append(h.ring, ev)
	if len(h.ring) > ringSize {
		h.ring = append(h.ring[:0:0], h.ring[len(h.ring)-ringSize:]...)
	}
	for s := range h.subs {
		select {
		case s.C <- ev:
		default:
			h.dropLocked(s, true)
		}
	}
}

// Subscribe registers a subscriber. With lastID > 0 the events after it are
// replayed from the ring; when the ring no longer reaches back that far, or the
// replay would overflow the buffer, the subscriber gets a `resync` instead.
func (h *Hub) Subscribe(lastID uint64) *Sub {
	s := &Sub{C: make(chan Event, subBuffer), hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(s.C)
		s.dead = true
		return s
	}
	if lastID > h.seq {
		// the client remembers an id from a previous process (or the future):
		// nothing can be replayed, so make it refetch
		s.C <- Event{ID: h.seq, Type: "resync", Data: json.RawMessage("{}")}
	} else if lastID > 0 && lastID < h.seq {
		var replay []Event
		for _, ev := range h.ring {
			if ev.ID > lastID {
				replay = append(replay, ev)
			}
		}
		covered := len(h.ring) > 0 && h.ring[0].ID <= lastID+1
		if !covered || len(replay) >= subBuffer {
			s.C <- Event{ID: h.seq, Type: "resync", Data: json.RawMessage("{}")}
		} else {
			for _, ev := range replay {
				s.C <- ev
			}
		}
	}
	h.subs[s] = struct{}{}
	return s
}

// LastID is the id of the most recent event (the seed before the first).
func (h *Hub) LastID() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Close drops one subscriber and closes its channel. It is idempotent.
func (s *Sub) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.dropLocked(s, false)
}

// Close closes every subscriber channel so SSE handlers return at once, and
// turns later publishes into no-ops.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		h.dropLocked(s, false)
	}
}

func (h *Hub) dropLocked(s *Sub, resync bool) {
	if s.dead {
		return
	}
	s.dead = true
	delete(h.subs, s)
	if resync {
		// make room for the resync marker if the buffer is full
		select {
		case s.C <- Event{ID: h.seq, Type: "resync", Data: json.RawMessage("{}")}:
		default:
			select {
			case <-s.C:
			default:
			}
			select {
			case s.C <- Event{ID: h.seq, Type: "resync", Data: json.RawMessage("{}")}:
			default:
			}
		}
	}
	close(s.C)
}
