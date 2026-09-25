package events

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func drain(s *Sub) []Event {
	var out []Event
	for ev := range s.C {
		out = append(out, ev)
	}
	return out
}

func TestPublishSubscribeOrder(t *testing.T) {
	h := New()
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
	h := New()
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
	h := New()
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
	h := New()
	for i := 0; i < ringSize+50; i++ {
		h.Publish("e", i)
	}
	s := h.Subscribe(3)
	require.Equal(t, "resync", (<-s.C).Type)
	s2 := h.Subscribe(uint64(ringSize + 45))
	require.Equal(t, uint64(ringSize+46), (<-s2.C).ID, "recent ids replay normally")
}

func TestCloseClosesEveryChannel(t *testing.T) {
	h := New()
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
