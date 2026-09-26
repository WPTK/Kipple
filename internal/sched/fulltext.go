package sched

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/events"
	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/ftrun"
)

// Ingest extraction limits (design §4.3, §7.5). Every one is overridable in
// Options; these are the defaults.
const (
	defaultFTMaxItems    = 20
	defaultFTItemTimeout = 10 * time.Second
	defaultFTPerHost     = 2
	defaultFTGlobal      = 4
	defaultFTQueue       = 500
	// While fetch.fulltext_all is on every feed feeds the pool, so a refresh-all would
	// overflow the plain limits: these are the limits then. Concurrency is unchanged.
	defaultFTMaxItemsAll = 50
	defaultFTQueueAll    = 2000
	// ftFlushDelay coalesces "text is ready" notifications into one event.
	ftFlushDelay = 300 * time.Millisecond
	// ftLookupTimeout bounds the post-commit lookup of the new items' ids.
	ftLookupTimeout = 5 * time.Second
)

type ftJob struct {
	itemID int64
	url    string
	host   string
}

// ftQueue is the bounded background queue of items waiting for extraction. It
// dedupes by item id and hands a job out only when its article host has a free
// slot, so one slow site cannot hold up the others (a FIFO channel would).
type ftQueue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	jobs     []ftJob
	queued   map[int64]bool // queued or running
	hostBusy map[string]int
	limit    int
	perHost  int
	closed   bool

	pendingReady []int64 // finished item ids awaiting the next ready event
	flushTimer   *time.Timer
}

func newFTQueue(limit, perHost int) *ftQueue {
	q := &ftQueue{queued: map[int64]bool{}, hostBusy: map[string]int{}, limit: limit, perHost: perHost}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// free is how many more jobs fit under limit (the queue's own bound when limit <= 0).
func (q *ftQueue) free(limit int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		limit = q.limit
	}
	return max(min(limit, q.limit)-len(q.jobs), 0)
}

// pushResult is what push did with a job.
type pushResult int

const (
	pushQueued pushResult = iota
	pushDup               // already queued or running
	pushFull              // the queue is at its bound
	pushClosed            // the queue is shut (shutdown)
)

// push adds a job within the queue's own bound.
func (q *ftQueue) push(j ftJob) pushResult { return q.pushWithin(j, 0) }

// pushWithin adds a job when fewer than limit are waiting (the queue's own bound when limit <= 0).
func (q *ftQueue) pushWithin(j ftJob, limit int) pushResult {
	if limit <= 0 {
		limit = q.limit
	}
	limit = min(limit, q.limit)
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case q.closed:
		return pushClosed
	case q.queued[j.itemID]:
		return pushDup
	case len(q.jobs) >= limit:
		return pushFull
	}
	q.queued[j.itemID] = true
	q.jobs = append(q.jobs, j)
	q.cond.Signal()
	return pushQueued
}

// take blocks for the oldest job whose host has a free slot. ok is false once
// the queue is closed. done must be called when the job is finished.
func (q *ftQueue) take() (j ftJob, done func(), ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if q.closed {
			return ftJob{}, nil, false
		}
		for i, c := range q.jobs {
			if q.hostBusy[c.host] < q.perHost {
				q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
				q.hostBusy[c.host]++
				return c, func() {
					q.mu.Lock()
					delete(q.queued, c.itemID)
					if q.hostBusy[c.host]--; q.hostBusy[c.host] == 0 {
						delete(q.hostBusy, c.host)
					}
					q.cond.Broadcast() // a host slot freed
					q.mu.Unlock()
				}, true
			}
		}
		q.cond.Wait()
	}
}

// close ends take for everyone; queued jobs are dropped.
func (q *ftQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// startFulltext launches the extraction pool: FulltextGlobal goroutines, the
// global cap on concurrent article fetches (memory guard). They end when the
// fetch context is cancelled by Stop; the dispatcher waits for them.
func (s *Scheduler) startFulltext() {
	context.AfterFunc(s.fetchCtx, s.ftq.close)
	for i := 0; i < s.opt.FulltextGlobal; i++ {
		s.ftWG.Add(1)
		go func() {
			defer s.ftWG.Done()
			for {
				j, done, ok := s.ftq.take()
				if !ok {
					return
				}
				s.runFulltext(s.fetchCtx, j)
				s.db.ClearFulltextPending(j.itemID) // after the row is written: the item is never unheld-and-unserved
				done()
			}
		}()
	}
}

// stopFulltext is called by the dispatcher after Stop: it waits for the pool
// and sends the last ready notification. Queued jobs are dropped; on-demand
// extraction covers them.
func (s *Scheduler) stopFulltext() {
	s.ftq.close() // Stop cancels fetchCtx, which closes it too; this makes it certain
	s.ftWG.Wait()
	s.ftq.mu.Lock()
	if s.ftq.flushTimer != nil {
		s.ftq.flushTimer.Stop()
		s.ftq.flushTimer = nil
	}
	ids := s.ftq.pendingReady
	s.ftq.pendingReady = nil
	dropped := len(s.ftq.jobs)
	s.ftq.mu.Unlock()
	s.publishReady(ids)
	if dropped > 0 {
		s.log.Info("sched: fulltext queue dropped at shutdown; items extract on demand", "items", dropped)
	}
}

// ftLimits are the per-fetch cap and queue bound in force now: the larger ones
// while fetch.fulltext_all is on.
func (s *Scheduler) ftLimits(ctx context.Context) (maxItems, queue int) {
	if s.db.FulltextAll(ctx) {
		return s.opt.FulltextMaxItemsAll, s.opt.FulltextQueueAll
	}
	return s.opt.FulltextMaxItems, s.opt.FulltextQueue
}

// noteReady batches finished items into one fulltext.ready event.
func (s *Scheduler) noteReady(id int64) {
	q := s.ftq
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pendingReady = append(q.pendingReady, id)
	if q.flushTimer == nil {
		q.flushTimer = time.AfterFunc(ftFlushDelay, func() {
			q.mu.Lock()
			ids := q.pendingReady
			q.pendingReady, q.flushTimer = nil, nil
			q.mu.Unlock()
			s.publishReady(ids)
		})
	}
}

// publishReady sends fulltext.ready (design §7.3): items whose extraction
// finished (text or error), ids as strings, at most MaxStateIDs per event.
func (s *Scheduler) publishReady(ids []int64) {
	if s.hub == nil {
		return
	}
	for len(ids) > 0 {
		n := min(len(ids), events.MaxStateIDs)
		strs := make([]string, n)
		for i, id := range ids[:n] {
			strs[i] = strconv.FormatInt(id, 10)
		}
		s.hub.Publish("fulltext.ready", map[string]any{"ids": strs, "source": "ingest"})
		ids = ids[n:]
	}
}

// pickFulltext chooses the new items of a fetched feed worth extracting and
// adds the fetch_log notes. It runs before the commit but does no network
// work: the extraction itself happens after the commit, in the pool (design
// §4.3), so a slow article host never holds a fetch worker.
//
// At most FulltextMaxItems (FulltextMaxItemsAll while fetch.fulltext_all is on) newest items are picked, and no more than the queue
// has room for. Everything not picked is left to the on-demand endpoint and
// counted in `fulltext_deferred`.
func (s *Scheduler) pickFulltext(ctx context.Context, res *fetch.Result) []fetch.Item {
	if res.Outcome != fetch.OutcomeOK || res.Feed == nil || len(res.Feed.Items) == 0 {
		return nil
	}
	// The current mode, not the one the fetch snapshotted before its (slow)
	// download: a switch or feed flag turned on meanwhile applies to these items.
	on, err := s.db.FeedFulltextNow(ctx, res.Snap.ID)
	if err != nil {
		on = res.Snap.Fulltext
	}
	if !on {
		return nil
	}
	if res.Snap.RekeyPending {
		// Unmatched items are inserted read after a re-key; leave them to the
		// on-demand path rather than fetching pages nobody is waiting on.
		res.Notes = append(res.Notes, "fulltext: skipped (re-key pending)")
		return nil
	}
	uids := make([]string, len(res.Feed.Items))
	for i, it := range res.Feed.Items {
		uids[i] = it.UID
	}
	known, err := s.db.KnownUIDs(ctx, res.Snap.ID, uids)
	if err != nil {
		s.log.Warn("sched: fulltext known uids", "feed", res.Snap.ID, "err", err)
		res.Notes = append(res.Notes, "fulltext: skipped (could not check existing items)")
		return nil
	}

	var cand []fetch.Item
	seen := map[string]bool{}
	for _, it := range res.Feed.Items {
		if known[it.UID] || seen[it.UID] || it.URL == "" {
			continue
		}
		seen[it.UID] = true
		cand = append(cand, it)
	}
	if len(cand) == 0 {
		return nil
	}
	// Items a mute rule will mute are never extracted: drop them here so they do not take the
	// per-fetch cap from real items (the commit's MutedIDs are what the queue finally skips).
	if muted, err := s.db.MutedUIDs(ctx, res.Snap.ID, cand); err != nil {
		s.log.Warn("sched: fulltext muted check", "feed", res.Snap.ID, "err", err)
	} else if len(muted) > 0 {
		kept := cand[:0]
		for _, it := range cand {
			if !muted[it.UID] {
				kept = append(kept, it)
			}
		}
		if cand = kept; len(cand) == 0 {
			return nil
		}
	}
	// Newest first; an item without a date is stamped with crawl time, so it is
	// the newest. Ties keep document order.
	sort.SliceStable(cand, func(a, b int) bool {
		pa, pb := cand[a].Published, cand[b].Published
		switch {
		case pa == nil && pb == nil:
			return false
		case pa == nil:
			return true
		case pb == nil:
			return false
		}
		return pa.After(*pb)
	})
	maxItems, queueLimit := s.ftLimits(ctx)
	keep := min(len(cand), maxItems, s.ftq.free(queueLimit))
	deferred := len(cand) - keep
	cand = cand[:keep]
	if keep > 0 {
		// Picked before the commit, so this is the number handed to the queue, not
		// a promise: a stale commit, a filled queue or a shutdown between the pick
		// and the push leave items to on-demand, logged with the real counts.
		res.Notes = append(res.Notes, fmt.Sprintf("fulltext_picked: %d", keep))
	}
	if deferred > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("fulltext_deferred: %d", deferred))
	}
	return cand
}

// queueFulltext hands the picked items of a committed fetch to the pool. Only
// items this commit actually inserted are queued. The queue may have filled (or
// shut) since pickFulltext; those items are logged and left to the on-demand
// path. The id lookup has its own short deadline, so a slow commit cannot
// starve it, and a failed lookup leaves the items to on-demand too.
func (s *Scheduler) queueFulltext(feedID int64, cand []fetch.Item, newIDs []int64) {
	if len(cand) == 0 || len(newIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.fetchCtx), ftLookupTimeout)
	defer cancel()
	// Re-evaluated at queue time: the feed flag or the switch may have been turned
	// off since the pick (the new items then never get a hold or a job).
	if on, err := s.db.FeedFulltextNow(ctx, feedID); err == nil && !on {
		return
	}
	_, queueLimit := s.ftLimits(ctx)
	uids := make([]string, len(cand))
	for i, it := range cand {
		uids[i] = it.UID
	}
	ids, err := s.db.ItemIDsByUID(ctx, feedID, uids)
	if err != nil {
		s.log.Warn("sched: fulltext resolve new items; they extract on demand", "feed", feedID, "items", len(cand), "err", err)
		return
	}
	isNew := make(map[int64]bool, len(newIDs))
	for _, id := range newIDs {
		isNew[id] = true
	}
	var queued, full, closed int
	for _, it := range cand {
		id, ok := ids[it.UID]
		if !ok || !isNew[id] {
			continue
		}
		// Marked pending before the push (a worker may finish the job at once) and
		// cleared again when the queue refuses it, so the Reader API holds only
		// items that are really waiting for text.
		s.db.MarkFulltextPending(id)
		switch s.ftq.pushWithin(ftJob{itemID: id, url: it.URL, host: ftrun.HostKey(it.URL)}, queueLimit) {
		case pushQueued:
			queued++
		case pushFull:
			full++
			s.db.ClearFulltextPending(id)
		case pushClosed:
			closed++
			s.db.ClearFulltextPending(id)
		default: // pushDup: another job already owns the pending mark
		}
	}
	if full > 0 {
		s.log.Warn("sched: fulltext queue full; items extract on demand", "feed", feedID, "dropped", full, "queued", queued)
	}
	if closed > 0 {
		s.log.Info("sched: fulltext queue shut; items extract on demand", "feed", feedID, "dropped", closed, "queued", queued)
	}
}

// runFulltext extracts one queued item through the shared runner, which stores
// the outcome with one small guarded write. It re-reads the item first, so work
// for an item that was deleted, whose URL or feed setting changed, or that
// already has an outcome is skipped. If the item is being extracted already
// (someone opened it), the runner joins that run instead of fetching twice.
func (s *Scheduler) runFulltext(ctx context.Context, j ftJob) {
	it, ok, err := s.db.GetFulltextItem(ctx, j.itemID)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("sched: fulltext load item", "item", j.itemID, "err", err)
		}
		return
	}
	if !ok || it.URL != j.url || it.Effective != 1 || it.HasRow {
		return
	}
	out, err := s.runner.Run(ctx, ftrun.Request{Item: it, Now: s.clk.Now().Unix(), Timeout: s.opt.FulltextItemTimeout, Background: true})
	switch {
	case errors.Is(err, ftrun.ErrAborted):
		return // shutdown, not the page's fault; on-demand covers it
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("sched: fulltext save", "item", j.itemID, "err", err)
		}
		return
	case !out.Written || out.Joined:
		return // refused by the guard, or the on-demand caller already has the result
	}
	if out.Save.Error != "" {
		s.log.Info("sched: fulltext failed", "feed", it.FeedID, "item", j.itemID, "error", out.Save.Error, "transient", out.Save.ErrorTransient)
	} else {
		s.log.Debug("sched: fulltext ok", "feed", it.FeedID, "item", j.itemID, "words", out.Save.WordCount)
	}
	s.noteReady(j.itemID)
}
