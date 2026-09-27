package sched

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

// The items picked for extraction are held from the moment their commit makes
// them visible, not only once queueFulltext runs after the whole commit: a
// Reader client listing the feed in between must not see them unheld. Items the
// queue then refuses lose their hold.
func TestPickedItemsHeldWhenCommitReturns(t *testing.T) {
	fx := &fakeExt{block: true}
	r := newRig(t, Options{Extractor: fx, FulltextQueue: 2, FulltextGlobal: 1})
	type seen struct {
		held    map[string]int64
		pending []int64
	}
	got := make(chan seen, 1)
	r.s.inDispatcher(func() {
		r.s.commitFetchFn = func(ctx context.Context, res *fetch.Result, perChunk time.Duration) (store.CommitInfo, error) {
			ci, err := r.db.CommitFetchTimeout(ctx, res, perChunk)
			var p []int64
			_ = json.Unmarshal([]byte(r.db.HoldPending()), &p)
			got <- seen{ci.Held, p} // the state a Reader client sees right after the commit
			return ci, err
		}
	})
	srv := newFTServer(t, nil)
	srv.body.Store(ftFeed("https://art.test", 1, 2, 3, 4, 5))
	r.ftFeed(srv.URL + "/f")
	r.s.Wake()
	s := <-got
	require.Len(t, s.held, 2, "the queue has room for 2: those are picked and held")
	ids := []int64{}
	for _, id := range s.held {
		ids = append(ids, id)
	}
	require.ElementsMatch(t, ids, s.pending, "held by the commit itself, before queueing")

	r.waitEvents("fetch.done", 1)
	var p []int64
	require.NoError(t, json.Unmarshal([]byte(r.db.HoldPending()), &p))
	require.ElementsMatch(t, ids, p, "queued items keep their hold")
}

// Audit L2: more due feeds on a held host than one tick loads must not starve
// feeds on other hosts. They used to fill the LIMIT, be skipped in Go and stay
// oldest-due, so the free feed behind them was never loaded.
func TestHeldHostBacklogDoesNotStarveOtherFeeds(t *testing.T) {
	old := dueLimit
	dueLimit = 2
	t.Cleanup(func() { dueLimit = old })
	r := newRig(t, Options{})
	srv := newSrv(t, serveOK)
	var held []int64
	for i := range 3 {
		id := r.add(srv.URL+"/held"+string(rune('a'+i)), nil)
		r.setHost(id, "held.test")
		r.sql("UPDATE feeds SET next_fetch_at = ? WHERE id = ?", base.Add(-time.Hour).Unix(), id)
		held = append(held, id)
	}
	free := r.add(srv.URL+"/free", nil)
	r.sql("UPDATE feeds SET next_fetch_at = ? WHERE id = ?", base.Add(-time.Minute).Unix(), free)
	r.s.inDispatcher(func() { r.s.hostUntil["held.test"] = base.Add(24 * time.Hour) })

	r.s.Wake()
	waitFor(t, "the free feed fetched", func() bool { return srv.count("/free") == 1 })
	r.waitEvents("fetch.done", 1)
	require.Zero(t, srv.count("/helda")+srv.count("/heldb")+srv.count("/heldc"), "the held host is not fetched")
	for _, id := range held {
		require.Equal(t, base.Add(-time.Hour).Unix(), r.next(id), "held feeds stay due")
	}
}

// When the queue refuses the held items (shut since the pick), their holds are
// cleared: nothing is held that is not waiting for text.
func TestRefusedHeldItemsAreReleased(t *testing.T) {
	r := newRig(t, Options{Extractor: &fakeExt{}})
	feed := r.ftFeed("https://ex.test/f")
	items := ftItems("https://art.test", 1, 2, 3)
	held := r.commitFTItems(feed, items) // the commit marked them
	require.NotEqual(t, "[]", r.db.HoldPending())
	r.s.ftq.close()
	r.s.queueFulltext(feed, items, held)
	require.Equal(t, "[]", r.db.HoldPending())
}
