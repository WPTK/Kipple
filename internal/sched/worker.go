package sched

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
	"github.com/WPTK/kipple/internal/store"
)

const commitTimeout = 10 * time.Second

// commitCtx is a fresh commit context, detached from the fetch context.
func (s *Scheduler) commitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(s.fetchCtx), s.opt.CommitTimeout)
}

// commitFetch runs the item commit with a per-chunk budget of CommitTimeout.
func (s *Scheduler) commitFetch(ctx context.Context, res *fetch.Result) (store.CommitInfo, error) {
	if s.commitFetchFn != nil {
		return s.commitFetchFn(ctx, res, s.opt.CommitTimeout)
	}
	return s.db.CommitFetchTimeout(ctx, res, s.opt.CommitTimeout)
}

// worker ranges over the job queue until Stop closes it (design §4.3). It holds
// no transaction across a network call or a channel send.
func (s *Scheduler) worker() {
	for f := range s.jobs {
		s.doneCh <- s.exec(f)
	}
	s.doneCh <- result{exit: true}
}

func (s *Scheduler) exec(f *flight) (out result) {
	out = result{feedID: f.snap.ID, host: f.snap.Host, trigger: f.snap.Trigger}
	// A panic anywhere in the job (a parser bug on a hostile feed, a store bug)
	// must not take the server down: it becomes an error result, so the worker
	// still reports on doneCh and the dispatcher's counters stay balanced.
	committing := false
	defer func() {
		if v := recover(); v != nil {
			out = s.recovered(f, v, debug.Stack(), committing)
		}
	}()
	if s.fetchCtx.Err() != nil {
		out.cancelled = true
		return out
	}

	switch f.kind {
	case kindSkip:
		cctx, cancel := s.commitCtx()
		defer cancel()
		note := fmt.Sprintf("skipped: host retry-after until %s", f.snap.HostUntil.UTC().Format(time.RFC3339))
		out.outcome = fetch.OutcomeSkipped
		if err := s.db.CommitSkip(cctx, f.snap, note); err != nil {
			s.log.Error("sched: write skip row", "feed", f.snap.ID, "err", err)
			out.errMsg = err.Error()
		}
	case kindTrim:
		cctx, cancel := s.commitCtx()
		defer cancel()
		out.outcome = fetch.OutcomeTrimOnly
		n, err := s.db.TrimOnly(cctx, f.snap.ID, fetch.TriggerRetention)
		if err != nil {
			s.log.Error("sched: trim", "feed", f.snap.ID, "err", err)
			out.outcome, out.errClass, out.errMsg = fetch.OutcomeError, "internal", err.Error()
		}
		out.trimmed = n
	default:
		fetchFn := s.client.Fetch
		if s.fetchFn != nil {
			fetchFn = s.fetchFn
		}
		res := fetchFn(s.fetchCtx, f.snap, s.clk.Now())
		if res.Cancelled {
			out.cancelled = true
			return out
		}
		// The schedule uses the host deadline the snapshot was started with
		// (Snap.HostUntil). A Retry-After a sibling feed learns while this fetch
		// is in flight is intentionally not applied here: tick skips held hosts,
		// so this feed simply waits out the deadline when it next comes due.
		// Extraction happens after the commit, in the background pool (design
		// §4.3): only the candidates and the fetch_log notes are chosen here.
		cand := s.pickFulltext(s.fetchCtx, res)
		res.Schedule(s.clk.Now(), s.opt.Rand)
		out.outcome, out.status = res.Outcome, res.Status
		out.errClass, out.errMsg = res.ErrClass, res.ErrMsg
		out.retry, out.nextFetch = res.RetryAfter, res.NextFetchAt

		// A completed fetch commits even when shutdown starts now: the commit
		// context is detached from the fetch context. CommitFetch bounds each
		// chunk itself (store.CommitFetch: ctx carries no deadline), so a feed
		// committed in several chunks gets a full CommitTimeout per chunk rather
		// than one shared window. The small follow-up writes get their own
		// bounded context, started after the item commit.
		var err error
		committing = true
		if s.failCommit != nil {
			err = s.failCommit(f.snap.ID)
		} else if res.Success() {
			if len(cand) > 0 {
				// The commit marks these pending as it inserts them, so a Reader
				// client never sees one before its hold starts.
				res.HoldUIDs = make(map[string]bool, len(cand))
				for _, it := range cand {
					res.HoldUIDs[it.UID] = true
				}
			}
			ci, cerr := s.commitFetch(context.WithoutCancel(s.fetchCtx), res)
			err = cerr
			out.newIDs, out.updated, out.trimmed, out.newItems = ci.NewIDs, ci.Updated, ci.Trimmed, ci.New
			out.migrated = ci.Migrated
			out.mutedIDs, out.muted = ci.MutedIDs, ci.Muted
			if cerr == nil {
				// ci.Held is what really committed: empty for a stale fetch, the
				// early chunks for a large one cut short by a URL edit.
				s.queueFulltext(f.snap.ID, cand, ci.Held)
			} else {
				s.db.ClearFulltextPending(heldIDs(ci.Held)...) // left to on-demand, as before
			}
			if res.UAFallbackWorked && !f.snap.UAFallback && cerr == nil && !ci.Stale {
				cctx, cancel := s.commitCtx()
				defer cancel()
				moved := ""
				if ci.Migrated {
					moved = res.Redirect.To
				}
				if uerr := s.db.SetFeedUAFallback(cctx, f.snap.ID, f.snap.URL, moved); uerr != nil {
					s.log.Warn("sched: remember browser user agent", "feed", f.snap.ID, "err", uerr)
				}
			}
		} else {
			cctx, cancel := s.commitCtx()
			defer cancel()
			err = s.db.CommitFetchError(cctx, res)
			out.gone = res.Gone && err == nil
		}
		if err != nil {
			out.commitFailed = true
			s.log.Error("sched: commit", "feed", f.snap.ID, "err", err)
			out.outcome, out.errClass, out.errMsg = fetch.OutcomeError, "internal", err.Error()
		}
	}
	return out
}

// panicMsg is the fetch_log error of a job that panicked; the stack is logged.
const panicMsg = "internal error while processing the feed (see the server log)"

// recovered turns a job's panic into its result. The stack is logged. A panic
// before the commit started (the fetch or the parse) is recorded as a parse
// error through the normal error bookkeeping, so the feed backs off like any
// failing feed and the user sees it in the fetch log; if that write fails too,
// or the panic came during or after the commit (whose state is unknown), the
// result is a failed commit, which the dispatcher backs off in memory.
func (s *Scheduler) recovered(f *flight, v any, stack []byte, committing bool) (out result) {
	s.log.Error("sched: job panicked", "feed", f.snap.ID, "kind", int(f.kind), "panic", fmt.Sprint(v), "stack", string(stack))
	out = result{feedID: f.snap.ID, host: f.snap.Host, trigger: f.snap.Trigger,
		outcome: fetch.OutcomeError, errClass: "internal", errMsg: panicMsg, commitFailed: true}
	if f.kind != kindFetch || committing {
		return out
	}
	defer func() {
		if v2 := recover(); v2 != nil {
			s.log.Error("sched: recording a panicked job panicked too", "feed", f.snap.ID, "panic", fmt.Sprint(v2))
		}
	}()
	now := s.clk.Now()
	res := &fetch.Result{Snap: f.snap, StartedAt: now, Outcome: fetch.OutcomeError, ErrClass: fetch.ClassParse, ErrMsg: panicMsg}
	res.Schedule(now, s.opt.Rand)
	cctx, cancel := s.commitCtx()
	defer cancel()
	if err := s.db.CommitFetchError(cctx, res); err != nil {
		s.log.Error("sched: record panicked job", "feed", f.snap.ID, "err", err)
		return out
	}
	out.errClass, out.nextFetch, out.commitFailed = fetch.ClassParse, res.NextFetchAt, false
	return out
}

// heldIDs lists the ids of a CommitInfo.Held map.
func heldIDs(held map[string]int64) []int64 {
	ids := make([]int64, 0, len(held))
	for _, id := range held {
		ids = append(ids, id)
	}
	return ids
}

// withoutIDs returns ids minus drop, keeping order. With nothing to drop it returns ids itself.
func withoutIDs(ids, drop []int64) []int64 {
	if len(drop) == 0 {
		return ids
	}
	skip := make(map[int64]struct{}, len(drop))
	for _, id := range drop {
		skip[id] = struct{}{}
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := skip[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
