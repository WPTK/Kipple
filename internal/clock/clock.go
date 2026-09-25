// Package clock is the injectable time source for the scheduler, the id
// allocator and the store, so tests can drive time deterministically.
package clock

import (
	"sync"
	"time"
)

// Clock supplies the current time and periodic ticks.
type Clock interface {
	Now() time.Time
	// Ticker returns a channel that receives about every d, and a stop func.
	Ticker(d time.Duration) (<-chan time.Time, func())
}

// Real is the wall clock.
type Real struct{}

// Now returns time.Now().
func (Real) Now() time.Time { return time.Now() }

// Ticker wraps time.Ticker.
func (Real) Ticker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

type fakeTicker struct {
	d    time.Duration
	next time.Time
	ch   chan time.Time
	dead bool
}

// NewFake returns a Fake starting at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the clock to t (forward or back) without firing tickers.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// Advance moves the clock forward by d and fires every ticker whose deadline
// passed (at most one pending tick per ticker, like time.Ticker).
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	for _, t := range f.tickers {
		if t.dead {
			continue
		}
		fired := false
		for !t.next.After(f.now) {
			t.next = t.next.Add(t.d)
			fired = true
		}
		if fired {
			select {
			case t.ch <- f.now:
			default:
			}
		}
	}
}

// Ticker registers a fake ticker.
func (f *Fake) Ticker(d time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTicker{d: d, next: f.now.Add(d), ch: make(chan time.Time, 1)}
	f.tickers = append(f.tickers, t)
	return t.ch, func() {
		f.mu.Lock()
		t.dead = true
		f.mu.Unlock()
	}
}
