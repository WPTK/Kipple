package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A panic inside a chunk's transaction must not leak pending marks: they would
// hide the items from the Reader API with nothing left to clear them.
func TestCommitPanicClearsHeldMarks(t *testing.T) {
	t.Cleanup(func() { commitChunkTestHook = nil })

	// One chunk: it rolls back.
	e := newEnv(t)
	id := e.addFeed("https://a.example/feed")
	res := e.okResult(e.snap(id), rss(numbered(3)...))
	res.HoldUIDs = map[string]bool{}
	for _, u := range uidsByGUID(res) {
		res.HoldUIDs[u] = true
	}
	commitChunkTestHook = func() { panic("injected") }
	require.PanicsWithValue(t, "injected", func() { _, _ = e.db.CommitFetch(e.ctx, res) })
	commitChunkTestHook = nil
	require.Equal(t, "[]", e.db.HoldPending())
	require.Equal(t, 0, e.count("SELECT count(*) FROM items WHERE feed_id = ?", id), "rolled back")

	// Several chunks, the second panics: the first chunk's items are durable
	// but the scheduler cannot queue them, so their marks go too.
	e2 := newEnv(t)
	id2 := e2.addFeed("https://b.example/feed")
	res2 := e2.okResult(e2.snap(id2), rss(numbered(chunkThreshold+10)...))
	res2.HoldUIDs = map[string]bool{}
	for _, u := range uidsByGUID(res2) {
		res2.HoldUIDs[u] = true
	}
	calls := 0
	commitChunkTestHook = func() {
		if calls++; calls == 2 {
			panic("second chunk")
		}
	}
	require.Panics(t, func() { _, _ = e2.db.CommitFetch(e2.ctx, res2) })
	commitChunkTestHook = nil
	require.Equal(t, chunkSize, e2.count("SELECT count(*) FROM items WHERE feed_id = ?", id2), "the first chunk committed")
	require.Equal(t, "[]", e2.db.HoldPending())
}
