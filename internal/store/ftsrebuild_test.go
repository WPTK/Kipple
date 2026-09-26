package store

import (
	"context"
	"database/sql"
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

// Review finding: the rebuild holds the writer for up to 45 s and every other write (edit-tag,
// mark-read, sessions, settings) waited out its 10 s deadline and failed with a 500. Now they get
// ErrMaintenance at once (503 + Retry-After upstream), while gated writers (fetch commits,
// maintenance batches) still queue on the gate and succeed after the rebuild.
func TestWritesDuringRebuildGetErrMaintenance(t *testing.T) {
	e := newEnv(t)
	e.loadFeed("http://a.example/feed", 20)
	entered, finish := make(chan struct{}), make(chan struct{})
	rebuildFTSHook = func(context.Context) { close(entered); <-finish }
	t.Cleanup(func() { rebuildFTSHook = nil })
	rebuilt := make(chan error, 1)
	go func() { rebuilt <- e.db.RebuildFTS(e.ctx) }()
	<-entered

	start := time.Now()
	err := e.db.WithWrite(e.ctx, func(context.Context, *sql.Tx) error { return nil })
	require.ErrorIs(t, err, ErrMaintenance)
	require.Less(t, time.Since(start), time.Second, "answered at once, not after the write deadline")
	require.ErrorIs(t, e.db.ResetTrimmedUnread(e.ctx, 1), ErrMaintenance, "a store method passes it through")

	gated := make(chan error, 1)
	go func() {
		_, err := e.db.PurgeSessions(e.ctx, e.clk.Now().Unix(), 10)
		gated <- err
	}()
	select {
	case err := <-gated:
		t.Fatalf("a gated writer must wait for the rebuild, got %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	require.NoError(t, <-rebuilt)
	require.NoError(t, <-gated, "the gated writer ran after the rebuild")
	require.NoError(t, e.db.WithWrite(e.ctx, func(context.Context, *sql.Tx) error { return nil }), "writes work again")
}
