package sched

import (
	"context"
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
			if t, ok := s.hostUntil[f.snap.Host]; ok && t.After(now) {
				if len(f.runs) == 0 && len(f.replies) == 0 && f.snap.Trigger == fetch.TriggerScheduled {
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

// handleDone processes one worker result (design §4.2 steps 1-6).
func (s *Scheduler) handleDone(r result) {
	if r.exit {
		s.live--
		return
	}
	f := s.flights[r.feedID]
	delete(s.flights, r.feedID)
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
	if r.retry > 0 {
		if until := now.Add(r.retry); until.After(s.hostUntil[r.host]) {
			s.hostUntil[r.host] = until
		}
	}

	var runIDs []int64
	for _, run := range f.runs {
		runIDs = append(runIDs, run.ID)
		run.Done++
		run.NewItems += r.newItems
		if r.outcome == fetch.OutcomeError {
			run.Errors++
		}
		run.Outstanding--
		if run.Outstanding <= 0 {
			delete(s.runs, run.Kind)
			s.hub.Publish("run.done", map[string]any{"run_id": run.ID, "new_items": run.NewItems, "errors": run.Errors})
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

	ids := r.newIDs
	if len(ids) > maxEventIDs {
		ids = ids[:maxEventIDs]
	}
	ev := map[string]any{
		"feed_id": r.feedID, "run_ids": runIDs, "trigger": r.trigger, "outcome": r.outcome,
		"new_items": r.newItems, "new_item_ids": ids, "updated_items": r.updated, "trimmed_items": r.trimmed,
		"error_class": r.errClass, "error": r.errMsg,
	}
	if !r.nextFetch.IsZero() {
		ev["next_fetch_at"] = r.nextFetch.Unix()
	}
	s.hub.Publish("fetch.done", ev)
	for _, run := range f.runs {
		if _, live := s.runs[run.Kind]; live && now.Sub(run.lastProgress) >= progressEvery {
			run.lastProgress = now
			s.hub.Publish("run.progress", map[string]any{"run_id": run.ID, "done": run.Done, "total": run.Total,
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
		for _, id := range req.feedIDs {
			snap, ok, err := s.db.FeedSnapshot(ctx, set, id)
			if err == nil && ok && snap.Enabled {
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
			// a fetch happening now counts as "fetched now"
			f.runs = append(f.runs, run)
			run.Outstanding++
			continue
		}
		f := &flight{runs: []*Run{run}}
		switch req.kind {
		case RunRetention:
			snap.Trigger, f.kind = fetch.TriggerRetention, kindTrim
		default:
			snap.Trigger = fetch.TriggerManual
			if req.kind == RunImport {
				snap.Trigger = fetch.TriggerImport
			}
			f.kind = kindFetch
			if t, held := s.hostUntil[snap.Host]; held && t.After(now) {
				f.kind, snap.HostUntil = kindSkip, t
			}
		}
		f.snap = snap
		toEnqueue = append(toEnqueue, f)
		run.Outstanding++
	}
	run.Total = run.Outstanding
	if run.Total == 0 {
		s.hub.Publish("run.done", map[string]any{"run_id": run.ID, "new_items": 0, "errors": 0})
		reply(runReply{info: RunInfo{RunID: run.ID, Kind: run.Kind}})
		return
	}
	s.runs[run.Kind] = run
	s.hub.Publish("run.start", map[string]any{"run_id": run.ID, "kind": run.Kind, "total": run.Total})
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
		f.replies = append(f.replies, req.reply) // it answers when the running job does
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
	case !snap.Enabled:
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
	s.flights[snap.ID] = f
	if !s.tryStart(f) {
		s.pending = append([]*flight{f}, s.pending...)
	}
}
