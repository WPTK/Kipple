package sched

import (
	"context"
	"fmt"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
)

const commitTimeout = 10 * time.Second

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
	if s.fetchCtx.Err() != nil {
		out.cancelled = true
		return out
	}
	// A completed fetch commits even when shutdown starts now: the commit
	// context is detached from the fetch context and has its own deadline.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(s.fetchCtx), commitTimeout)
	defer cancel()

	switch f.kind {
	case kindSkip:
		note := fmt.Sprintf("skipped: host retry-after until %s", f.snap.HostUntil.UTC().Format(time.RFC3339))
		out.outcome = fetch.OutcomeSkipped
		if err := s.db.CommitSkip(cctx, f.snap, note); err != nil {
			s.log.Error("sched: write skip row", "feed", f.snap.ID, "err", err)
			out.errMsg = err.Error()
		}
	case kindTrim:
		out.outcome = fetch.OutcomeTrimOnly
		n, err := s.db.TrimOnly(cctx, f.snap.ID, fetch.TriggerRetention)
		if err != nil {
			s.log.Error("sched: trim", "feed", f.snap.ID, "err", err)
			out.outcome, out.errClass, out.errMsg = fetch.OutcomeError, "internal", err.Error()
		}
		out.trimmed = n
	default:
		res := s.client.Fetch(s.fetchCtx, f.snap, s.clk.Now())
		if res.Cancelled {
			out.cancelled = true
			return out
		}
		res.Schedule(s.clk.Now(), s.opt.Rand)
		out.outcome, out.status = res.Outcome, res.Status
		out.errClass, out.errMsg = res.ErrClass, res.ErrMsg
		out.retry, out.nextFetch = res.RetryAfter, res.NextFetchAt

		var err error
		if res.Success() {
			ci, cerr := s.db.CommitFetch(cctx, res)
			err = cerr
			out.newIDs, out.updated, out.trimmed, out.newItems = ci.NewIDs, ci.Updated, ci.Trimmed, ci.New
		} else {
			err = s.db.CommitFetchError(cctx, res)
		}
		if err != nil {
			s.log.Error("sched: commit", "feed", f.snap.ID, "err", err)
			out.outcome, out.errClass, out.errMsg = fetch.OutcomeError, "internal", err.Error()
		}
	}
	return out
}
