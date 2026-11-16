package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func (e *env) seedFetchLog(feedID int64, n int, startedAt func(i int) int64, outcome string, keep int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		e.exec(`INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, keep)
			VALUES (?, 'scheduled', ?, 5, ?, ?)`, feedID, startedAt(i), outcome, keep)
	}
}

// The age cut deletes the rows past 14 days, keeps the 50 newest rows, the last
// 10 errors and every keep = 1 row, and reads only the expired rows plus the
// first one inside the limit, however long the rest of the history is.
func TestCapFetchLogReadsOnlyExpiredRows(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	now := e.clk.Now().Unix()
	day := int64(86400)

	e.exec("DELETE FROM fetch_log WHERE feed_id = ?", id)
	e.seedFetchLog(id, 3, func(i int) int64 { return now - 30*day + int64(i) }, "ok", 0)   // expired
	e.seedFetchLog(id, 1, func(i int) int64 { return now - 29*day }, "error", 0)           // expired, but one of the last 10 errors
	e.seedFetchLog(id, 1, func(i int) int64 { return now - 28*day }, "ok", 1)              // expired, kept
	e.seedFetchLog(id, 400, func(i int) int64 { return now - 10*day + int64(i) }, "ok", 0) // inside the limit

	var last int64
	var read int
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		last, read, err = expiredFetchLogBound(ctx, tx, id, now-14*day)
		return err
	}))
	require.NotZero(t, last)
	require.Equal(t, 6, read, "five expired rows and the first row inside the limit")

	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return capFetchLog(ctx, tx, id, now)
	}))
	require.Equal(t, 0, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND outcome = 'ok' AND keep = 0 AND started_at < ?", id, now-14*day))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND outcome = 'error'", id))
	require.Equal(t, 1, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND keep = 1", id))
	require.Equal(t, 400, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ? AND started_at >= ?", id, now-14*day))
}

// Nothing is read past the oldest row when it is inside the limit.
func TestCapFetchLogNothingExpired(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	now := e.clk.Now().Unix()
	e.exec("DELETE FROM fetch_log WHERE feed_id = ?", id)
	e.seedFetchLog(id, 300, func(i int) int64 { return now - 5*86400 + int64(i) }, "ok", 0)
	var last int64
	var read int
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		last, read, err = expiredFetchLogBound(ctx, tx, id, now-14*86400)
		return err
	}))
	require.Zero(t, last)
	require.Equal(t, 1, read)
}

// The 50-row floor holds: a feed whose history is all past the limit keeps its
// newest 50 rows.
func TestCapFetchLogKeepsTheFloor(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	now := e.clk.Now().Unix()
	e.exec("DELETE FROM fetch_log WHERE feed_id = ?", id)
	e.seedFetchLog(id, 80, func(i int) int64 { return now - 60*86400 + int64(i) }, "ok", 0)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return capFetchLog(ctx, tx, id, now)
	}))
	require.Equal(t, 50, e.count("SELECT count(*) FROM fetch_log WHERE feed_id = ?", id))
	// The 50 that stay are the newest ones (rows 30 to 79 of the 80 written), not the oldest.
	base := now - 60*86400
	require.Equal(t, base+30, scalar[int64](t, e.db.Reader(), "SELECT min(started_at) FROM fetch_log WHERE feed_id = ?", id))
	require.Equal(t, base+79, scalar[int64](t, e.db.Reader(), "SELECT max(started_at) FROM fetch_log WHERE feed_id = ?", id))
}
