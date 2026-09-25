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

// WithWrite is the only door to the writer pool. It derives a 10 s deadline, begins an
// immediate transaction, runs fn (handing it the deadline-bound context), and commits on nil or rolls back otherwise. Rows opened
// inside fn must be closed by fn (or the store method that opened them) before returning.
func (d *DB) WithWrite(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) error {
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
