package greader

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/store"
)

// When a later batch of mark-all-as-read fails, the earlier ones stay committed, so the stream still tells
// the other clients to resync before the request answers with the error.
func TestMarkAllAsReadPublishesResyncWhenALaterBatchFails(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hub := events.New()
	h.api.opt.Events = hub
	sub := hub.Subscribe(0)
	defer sub.Close()

	f := h.addFeed("https://a.example/f", "A", "")
	for range store.MarkAllBatch + 5 {
		h.addItem(f, itemSeed{})
	}
	// The second batch starts with MarkAllBatch items already read: fail it.
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER fail_second_batch BEFORE UPDATE OF read ON items
			WHEN (SELECT count(*) FROM items WHERE read = 1) >= %d
			BEGIN SELECT RAISE(ABORT, 'stop'); END`, store.MarkAllBatch))
		return err
	}))

	w := h.post(rd+"mark-all-as-read", "T="+h.tok+"&s=user/-/state/com.google/reading-list")
	require.Equal(t, 500, w.Code)
	var read int
	require.NoError(t, h.db.Reader().QueryRow("SELECT count(*) FROM items WHERE read = 1").Scan(&read))
	require.Equal(t, store.MarkAllBatch, read, "the first batch is committed")
	select {
	case ev := <-sub.C:
		require.Equal(t, "resync", ev.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("no resync after a partly applied mark-all-as-read")
	}
}

// A first batch that rolls back (its ledger statement fails after its items UPDATE changed rows) changed
// nothing, so there is nothing to announce.
func TestMarkAllAsReadPublishesNothingWhenTheFirstBatchRollsBack(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	hub := events.New()
	h.api.opt.Events = hub
	sub := hub.Subscribe(0)
	defer sub.Close()

	f := h.addFeed("https://a.example/f", "A", "")
	var last int64
	for range 3 {
		last = h.addItem(f, itemSeed{})
	}
	require.NoError(t, h.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?, ?, 'l1', 0, 1, 1)`, last+1, f); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER fail_ledger BEFORE UPDATE OF read ON trimmed_items BEGIN SELECT RAISE(ABORT, 'stop'); END`)
		return err
	}))

	w := h.post(rd+"mark-all-as-read", "T="+h.tok+"&s=user/-/state/com.google/reading-list")
	require.Equal(t, 500, w.Code)
	var read int
	require.NoError(t, h.db.Reader().QueryRow("SELECT count(*) FROM items WHERE read = 1").Scan(&read))
	require.Zero(t, read, "the batch rolled back")
	select {
	case ev := <-sub.C:
		t.Fatalf("unexpected %s event after a rolled-back batch", ev.Type)
	case <-time.After(300 * time.Millisecond):
	}
}
