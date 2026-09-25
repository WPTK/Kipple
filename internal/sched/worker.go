package sched

import (
	"context"
	"fmt"
	"time"

	"github.com/WPTK/kipple/internal/fetch"
)

const commitTimeout = 10 * time.Second

// commitCtx is a fresh commit context, detached from the fetch context.
func (s *Scheduler) commitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(s.fetchCtx), s.opt.CommitTimeout)
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
		res := s.client.Fetch(s.fetchCtx, f.snap, s.clk.Now())
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
		// context is detached from the fetch context and gets its own deadline,
		// started here so a slow fetch does not eat the commit's budget.
		cctx, cancel := s.commitCtx()
		defer cancel()
		var err error
		if s.failCommit != nil {
			err = s.failCommit(f.snap.ID)
		} else if res.Success() {
			ci, cerr := s.db.CommitFetch(cctx, res)
			err = cerr
			out.newIDs, out.updated, out.trimmed, out.newItems = ci.NewIDs, ci.Updated, ci.Trimmed, ci.New
			out.migrated = ci.Migrated
			if cerr == nil && !ci.Stale {
				s.queueFulltext(cctx, f.snap.ID, cand, ci.NewIDs)
			}
			if res.UAFallbackWorked && !f.snap.UAFallback && cerr == nil && !ci.Stale {
				if uerr := s.db.SetFeedUAFallback(cctx, f.snap.ID); uerr != nil {
					s.log.Warn("sched: remember browser user agent", "feed", f.snap.ID, "err", uerr)
				}
			}
		} else {
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
