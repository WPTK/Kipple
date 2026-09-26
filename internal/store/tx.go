package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// holder records who currently owns the writer, for timeout diagnostics.
type holder struct {
	file  string
	line  int
	fn    string
	start time.Time
}

// Querier is satisfied by *sql.Tx and *sql.DB; store methods take it, never a *sql.DB
// from inside a write transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrMaintenance means a maintenance job that holds the writer longer than the
// write deadline (the FTS rebuild, up to FTSRebuildTimeout) is running, so the
// write was not attempted. It is temporary: the API and the Reader API answer
// 503 with Retry-After: MaintenanceRetryAfter, and clients retry. Writers that
// take the commit gate first (fetch commits, maintenance batches) never see it:
// they wait on the gate instead.
var ErrMaintenance = errors.New("store: maintenance in progress, retry shortly")

// MaintenanceRetryAfter is the Retry-After the HTTP layers send with ErrMaintenance.
const MaintenanceRetryAfter = 15 * time.Second

// WithWrite is the only door to the writer pool. It derives a 10 s deadline, begins an
// immediate transaction, runs fn (handing it the deadline-bound context), and commits on nil or rolls back otherwise. Rows opened
// inside fn must be closed by fn (or the store method that opened them) before returning.
// While a maintenance job owns the writer it returns ErrMaintenance at once
// instead of waiting, and a wait that times out because one started meanwhile
// ends with ErrMaintenance too.
func (d *DB) WithWrite(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
	gen := d.maintGen.Load()
	if d.maintActive.Load() {
		return ErrMaintenance
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	h := &holder{start: time.Now()}
	if pc, file, line, ok := runtime.Caller(1); ok {
		h.file, h.line = file, line
		if f := runtime.FuncForPC(pc); f != nil {
			h.fn = f.Name()
		}
	}

	tx, err := d.writer.BeginTx(ctx, nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && (d.maintActive.Load() || d.maintGen.Load() != gen) {
			return fmt.Errorf("%w (%w)", ErrMaintenance, err)
		}
		if prev := d.holder.Load(); errors.Is(err, context.DeadlineExceeded) && prev != nil {
			d.log.Error("store: timed out acquiring the writer",
				"holder", prev.fn, "at", fmt.Sprintf("%s:%d", prev.file, prev.line),
				"held_for", time.Since(prev.start).String())
		}
		return fmt.Errorf("store: begin write: %w", err)
	}
	d.holder.Store(h)
	defer d.holder.CompareAndSwap(h, nil)

	if err := fn(ctx, tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}
