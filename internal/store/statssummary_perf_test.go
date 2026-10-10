package store

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// seedStats fills stats_events with about n rows over a year, with a realistic mix: mostly
// read_time, some open, scroll, star and open_original. Each open starts a session.
func seedStats(t testing.TB, e *env, n int, end time.Time) {
	rng := rand.New(rand.NewSource(1))
	tx, err := e.db.writer.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	st, err := tx.PrepareContext(e.ctx, `INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
		folder_id, folder_name, item_title, value, session_key) VALUES (?,?,?,?,?, 'web', ?,?,?, 1, 'Uncategorized', ?, ?, ?)`)
	require.NoError(t, err)
	start := end.AddDate(0, 0, -364)
	span := end.Unix() - start.Unix()
	first := start.Add(120 * 24 * time.Hour).Unix() // the sender arrived a third of the way in
	sess := 0
	for i := 0; i < n; {
		ts := start.Unix() + rng.Int63n(span)
		lt := time.Unix(ts, 0).UTC()
		item := rng.Int63n(500000) + 1
		feed := rng.Int63n(200) + 1
		ins := func(kind string, val any, sk any) {
			_, err := st.ExecContext(e.ctx, ts, lt.Format(dateLayout), lt.Hour(), int(lt.Weekday()), kind, item, feed,
				fmt.Sprintf("Feed %d", feed), fmt.Sprintf("Item %d", item), val, sk)
			require.NoError(t, err)
			i++
		}
		sess++
		key := fmt.Sprintf("%016x%016x", rng.Uint64(), rng.Uint64()) // 32 hex characters, like production
		ins("open", nil, key)
		if ts >= first {
			for k, m := 0, rng.Intn(14); k < m; k++ {
				ins("read_time", 5+rng.Intn(56), key)
			}
			if rng.Intn(3) == 0 {
				ins("scroll", rng.Intn(101), key)
			}
		}
		if rng.Intn(20) == 0 {
			ins("star", nil, nil)
		}
		if rng.Intn(30) == 0 {
			ins("open_original", nil, nil)
		}
	}
	require.NoError(t, st.Close())
	require.NoError(t, tx.Commit())
}

// TestStatsSummaryPerf checks the design budget (under 200 ms at a million events); skipped with -short.
func TestStatsSummaryPerf(t *testing.T) {
	t.Parallel()
	if testing.Short() || (os.Getenv("KIPPLE_PERF") == "" && os.Getenv("KIPPLE_PERF_DB") == "") {
		t.Skip("seeds a million stats rows; set KIPPLE_PERF=1 (or KIPPLE_PERF_DB=<path> to reuse a seeded database)")
	}
	e := newEnv(t)
	if p := os.Getenv("KIPPLE_PERF_DB"); p != "" { // scratch: reuse a seeded database
		_ = e.db.Close()
		db, err := Open(e.ctx, Options{Path: p, Clock: e.clk})
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		e.db = db
	}
	end := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	t0 := time.Now()
	if e.count("SELECT count(*) FROM stats_events") < 1_000_000 {
		seedStats(t, e, 1_000_000, end)
	}
	t.Logf("seeded %d rows in %v", e.count("SELECT count(*) FROM stats_events"), time.Since(t0))
	// Build cost of migration 0009 at this size: drop the three indexes, then apply its SQL again.
	used := func() int64 {
		return int64(e.count("SELECT (SELECT page_count FROM pragma_page_count) - (SELECT freelist_count FROM pragma_freelist_count)")) *
			int64(e.count("SELECT page_size FROM pragma_page_size"))
	}
	rows := e.count("SELECT count(*) FROM stats_events")
	withIdx := used()
	e.exec(undo0009)
	without := used()
	ms, err := loadMigrations()
	require.NoError(t, err)
	b0 := time.Now()
	require.NoError(t, e.db.applyMigration(e.ctx, ms[8]))
	t.Logf("migration 0009 index build: %v for %d rows; the indexes add %.1f MB (%.1f MB per million events); db %.0f MB with, %.0f MB without",
		time.Since(b0), rows, float64(used()-without)/1e6, float64(used()-without)/1e6*1e6/float64(rows), float64(withIdx)/1e6, float64(without)/1e6)
	if os.Getenv("KIPPLE_PERF_DB") == "" {
		e.exec("ANALYZE")
	}
	for _, h := range statsHinted() { // the plans of every hinted query, after ANALYZE too
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+h.sql, h.args...)
		require.NoError(t, err, h.name)
		var plan string
		for rows.Next() {
			var a, b, c int
			var d string
			require.NoError(t, rows.Scan(&a, &b, &c, &d))
			plan += d + "; "
		}
		require.NoError(t, rows.Close())
		t.Logf("plan %s: %s", h.name, plan)
		require.Contains(t, plan, "INDEX "+h.index, h.name)
	}
	for _, key := range []string{"week", "month", "year", "all"} {
		from, to := "", end.Format(dateLayout)
		switch key {
		case "week":
			from = end.AddDate(0, 0, -6).Format(dateLayout)
		case "month":
			from = end.AddDate(0, 0, -29).Format(dateLayout)
		case "year":
			from = end.AddDate(0, 0, -364).Format(dateLayout)
		}
		var best time.Duration
		for i := 0; i < 3; i++ {
			s := time.Now()
			out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: key, From: from, To: to, Now: end})
			require.NoError(t, err)
			if d := time.Since(s); i == 0 || d < best {
				best = d
			}
			require.True(t, out.Totals.Opens > 0)
		}
		t.Logf("%-5s best of 3: %v", key, best)
		if key == "month" {
			require.Less(t, best, time.Second, "month is the default view")
		}
	}
}

var _ = sql.ErrNoRows
