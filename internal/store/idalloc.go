package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/WPTK/kipple/internal/clock"
)

// IDAlloc hands out item ids: unix microseconds, strictly increasing, never
// reused across restarts (design §3).
type IDAlloc struct {
	mu    sync.Mutex
	clock clock.Clock
	last  int64
}

// Next returns the next id. It is only called inside a write transaction,
// after BEGIN IMMEDIATE has taken the write lock.
func (a *IDAlloc) Next() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now().UnixMicro()
	if now <= a.last {
		now = a.last + 1
	}
	a.last = now
	return now
}

// Last returns the last id handed out (or the seed).
func (a *IDAlloc) Last() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// Skew returns how far the allocator is ahead of the clock (0 when it is not).
func (a *IDAlloc) Skew() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if d := time.Duration(a.last-a.clock.Now().UnixMicro()) * time.Microsecond; d > 0 {
		return d
	}
	return 0
}

// seedIDAlloc seeds the allocator from max(sys.id_high_water, MAX(items.id),
// MAX(trimmed_items.id)) and logs an error when the seed is over an hour ahead
// of the clock.
func seedIDAlloc(ctx context.Context, q Querier, clk clock.Clock, log *slog.Logger) (*IDAlloc, error) {
	var seed int64
	var hw sql.NullString
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = 'sys.id_high_water'").Scan(&hw)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("store: read id high-water: %w", err)
	}
	if hw.Valid {
		if n, perr := strconv.ParseInt(hw.String, 10, 64); perr == nil {
			seed = n
		}
	}
	for _, tbl := range []string{"items", "trimmed_items"} {
		var m sql.NullInt64
		if err := q.QueryRowContext(ctx, "SELECT max(id) FROM "+tbl).Scan(&m); err != nil {
			return nil, fmt.Errorf("store: seed ids from %s: %w", tbl, err)
		}
		if m.Valid && m.Int64 > seed {
			seed = m.Int64
		}
	}
	a := &IDAlloc{clock: clk, last: seed}
	if skew := a.Skew(); skew > time.Hour {
		log.Error("store: item ids are ahead of the clock", "offset", skew.String())
	}
	return a, nil
}
