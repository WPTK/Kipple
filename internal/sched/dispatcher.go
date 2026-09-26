package sched

import (
	"context"
	"strconv"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
)

const (
	dueLimit         = 500
	tickQueryTimeout = 5 * time.Second
	progressEvery    = 500 * time.Millisecond
	maxEventIDs      = 50
)

// dispatch is the dispatcher goroutine: the only owner of flights, per-host
// counts, host deadlines, the pending queue and runs (design §4.1).
func (s *Scheduler) dispatch(tickC <-chan time.Time, stopTick func()) {
	defer close(s.stopped)
	defer stopTick()
	for {
		select {
		case <-tickC:
			s.tick()
		case <-s.wake:
			s.tick()
		case req := <-s.manualCh:
			s.handleRun(req)
		case req := <-s.priorityCh:
			s.handlePriority(req)
		case fn := <-s.syncCh:
			// barrier: process an already-delivered tick first, then run fn here
			select {
			case <-tickC:
				s.tick()
			default:
			}
			select {
			case <-s.wake:
				s.tick()
			default:
			}
			fn()
		case r := <-s.doneCh:
			s.handleDone(r)
		case <-s.stopCh:
			s.stopping = true
			stopTick()
			s.cancelFetch()
			close(s.jobs)
			// Keep receiving until every worker has sent its final exit, so no
			// worker can block on doneCh.
			for s.live > 0 {
				s.handleDone(<-s.doneCh)
			}
			s.stopFulltext()
			return
		}
	}
}

// tick enqueues every enabled feed that is due (design §4.2).
func (s *Scheduler) tick() {
	now := s.clk.Now()
	for h, t := range s.hostUntil {
		if !t.After(now) {
			delete(s.hostUntil, h)
		}
	}
	for id, t := range s.notBefore {
		if !t.After(now) {
			delete(s.notBefore, id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), tickQueryTimeout)
	defer cancel()
	set := s.db.FetchSettings(ctx)
	due, err := s.db.DueFeeds(ctx, set, now.Unix(), dueLimit)
	if err != nil {
		s.log.Error("sched: due query", "err", err)
		return
	}
	for _, snap := range due {
		if _, busy := s.flights[snap.ID]; busy {
			continue
		}
		if _, waiting := s.notBefore[snap.ID]; waiting {
			continue // its last commit failed; retry after the backoff
		}
		if _, held := s.hostUntil[snap.Host]; held {
			continue // stays due; retried on the first tick after the deadline
		}
		snap.Trigger = fetch.TriggerScheduled
		s.enqueue(&flight{snap: snap, kind: kindFetch})
	}
	s.drainPending()
}

// enqueue registers a flight and starts it when a worker and a host slot are
// free; otherwise it waits in pending.
func (s *Scheduler) enqueue(f *flight) {
	if f.snap.RekeyPending {
		f.snap.Full = true // a not_modified would never re-key
	}
	s.flights[f.snap.ID] = f
	if !s.tryStart(f) {
		s.pending = append(s.pending, f)
	}
}

func (s *Scheduler) canStart(f *flight) bool {
	return !s.stopping && s.running < s.opt.Workers && s.perHost[f.snap.Host] < s.opt.PerHost
}

func (s *Scheduler) tryStart(f *flight) bool {
	if !s.canStart(f) {
		return false
	}
	if t, ok := s.hostUntil[f.snap.Host]; ok && t.After(s.clk.Now()) {
		f.snap.HostUntil = t
	}
	select {
	case s.jobs <- f:
	default:
		return false // cannot happen (running < workers = capacity); stay pending
	}
	f.started = true
	s.running++
	s.perHost[f.snap.Host]++
	return true
}

// drainPending starts waiting jobs whose host slot has freed. A job that
// became blocked by a Retry-After after it was queued is dropped when it is a
// plain scheduled fetch (the feed stays due) and turned into a skip when a
// person asked for it.
func (s *Scheduler) drainPending() {
	if s.stopping {
		return
	}
	now := s.clk.Now()
	kept := s.pending[:0:0]
	for _, f := range s.pending {
		if f.kind == kindFetch {
			if t, ok := s.hostUntil[f.snap.Host]; ok && t.After(now) && !forcesFetch(f) {
				// Never drop a flight someone is waiting on: a run, a reply, or a
				// queued follow-up (a trim, a re-key) that only runs once it finishes.
				if len(f.runs) == 0 && len(f.replies) == 0 && len(f.followups) == 0 && len(f.runFollows) == 0 &&
					f.snap.Trigger == fetch.TriggerScheduled {
					delete(s.flights, f.snap.ID)
					continue
				}
				f.kind = kindSkip
				f.snap.HostUntil = t
			}
		}
		if !s.tryStart(f) {
			kept = append(kept, f)
		}
	}
	s.pending = kept
}

// forcesFetch reports whether a job goes ahead despite a host Retry-After: a
// full refetch or a subscribe-time fetch, which the caller needs a real answer
// for.
func forcesFetch(f *flight) bool {
	return f.snap.Full || f.snap.Trigger == fetch.TriggerSubscribe
}

// maxCommitFailCount stops the in-memory counter growing without bound; well
// past the point where the backoff has reached its cap.
const maxCommitFailCount = 64

// handleDone processes one worker result (design §4.2 steps 1-6).
func (s *Scheduler) handleDone(r result) {
	if r.exit {
		s.live--
		return
	}
	f := s.flights[r.feedID]
	delete(s.flights, r.feedID)
	if f != nil {
		// Replayed last, on every exit path (including a cancelled job, where
		// stopping is set and each request is answered ErrStopped).
		defer func() {
			for _, req := range f.followups {
				s.handlePriority(req)
			}
			for _, rf := range f.runFollows {
				s.runFollowup(rf)
			}
		}()
	}
	if f != nil && f.started {
		s.running--
		s.perHost[r.host]--
		if s.perHost[r.host] <= 0 {
			delete(s.perHost, r.host)
		}
	}
	if r.cancelled || f == nil {
		return // shutdown aborted it; nothing was written
	}

	now := s.clk.Now()
	// hostUntil is fed by every completed fetch, but each fetch's own
	// next_fetch_at was computed at fetch time from the deadline it started
	// with: a Retry-After learned mid-flight from a sibling is intentionally not
	// applied to an in-flight fetch's schedule (tick skips held hosts anyway).
	if r.retry > 0 {
		if until := now.Add(r.retry); until.After(s.hostUntil[r.host]) {
			s.hostUntil[r.host] = until
		}
	}

	if r.commitFailed {
		// Nothing was written, so next_fetch_at is still in the past and the feed
		// would be redispatched every tick. Back it off in memory instead.
		// The persisted failure counter does not move when the write itself
		// fails, so escalate on an in-memory count of consecutive commit
		// failures on top of it. FailureDelaySeconds caps the delay (24 h, or the
		// feed's interval when longer), which bounds retries on a dead database.
		s.commitFails[r.feedID] = min(s.commitFails[r.feedID]+1, maxCommitFailCount)
		n := f.snap.ConsecutiveFailures + s.commitFails[r.feedID]
		nb, _ := fetch.NextOnFailure(now, f.snap.IntervalS, n, 0, time.Time{}, s.opt.Rand)
		if r.nextFetch.After(nb) {
			nb = r.nextFetch
		}
		s.notBefore[r.feedID] = nb
	} else if f.kind == kindFetch {
		delete(s.notBefore, r.feedID)
		delete(s.commitFails, r.feedID)
	}

	// Ids are strings in every JSON body (design §7).
	runIDs := []string{}
	for _, run := range f.runs {
		runIDs = append(runIDs, strconv.FormatInt(run.ID, 10))
		run.Done++
		run.NewItems += r.newItems
		if r.outcome == fetch.OutcomeError {
			run.Errors++
		}
		run.Outstanding--
		if run.Outstanding <= 0 {
			if s.runs[run.Kind] == run {
				delete(s.runs, run.Kind)
			}
			s.hub.Publish("run.done", map[string]any{"run_id": idStr(run.ID), "new_items": run.NewItems, "errors": run.Errors})
		}
	}

	rep := Reply{FeedID: r.feedID, Outcome: r.outcome, NewItems: r.newItems, Updated: r.updated,
		Trimmed: r.trimmed, ErrClass: r.errClass, ErrMsg: r.errMsg}
	for _, ch := range f.replies {
		select {
		case ch <- rep:
		default:
		}
	}

	// Muted items are read already and hidden from every list but the Muted view, so a client
	// catching up on new items never needs their ids.
	shown := withoutIDs(r.newIDs, r.mutedIDs)
	ids := make([]string, 0, min(len(shown), maxEventIDs))
	for _, id := range shown[:min(len(shown), maxEventIDs)] {
		ids = append(ids, idStr(id))
	}
	ev := map[string]any{
		"feed_id": idStr(r.feedID), "run_ids": runIDs, "trigger": r.trigger, "outcome": r.outcome,
		"new_items": r.newItems, "new_item_ids": ids, "muted_items": r.muted, "updated_items": r.updated, "trimmed_items": r.trimmed,
		"error_class": r.errClass, "error": r.errMsg,
	}
	if !r.nextFetch.IsZero() {
		ev["next_fetch_at"] = r.nextFetch.Unix()
	}
	s.hub.Publish("fetch.done", ev)
	if r.migrated || r.gone {
		s.hub.Publish("feed.changed", map[string]any{"feed_id": idStr(r.feedID)})
	}
	for _, run := range f.runs {
		if s.runs[run.Kind] == run && now.Sub(run.lastProgress) >= progressEvery {
			run.lastProgress = now
			s.hub.Publish("run.progress", map[string]any{"run_id": idStr(run.ID), "done": run.Done, "total": run.Total,
				"new_items": run.NewItems, "errors": run.Errors})
		}
	}

	s.drainPending()
}

// handleRun creates (or joins) a run over a set of feeds (design §4.9).
func (s *Scheduler) handleRun(req runReq) {
	reply := func(r runReply) {
		select {
		case req.reply <- r:
		default:
		}
	}
	if s.stopping {
		reply(runReply{err: ErrStopped})
		return
	}
	if req.kind == RunManual {
		if r, ok := s.runs[RunManual]; ok {
			reply(runReply{info: RunInfo{RunID: r.ID, Kind: r.Kind, Total: r.Total, Joined: true}})
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), tickQueryTimeout)
	defer cancel()
	set := s.db.FetchSettings(ctx)
	var snaps []fetch.Snapshot
	switch req.kind {
	case RunImport:
		// One query for every new feed: a lookup per feed would share this one
		// short deadline, hold up the dispatcher and drop feeds silently once it ran out.
		all, err := s.db.FeedSnapshotsByID(ctx, set, req.feedIDs)
		if err != nil {
			s.log.Error("sched: load imported feeds", "feeds", len(req.feedIDs), "err", err)
			reply(runReply{err: err})
			return
		}
		for _, snap := range all {
			if snap.Enabled {
				snaps = append(snaps, snap)
			}
		}
	default:
		all, err := s.db.EnabledFeeds(ctx, set)
		if err != nil {
			s.log.Error("sched: list feeds", "err", err)
			reply(runReply{err: err})
			return
		}
		for _, snap := range all {
			if req.kind == RunRetention && !req.all && snap.Retention != -1 {
				continue
			}
			snaps = append(snaps, snap)
		}
	}

	s.lastRunID = max(s.lastRunID+1, s.clk.Now().UnixMilli())
	run := &Run{ID: s.lastRunID, Kind: req.kind}
	now := s.clk.Now()
	var toEnqueue []*flight
	for _, snap := range snaps {
		if f, busy := s.flights[snap.ID]; busy {
			run.Outstanding++
			if runSatisfied(f, req.kind) {
				// a fetch happening now counts as "fetched now"
				f.runs = append(f.runs, run)
			} else {
				// A trim or skip in flight did not fetch the feed: the run's
				// own job for it follows once that one is done.
				f.runFollows = append(f.runFollows, runFollow{run: run, kind: req.kind, feedID: snap.ID})
			}
			continue
		}
		f := s.newRunFlight(run, req.kind, snap, now)
		toEnqueue = append(toEnqueue, f)
		run.Outstanding++
	}
	run.Total = run.Outstanding
	if run.Total == 0 {
		s.hub.Publish("run.done", map[string]any{"run_id": idStr(run.ID), "new_items": 0, "errors": 0})
		reply(runReply{info: RunInfo{RunID: run.ID, Kind: run.Kind}})
		return
	}
	s.runs[run.Kind] = run
	s.hub.Publish("run.start", map[string]any{"run_id": idStr(run.ID), "kind": run.Kind, "total": run.Total})
	for _, f := range toEnqueue {
		s.enqueue(f)
	}
	reply(runReply{info: RunInfo{RunID: run.ID, Kind: run.Kind, Total: run.Total}})
}

// handlePriority queues one per-feed job ahead of the FIFO.
func (s *Scheduler) handlePriority(req priorityReq) {
	answer := func(r Reply) {
		select {
		case req.reply <- r:
		default:
		}
	}
	if s.stopping {
		answer(Reply{FeedID: req.p.FeedID, Err: ErrStopped})
		return
	}
	if f, busy := s.flights[req.p.FeedID]; busy {
		if satisfies(f, req.p) {
			f.replies = append(f.replies, req.reply) // it answers when the running job does
		} else if !f.started && req.p.Kind == PriorityRefresh && req.p.Full && f.kind != kindTrim {
			// Not started yet (waiting for a worker or host slot): upgrade it in
			// place to the full refetch instead of queueing a follow-up behind a
			// job that may never run.
			f.snap.Full, f.kind, f.snap.HostUntil = true, kindFetch, time.Time{}
			f.replies = append(f.replies, req.reply)
		} else {
			// The running job cannot honor this request's intent (a full refetch,
			// a trim, a re-key that arrived after its snapshot). Keep the request
			// whole and run it after this job, on a fresh snapshot.
			f.followups = append(f.followups, req)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), tickQueryTimeout)
	defer cancel()
	snap, ok, err := s.db.FeedSnapshot(ctx, s.db.FetchSettings(ctx), req.p.FeedID)
	switch {
	case err != nil:
		answer(Reply{FeedID: req.p.FeedID, Err: err})
		return
	case !ok:
		answer(Reply{FeedID: req.p.FeedID, Err: ErrNotFound})
		return
	case !snap.Enabled && req.p.Kind != PriorityTrim: // a trim touches no network, so a disabled feed still honors a lowered cap
		answer(Reply{FeedID: req.p.FeedID, Err: ErrDisabled})
		return
	}
	f := &flight{snap: snap, replies: []chan Reply{req.reply}}
	switch req.p.Kind {
	case PriorityTrim:
		f.kind, f.snap.Trigger = kindTrim, fetch.TriggerRetention
	default:
		f.kind = kindFetch
		f.snap.Trigger = req.p.Trigger
		if f.snap.Trigger == "" {
			f.snap.Trigger = fetch.TriggerFeedManual
		}
		f.snap.Full = req.p.Full
	}
	if f.kind == kindFetch && f.snap.RekeyPending {
		f.snap.Full = true
	}
	if f.kind == kindFetch && !forcesFetch(f) {
		// same as drainPending: a held host turns a person's refresh into a skip
		if t, held := s.hostUntil[snap.Host]; held && t.After(s.clk.Now()) {
			f.kind, f.snap.HostUntil = kindSkip, t
		}
	}
	s.flights[snap.ID] = f
	if !s.tryStart(f) {
		s.pending = append([]*flight{f}, s.pending...)
	}
}

// satisfies reports whether the in-flight job f already does what p asks: a
// plain refresh is covered by any fetch, a full refresh only by a full fetch,
// a trim only by a trim.
func satisfies(f *flight, p Priority) bool {
	switch p.Kind {
	case PriorityTrim:
		return f.kind == kindTrim
	default:
		return f.kind == kindFetch && (!p.Full || f.snap.Full)
	}
}

// runFollow is a run's job for a feed whose in-flight job did not do what the
// run needs (a trim or a skip where the run fetches). It starts a fresh flight
// once the in-flight one is done; the run keeps that feed outstanding meanwhile.
type runFollow struct {
	run    *Run
	kind   string
	feedID int64
}

// runSatisfied reports whether the in-flight job f does what a run of the
// given kind needs for that feed: a fetch run needs a real fetch (same rule as
// a priority refresh); a retention run is served by a trim or by a fetch, which
// trims after its commit.
func runSatisfied(f *flight, runKind string) bool {
	if runKind == RunRetention {
		return f.kind == kindTrim || f.kind == kindFetch
	}
	return satisfies(f, Priority{Kind: PriorityRefresh})
}

// newRunFlight builds the job a run uses for one feed.
func (s *Scheduler) newRunFlight(run *Run, runKind string, snap fetch.Snapshot, now time.Time) *flight {
	f := &flight{runs: []*Run{run}}
	switch runKind {
	case RunRetention:
		snap.Trigger, f.kind = fetch.TriggerRetention, kindTrim
	default:
		snap.Trigger = fetch.TriggerManual
		if runKind == RunImport {
			snap.Trigger = fetch.TriggerImport
		}
		f.kind = kindFetch
		if t, held := s.hostUntil[snap.Host]; held && t.After(now) {
			f.kind, snap.HostUntil = kindSkip, t
		}
	}
	f.snap = snap
	return f
}

// runFollowup starts a run's deferred job for one feed, or settles the feed as
// an error when it can no longer run (shutdown, feed gone or disabled).
func (s *Scheduler) runFollowup(rf runFollow) {
	ctx, cancel := context.WithTimeout(context.Background(), tickQueryTimeout)
	defer cancel()
	snap, ok, err := s.db.FeedSnapshot(ctx, s.db.FetchSettings(ctx), rf.feedID)
	if s.stopping || err != nil || !ok || !snap.Enabled {
		s.settleRunFeed(rf.run, true)
		return
	}
	if f, busy := s.flights[rf.feedID]; busy { // something else started meanwhile
		if runSatisfied(f, rf.kind) {
			f.runs = append(f.runs, rf.run)
		} else {
			f.runFollows = append(f.runFollows, rf)
		}
		return
	}
	s.enqueue(s.newRunFlight(rf.run, rf.kind, snap, s.clk.Now()))
}

// settleRunFeed closes one feed of a run without a job.
func (s *Scheduler) settleRunFeed(run *Run, isErr bool) {
	run.Done++
	if isErr {
		run.Errors++
	}
	run.Outstanding--
	if run.Outstanding <= 0 {
		if s.runs[run.Kind] == run {
			delete(s.runs, run.Kind)
		}
		s.hub.Publish("run.done", map[string]any{"run_id": idStr(run.ID), "new_items": run.NewItems, "errors": run.Errors})
	}
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }
