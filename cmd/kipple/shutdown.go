package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Shutdown budget (design §4.10, docs/deploy.md): everything after the stop
// signal fits in one shutdownTotal, inside the compose file's 30 s
// stop_grace_period, so a slow stage can never earn the process a SIGKILL
// mid-checkpoint. Each stage is capped by its own maximum and by what is left,
// and closeReserve is always kept back for the final closes (the store's WAL
// checkpoint above all). Variables, so tests can shrink them.
var (
	shutdownTotal = 25 * time.Second
	closeReserve  = 5 * time.Second
	httpDrainMax  = 10 * time.Second
	schedDrainMax = 15 * time.Second
)

// shutdownBudget is the one deadline every shutdown stage draws on. The zero
// value has not started: a stage then gets its own maximum (startup error paths).
type shutdownBudget struct {
	mu       sync.Mutex
	deadline time.Time
}

// start fixes the deadline at now + shutdownTotal (only the first call counts).
func (b *shutdownBudget) start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.deadline.IsZero() {
		b.deadline = time.Now().Add(shutdownTotal)
	}
}

// left is how long a stage may take: at most limit, and no later than the
// deadline less reserve. Never negative.
func (b *shutdownBudget) left(reserve, limit time.Duration) time.Duration {
	b.mu.Lock()
	dl := b.deadline
	b.mu.Unlock()
	if dl.IsZero() {
		return limit
	}
	return min(limit, max(0, time.Until(dl)-reserve))
}

// bounded runs fn and waits at most d for it. It reports whether fn finished; a
// stage that did not is left running (the process is about to exit).
func bounded(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// shutdownSteps are the stages of stopAll, injectable for tests.
type shutdownSteps struct {
	stopWork  func()                          // stop the scheduler, close SSE (non-blocking)
	drainHTTP func(ctx context.Context) error // http.Server.Shutdown
	cutHTTP   func()                          // http.Server.Close, when the drain ran out
	stopped   <-chan struct{}                 // the scheduler's workers have drained
	stopMaint func()                          // cancel and join maintenance
}

// runShutdown starts the budget and runs the stages in order (design §4.10):
// stop the scheduler, close SSE, drain HTTP, wait for the workers, stop
// maintenance. The deferred closes (store checkpoint last) get what is left,
// at least closeReserve.
func runShutdown(b *shutdownBudget, s shutdownSteps, logger *slog.Logger) error {
	b.start()
	s.stopWork()
	ctx, cancel := context.WithTimeout(context.Background(), b.left(closeReserve, httpDrainMax))
	shutErr := s.drainHTTP(ctx)
	cancel()
	if shutErr != nil {
		s.cutHTTP() // a request outlived the grace period: cut it
	}
	t := time.NewTimer(b.left(closeReserve, schedDrainMax))
	select {
	case <-s.stopped:
	case <-t.C:
		logger.Error("scheduler did not drain in time")
	}
	t.Stop()
	// Design §4.10 step 5: cancel maintenance (interrupts a running purge or
	// VACUUM INTO) before the final checkpoint in the deferred db.Close.
	if !bounded(b.left(closeReserve, shutdownTotal), s.stopMaint) {
		logger.Error("maintenance did not stop in time")
	}
	if shutErr != nil {
		return fmt.Errorf("shutdown: %w", shutErr)
	}
	return nil
}

// closeWithin runs one deferred close inside what is left of the budget
// (closeMax when the budget has not started), logging when it runs out.
func closeWithin(b *shutdownBudget, logger *slog.Logger, what string, fn func() error) {
	var err error
	if !bounded(b.left(0, closeMax), func() { err = fn() }) {
		logger.Error(what+" did not finish in the shutdown budget", "budget", shutdownTotal)
		return
	}
	if err != nil {
		logger.Error(what, "err", err)
	}
}

// closeMax bounds a deferred close on a startup error path (no budget yet).
var closeMax = 10 * time.Second
