package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// putStat inserts one stats row with an explicit local date and snapshot title.
func (e *env) putStat(kind, date string, item, feed int64, session string, value any, title string) {
	e.t.Helper()
	var sk any
	if session != "" {
		sk = session
	}
	d, err := time.ParseInLocation(dateLayout, date, time.UTC)
	require.NoError(e.t, err)
	e.exec(`INSERT INTO stats_events (ts, local_date, local_hour, local_weekday, kind, client, item_id, feed_id, feed_title,
		folder_id, folder_name, item_title, value, session_key) VALUES (?,?,9,?,?, 'web', ?,?,?, 7, 'Snap folder', 'Item', ?, ?)`,
		d.Unix()+9*3600, date, int(d.Weekday()), kind, item, feed, title, value, sk)
}

func (e *env) summary(from, to string) *StatsSummary {
	e.t.Helper()
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	out, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: from, To: to, Now: base})
	require.NoError(e.t, err)
	return out
}

// Every INDEXED BY query of the summary must plan with its index (covering for the three 0009
// indexes). A hint whose index is missing fails to prepare, so a future table rebuild that forgets
// to recreate the indexes fails here.
func TestStatsSummaryPlans(t *testing.T) {
	e := newEnv(t)
	for _, h := range statsHinted() {
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+h.sql, h.args...)
		require.NoError(t, err, h.name)
		var plan string
		for rows.Next() {
			var a, b, c int
			var d string
			require.NoError(t, rows.Scan(&a, &b, &c, &d))
			plan += d + "\n"
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		want := "INDEX " + h.index
		if strings.HasSuffix(h.index, "_cov") {
			want = "COVERING INDEX " + h.index
		}
		require.GreaterOrEqual(t, strings.Count(plan, want), h.hits, "%s (%s) plan:\n%s", h.name, h.index, plan)
	}
}

// A read that fails partway through must fail the request, never return the rows seen so far.
func TestEachRowReportsMidIterationError(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 50; i++ {
		e.putStat("open", "2026-09-01", int64(i), 1, "", nil, "F")
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	rows, err := e.db.Reader().QueryContext(ctx, "SELECT id FROM stats_events")
	require.NoError(t, err)
	n := 0
	err = eachRow(rows, func() error {
		if n++; n == 1 {
			cancel()
			time.Sleep(100 * time.Millisecond) // let database/sql notice the cancellation
		}
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, n, 50)

	// The same through the summary: an already cancelled context is an error, not an empty summary.
	e.putStat("open", "2026-09-01", 1, 1, "", nil, "F")
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	cctx, ccancel := context.WithCancel(e.ctx)
	ccancel()
	_, err = StatsSummaryFor(cctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base})
	require.Error(t, err)
}

func TestStatsGroupCapsConcurrencyAndCancelsOnError(t *testing.T) {
	g := newStatsGroup(context.Background(), 3)
	var cur, peak atomic.Int32
	for i := 0; i < 12; i++ {
		g.Go(func(ctx context.Context) error {
			n := cur.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			cur.Add(-1)
			return nil
		})
	}
	require.NoError(t, g.Wait())
	require.LessOrEqual(t, peak.Load(), int32(3))
	require.GreaterOrEqual(t, peak.Load(), int32(2))

	boom := errors.New("boom")
	g = newStatsGroup(context.Background(), 3)
	var sawCancel atomic.Bool
	g.Go(func(ctx context.Context) error {
		<-ctx.Done()
		sawCancel.Store(true)
		return ctx.Err()
	})
	g.Go(func(ctx context.Context) error { return boom })
	require.ErrorIs(t, g.Wait(), boom, "the first error wins")
	require.True(t, sawCancel.Load(), "the sibling's context was cancelled")

	// A cancelled caller is an error even when no task ran.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g = newStatsGroup(ctx, 1)
	g.Go(func(context.Context) error { return nil })
	require.Error(t, g.Wait())
}

// first_event_date is the smallest local date, not the date of the lowest id.
func TestStatsFirstEventDateIsMinimum(t *testing.T) {
	e := newEnv(t)
	got, err := StatsFirstEventDate(e.ctx, e.db.Reader())
	require.NoError(t, err)
	require.Equal(t, "", got)
	e.putStat("open", "2026-09-10", 1, 1, "a", nil, "F") // lowest id, later date (written under another zone)
	e.putStat("read_time", "2026-09-05", 1, 1, "a", 20, "F")
	e.putStat("star", "2026-09-03", 2, 1, "", nil, "F")
	e.putStat("scroll", "2026-09-04", 1, 1, "a", 50, "F")
	got, err = StatsFirstEventDate(e.ctx, e.db.Reader())
	require.NoError(t, err)
	require.Equal(t, "2026-09-03", got)
	e.putStat("share", "2026-08-30", 2, 1, "", nil, "F")
	got, _ = StatsFirstEventDate(e.ctx, e.db.Reader())
	require.Equal(t, "2026-08-30", got)
	require.Equal(t, "2026-08-30", *e.summary("2026-09-01", "2026-09-24").FirstEventDate)
}

// Rows whose local date is later than today (a zone change) count as today for the streaks.
func TestStatsStreaksClampFutureDates(t *testing.T) {
	e := newEnv(t) // today is 2026-09-24 in UTC
	for i, d := range []string{"2026-09-23", "2026-09-24", "2026-09-25", "2026-09-27"} {
		e.putStat("open", d, int64(i+1), 1, "", nil, "F") // legacy opens count as reads
	}
	st := e.summary("2026-09-01", "2026-09-24").Streaks
	require.Equal(t, 2, st.Current, "23rd and 24th; the future dates fold into today")
	require.Equal(t, 2, st.Longest)
	require.Equal(t, "2026-09-24", *st.LongestEnd)
}

// Ingest that lands after the summary started is invisible to it.
type insertAfterMax struct {
	Querier
	e    *env
	done bool
}

func (q *insertAfterMax) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := q.Querier.QueryRowContext(ctx, query, args...)
	if !q.done && strings.Contains(query, "MAX(id)") {
		q.done = true
		q.e.putStat("read_time", "2026-09-20", 1, 1, "s1", 30, "F") // more time for a session already open
		q.e.putStat("open", "2026-09-20", 2, 1, "s2", nil, "F")     // a new session
		q.e.putStat("read_time", "2026-09-20", 2, 1, "s2", 30, "F")
	}
	return row
}

func TestStatsSummaryIsBoundedBySnapshot(t *testing.T) {
	e := newEnv(t)
	e.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	e.putStat("open", "2026-09-19", 9, 1, "old", nil, "F") // legacy: before the first read_time
	e.putStat("open", "2026-09-20", 1, 1, "s1", nil, "F")
	e.putStat("read_time", "2026-09-20", 1, 1, "s1", 4, "F")
	before, err := StatsSummaryFor(e.ctx, e.db.Reader(), StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base})
	require.NoError(t, err)
	require.Equal(t, 2, before.Totals.Opens)
	require.EqualValues(t, 4, before.Totals.ActiveSeconds)

	e2 := newEnv(t)
	e2.exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('tz', '\"UTC\"')")
	e2.putStat("open", "2026-09-19", 9, 1, "old", nil, "F")
	e2.putStat("open", "2026-09-20", 1, 1, "s1", nil, "F")
	e2.putStat("read_time", "2026-09-20", 1, 1, "s1", 4, "F")
	out, err := StatsSummaryFor(e2.ctx, &insertAfterMax{Querier: e2.db.Reader(), e: e2}, StatsSummaryParams{Key: "custom", From: "2026-09-01", To: "2026-09-24", Now: base})
	require.NoError(t, err)
	require.Equal(t, before.Totals, out.Totals, "rows written after the summary started are not counted")
	require.Equal(t, before.Streaks, out.Streaks)
	require.Equal(t, 6, e2.count("SELECT count(*) FROM stats_events"), "the late rows were written")
	// The next summary sees them.
	next := e2.summary("2026-09-01", "2026-09-24")
	require.Equal(t, 3, next.Totals.Opens)
	require.EqualValues(t, 64, next.Totals.ActiveSeconds)
}

// A feed with only read time in the range is named from its read_time snapshot row.
func TestStatsSourceNamedFromReadTimeSnapshot(t *testing.T) {
	e := newEnv(t)
	f := e.addFeed("https://example.com/current.xml")
	e.exec("UPDATE feeds SET title = 'Current name' WHERE id = ?", f)
	e.putStat("read_time", "2026-09-20", 1, f, "spans", 12, "Old snapshot")
	e.putStat("read_time", "2026-09-21", 1, f, "spans", 12, "Newest snapshot")
	out := e.summary("2026-09-15", "2026-09-24")
	require.Len(t, out.Sources, 1)
	src := out.Sources[0]
	require.Equal(t, "Newest snapshot", src.FeedTitle)
	require.NotNil(t, src.FolderName)
	require.Equal(t, "Snap folder", *src.FolderName)
	require.NotNil(t, src.FolderID)
	require.Equal(t, "7", *src.FolderID)
	require.EqualValues(t, 24, src.ActiveSeconds)
	require.Nil(t, src.AvgReadSeconds)
	require.Equal(t, 0, src.TimedItems)
}

func TestStatsSourcesTruncation(t *testing.T) {
	e := newEnv(t)
	for i := 1; i <= statsSourcesMax; i++ {
		e.putStat("open", "2026-09-20", int64(i), int64(i), "", nil, fmt.Sprintf("F%d", i))
	}
	out := e.summary("2026-09-15", "2026-09-24")
	require.Len(t, out.Sources, statsSourcesMax)
	require.False(t, out.SourcesTruncated, "exactly the cap is not truncated")
	e.putStat("open", "2026-09-20", 999, 999, "", nil, "One more")
	out = e.summary("2026-09-15", "2026-09-24")
	require.Len(t, out.Sources, statsSourcesMax)
	require.True(t, out.SourcesTruncated)
}

func TestStatsSourceTimedFields(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-19", 9, 1, "old", nil, "F")   // legacy read, no time
	e.putStat("open", "2026-09-20", 1, 1, "a", nil, "F")     // read, 30 s
	e.putStat("read_time", "2026-09-20", 1, 1, "a", 30, "F") //
	e.putStat("open", "2026-09-20", 2, 1, "b", nil, "F")     // read, 10 s
	e.putStat("read_time", "2026-09-20", 2, 1, "b", 10, "F") //
	e.putStat("open", "2026-09-20", 3, 1, "c", nil, "F")     // bounce: 4 s is not a read
	e.putStat("read_time", "2026-09-20", 3, 1, "c", 4, "F")  //
	out := e.summary("2026-09-15", "2026-09-24")
	require.Len(t, out.Sources, 1)
	s := out.Sources[0]
	require.EqualValues(t, 40, s.TimedSeconds, "only read opens with time")
	require.Equal(t, 2, s.TimedItems)
	require.InDelta(t, 20, *s.AvgReadSeconds, 1e-9)
	require.EqualValues(t, 44, s.ActiveSeconds)
}

// A statistics delete that commits after the summary chose its longest read, and before it looked
// up that read's title, must not fail the summary: the longest read keeps its figures without a
// title (issue #27).
func TestStatsSummarySurvivesDeleteOfTheLongestRead(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-19", 9, 1, "old", nil, "F") // legacy, so the timed rows below are not
	e.putStat("open", "2026-09-20", 1, 1, "a", nil, "Feed A")
	e.putStat("read_time", "2026-09-20", 1, 1, "a", 90, "Feed A")

	// Without the delete the longest read has its title.
	lr := e.summary("2026-09-15", "2026-09-24").Behavior.LongestRead
	require.NotNil(t, lr)
	require.Equal(t, "Item", lr.Title)
	require.Equal(t, "Feed A", lr.FeedTitle)

	calls := 0
	statsLongestTitleHook = func() {
		calls++
		n, err := StatsDelete(e.ctx, e.db, "", "", nil)
		require.NoError(t, err)
		require.Equal(t, 3, n)
	}
	t.Cleanup(func() { statsLongestTitleHook = nil })
	out := e.summary("2026-09-15", "2026-09-24") // fails the test on a summary error
	require.Equal(t, 1, calls)
	require.Zero(t, e.count("SELECT count(*) FROM stats_events"), "the delete ran inside the window")
	lr = out.Behavior.LongestRead
	require.NotNil(t, lr, "the figures computed before the delete are kept")
	require.Equal(t, "1", lr.ItemID)
	require.EqualValues(t, 90, lr.Seconds)
	require.Equal(t, "2026-09-20", lr.Date)
	require.Empty(t, lr.Title)
	require.Empty(t, lr.FeedTitle)
}

// A read_time or scroll row without a session key must not fail the summary, and must not be
// attributed to the sessions of opens that have no key either.
func TestStatsSummaryToleratesNullSessionKeys(t *testing.T) {
	e := newEnv(t)
	e.putStat("open", "2026-09-19", 9, 1, "old", nil, "F")
	e.putStat("open", "2026-09-20", 1, 1, "", nil, "F") // an open without a key
	e.putStat("read_time", "2026-09-20", 1, 1, "", 30, "F")
	e.putStat("scroll", "2026-09-20", 1, 1, "", 90, "F")
	out := e.summary("2026-09-15", "2026-09-24")
	require.Equal(t, 2, out.Totals.Opens)
	require.EqualValues(t, 30, out.Totals.ActiveSeconds, "the time still counts as active time")
	require.Equal(t, 1, out.Totals.ItemsRead, "the keyless open is not made a read by the keyless rows")
}
