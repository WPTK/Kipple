package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatsTimedSinceStoredOnFirstTimedRow(t *testing.T) {
	e := newEnv(t)
	insert := func(kind string, ts int64) {
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			return InsertStat(ctx, tx, StatRow{TS: ts, LocalDate: "2026-09-01", Kind: kind, Client: "web", ItemID: 1, FeedID: 1, FeedTitle: "F", SessionKey: "k"})
		}))
	}
	has := func() int { return e.count(`SELECT count(*) FROM settings WHERE key = 'sys.stats_timed_since'`) }
	insert("open", 100)
	require.Zero(t, has(), "an open is not a timed event")
	insert("read_time", 200)
	require.Equal(t, 1, has())
	insert("scroll", 300)
	insert("read_time", 400)
	require.Equal(t, 200, e.count(`SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'sys.stats_timed_since'`), "only the first is kept")
}

func TestStatsDeleteWindowsAndProgress(t *testing.T) {
	e := newEnv(t)
	e.exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 25000)
		INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title)
		SELECT 1000 + i, '2026-09-20', 9, 0, 'open', 'web', i, 1, 'F' FROM c`)
	calls := 0
	n, err := StatsDelete(e.ctx, e.db, "2026-09-20", "2026-09-20", func() { calls++ })
	require.NoError(t, err)
	require.Equal(t, 25000, n)
	require.Equal(t, 3, calls, "one progress call per 10,000-id window")
	require.Zero(t, e.count("SELECT count(*) FROM stats_events"))
}
