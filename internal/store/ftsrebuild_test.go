package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The FTS rebuild is one statement that cannot be batched: it gets its own
// deadline, longer than the 10 s write deadline and under the HTTP server's
// 60 s WriteTimeout, and it waits behind the commit gate.
func TestRebuildFTSHasItsOwnDeadline(t *testing.T) {
	e := newEnv(t)
	e.loadFeed("http://a.example/feed", 20)

	var dl time.Time
	rebuildFTSHook = func(ctx context.Context) { dl, _ = ctx.Deadline() }
	t.Cleanup(func() { rebuildFTSHook = nil })
	require.NoError(t, e.db.RebuildFTS(e.ctx))
	require.WithinDuration(t, time.Now().Add(FTSRebuildTimeout), dl, 2*time.Second)
	require.Greater(t, FTSRebuildTimeout, writeTimeout)
	require.Less(t, FTSRebuildTimeout, 60*time.Second)
	e.exec("INSERT INTO items_fts(items_fts) VALUES('integrity-check')")
	require.Equal(t, 20, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'body'"))

	release, err := e.db.AcquireGate(e.ctx)
	require.NoError(t, err)
	short, cancel := context.WithTimeout(e.ctx, 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, e.db.RebuildFTS(short), context.DeadlineExceeded, "it queues behind the commit gate")
	release()
}
