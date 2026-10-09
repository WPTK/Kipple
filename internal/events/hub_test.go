package events

import (
	"testing"
	"time"

	"github.com/WPTK/kipple/internal/clock"

	"github.com/stretchr/testify/require"
)

// newHub starts ids at 0 so tests can use small literal ids.
func newHub() *Hub { return NewWithClock(clock.NewFake(time.Unix(0, 0))) }

func drain(s *Sub) []Event {
	var out []Event
	for ev := range s.C {
		out = append(out, ev)
	}
	return out
}

func TestPublishSubscribeOrder(t *testing.T) {
	h := newHub()
	s := h.Subscribe(0)
	h.Publish("a", map[string]int{"n": 1})
	h.Publish("b", nil)
	ev := <-s.C
	require.Equal(t, uint64(1), ev.ID)
	require.Equal(t, "a", ev.Type)
	require.JSONEq(t, `{"n":1}`, string(ev.Data))
	require.Equal(t, uint64(2), (<-s.C).ID)
}

func TestOverflowDropsWithResync(t *testing.T) {
	h := newHub()
	slow := h.Subscribe(0)
	fast := h.Subscribe(0)
	for i := 0; i < subBuffer; i++ {
		h.Publish("e", i)
		<-fast.C
	}
	h.Publish("e", "overflow") // slow's buffer is full: it is dropped
	got := drain(slow)
	require.Equal(t, "resync", got[len(got)-1].Type)
	require.Len(t, got, subBuffer)
	require.Equal(t, "overflow", func() string { ev := <-fast.C; return string(ev.Data[1 : len(ev.Data)-1]) }())
	require.Len(t, h.subs, 1, "the dropped subscriber is unregistered")
}

func TestReplayFromRing(t *testing.T) {
	h := newHub()
	for i := 0; i < 10; i++ {
		h.Publish("e", i)
	}
	s := h.Subscribe(7)
	h.Publish("e", 10)
	s.Close()
	got := drain(s)
	require.Len(t, got, 4)
	require.Equal(t, uint64(8), got[0].ID)
	require.Equal(t, uint64(11), got[3].ID)
}

func TestReplayBeyondRingResyncs(t *testing.T) {
	h := newHub()
	for i := 0; i < ringSize+50; i++ {
		h.Publish("e", i)
	}
	s := h.Subscribe(3)
	require.Equal(t, "resync", (<-s.C).Type)
	s2 := h.Subscribe(uint64(ringSize + 45))
	require.Equal(t, uint64(ringSize+46), (<-s2.C).ID, "recent ids replay normally")
}

func TestCloseClosesEveryChannel(t *testing.T) {
	h := newHub()
	a, b := h.Subscribe(0), h.Subscribe(0)
	h.Close()
	h.Publish("late", nil) // no-op, no panic
	_, ok := <-a.C
	require.False(t, ok)
	_, ok = <-b.C
	require.False(t, ok)
	a.Close() // idempotent
	late := h.Subscribe(0)
	_, ok = <-late.C
	require.False(t, ok)
}

func TestSubscribeAheadOfSeqResyncs(t *testing.T) {
	h := newHub()
	for i := 0; i < 5; i++ {
		h.Publish("e", i)
	}
	s := h.Subscribe(9999) // e.g. an id from before a restart
	ev := <-s.C
	require.Equal(t, "resync", ev.Type)
	require.Equal(t, uint64(5), ev.ID)
	h.Publish("e", 5)
	require.Equal(t, uint64(6), (<-s.C).ID)

	same := h.Subscribe(6) // caught up: nothing to send
	select {
	case ev := <-same.C:
		t.Fatalf("unexpected event %v", ev)
	default:
	}
}

func TestSeqSeededFromClockStaysMonotonicAcrossRestart(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	h1 := NewWithClock(clk)
	h1.Publish("e", 1)
	last := h1.LastID()
	require.Equal(t, uint64(clk.Now().UnixMicro())+1, last)

	clk.Advance(2 * time.Second) // process restarts later
	h2 := NewWithClock(clk)
	h2.Publish("e", 1)
	require.Greater(t, h2.LastID(), last)

	// a client holding the old id replays or resyncs, never silently gets nothing
	s := h2.Subscribe(last)
	require.Equal(t, "resync", (<-s.C).Type, "old id is behind the ring's start")
}

func TestRingBoundedByBytesAsWellAsCount(t *testing.T) {
	h := newHub()
	big := make([]byte, 100<<10)
	for i := range big {
		big[i] = 'x'
	}
	payload := string(big)
	for i := 0; i < 30; i++ {
		h.Publish("big", payload)
	}
	require.LessOrEqual(t, h.ringUsed, ringBytes)
	require.Less(t, len(h.ring), 12, "about 1 MiB of 100 KiB events, not 30")
	require.Equal(t, uint64(30), h.ring[len(h.ring)-1].ID, "the newest event is kept")

	// an old id no longer covered by the byte-bounded ring resyncs
	s := h.Subscribe(2)
	require.Equal(t, "resync", (<-s.C).Type)

	// small events are still limited by count, and the byte total stays exact
	h2 := newHub()
	for i := 0; i < ringSize+20; i++ {
		h2.Publish("e", i)
	}
	require.Len(t, h2.ring, ringSize)
	sum := 0
	for _, ev := range h2.ring {
		sum += eventSize(ev)
	}
	require.Equal(t, sum, h2.ringUsed)

	// one event larger than the whole budget is not retained
	h3 := newHub()
	h3.Publish("huge", string(make([]byte, ringBytes+1)))
	require.Empty(t, h3.ring)
	require.Zero(t, h3.ringUsed)
}

func TestSubscribeRefusesBeyondTheLimit(t *testing.T) {
	h := newHub()
	subs := make([]*Sub, 0, MaxSubscribers)
	for i := 0; i < MaxSubscribers; i++ {
		s := h.Subscribe(0)
		require.NotNil(t, s)
		subs = append(subs, s)
	}
	require.Nil(t, h.Subscribe(0), "one more than the limit is refused")
	subs[0].Close()
	s := h.Subscribe(0)
	require.NotNil(t, s, "a closed subscriber frees its place")
	s.Close()
}

func TestSubscribeOnAClosedHubReturnsAClosedSubscriberEvenWhenFull(t *testing.T) {
	h := newHub()
	for i := 0; i < MaxSubscribers; i++ {
		require.NotNil(t, h.Subscribe(0))
	}
	h.Close()
	s := h.Subscribe(0)
	require.NotNil(t, s)
	_, open := <-s.C
	require.False(t, open)
}
