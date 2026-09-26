package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The fixed "now" of the auto-read tests: 2026-11-02 12:00 New York, the day after the fall-back
// DST change (EDT to EST), so every window below spans a 25-hour local day.
var arNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.FixedZone("EST", -5*3600))

type arEnv struct {
	t    *testing.T
	db   *DB
	feed int64
	n    int64
}

func newAREnv(t *testing.T) *arEnv {
	t.Helper()
	db, _ := openTest(t)
	e := &arEnv{t: t, db: db}
	e.feed = e.addFeed("https://a/f")
	return e
}

func (e *arEnv) exec(q string, args ...any) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (e *arEnv) addFeed(url string) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES (?1, ?1, 'a') RETURNING id`, url).Scan(&id)
	}))
	return id
}

// item inserts an unread item crawled `age` before arNow (its id is that instant in microseconds).
func (e *arEnv) item(feed int64, age time.Duration) int64 {
	e.t.Helper()
	e.n++
	id := arNow.Add(-age).UnixMicro() + e.n // +n keeps two items of one age distinct
	e.exec(`INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash) VALUES (?1, ?2, 1, 1, ?3, 'c', 't')`,
		id, feed, fmt.Sprint("u", id))
	return id
}

func (e *arEnv) read(id int64) bool {
	e.t.Helper()
	return scalar[int](e.t, e.db.Reader(), "SELECT read FROM items WHERE id = ?", id) == 1
}

func (e *arEnv) setDays(feed int64, days any) {
	e.t.Helper()
	e.exec("UPDATE feeds SET auto_read_days = ? WHERE id = ?", days, feed)
}

const h = time.Hour

func TestAutoReadBoundsAreUnixArithmeticAcrossDST(t *testing.T) {
	// Local-calendar arithmetic across the 2026-11-01 fall-back would land an hour off; the window
	// is unix seconds, so ten days is exactly 864,000 s whatever the zone did in between.
	since := time.Date(2026, 11, 1, 12, 0, 0, 0, time.FixedZone("EST", -5*3600))
	lo, hi := autoReadBounds(arNow, since, 10)
	require.Equal(t, (arNow.Unix()-10*86400)*1_000_000, hi)
	require.Equal(t, (since.Unix()-10*86400)*1_000_000, lo)
	require.Equal(t, int64(86400*1_000_000), hi-lo, "the window is one unix day")
	// The same instants built in a DST-observing zone give the same numbers.
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	lo2, hi2 := autoReadBounds(arNow.In(ny), since.In(ny), 10)
	require.Equal(t, [2]int64{lo, hi}, [2]int64{lo2, hi2})
	l0, _ := autoReadBounds(arNow, time.Time{}, 10)
	require.Zero(t, l0, "a zero Since is the catch-up: no lower bound")
}

func TestAutoReadWindowMarksOnlyItemsCrossingSinceTheLastRun(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	e.setDays(e.feed, 10)
	since := arNow.Add(-24 * h)
	old := e.item(e.feed, 20*24*h)      // crossed long before the last run: not in the window
	in1 := e.item(e.feed, 10*24*h+2*h)  // crossed since the last run
	in2 := e.item(e.feed, 10*24*h+23*h) // ditto, near the lower edge
	fresh := e.item(e.feed, 24*h)       // younger than 10 days
	edge := e.item(e.feed, 10*24*h-2*h) // not yet 10 days old
	res, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow, Since: since})
	require.NoError(t, err)
	require.Equal(t, 2, res.Items)
	require.True(t, e.read(in1) && e.read(in2))
	require.False(t, e.read(old) || e.read(fresh) || e.read(edge))

	// A manual mark-unread sticks: the next night's window has moved on.
	require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := SetRead(ctx, tx, []int64{in1}, false, arNow.Unix())
		return err
	}))
	next := arNow.Add(24 * h)
	res, err = e.db.RunAutoRead(ctx, AutoReadOptions{Now: next, Since: arNow})
	require.NoError(t, err)
	require.Equal(t, 1, res.Items, "only what crossed in the new window (edge, now 10 days and 22 hours old)")
	require.False(t, e.read(in1), "an article marked unread again stays unread")
	require.True(t, e.read(edge))
	require.False(t, e.read(old))
}

func TestAutoReadDowntimeIsCoveredByTheWindowStart(t *testing.T) {
	// The server was down for three nights: the window starts at the last completed run, so the
	// three days' crossings are all caught, and nothing older is.
	e := newAREnv(t)
	e.setDays(e.feed, 7)
	last := arNow.Add(-3 * 24 * h)
	older := e.item(e.feed, 7*24*h+3*24*h+5*h) // crossed before the last run
	var got []int64
	for _, hrs := range []time.Duration{1, 30, 60, 71} {
		got = append(got, e.item(e.feed, 7*24*h+hrs*h))
	}
	res, err := e.db.RunAutoRead(context.Background(), AutoReadOptions{Now: arNow, Since: last})
	require.NoError(t, err)
	require.Equal(t, 4, res.Items)
	for _, id := range got {
		require.True(t, e.read(id))
	}
	require.False(t, e.read(older))
}

func TestAutoReadSparesStarredMutedHeldAndReadItems(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	e.setDays(e.feed, 5)
	old := 30 * 24 * h
	plain := e.item(e.feed, old)
	starred := e.item(e.feed, old)
	e.exec("UPDATE items SET starred = 1 WHERE id = ?", starred)
	muted := e.item(e.feed, old)
	e.exec("UPDATE items SET read = 1, muted_by = 9, muted_was_read = 0 WHERE id = ?", muted)
	heldFuture := e.item(e.feed, old)
	e.exec("UPDATE items SET retain_until = ? WHERE id = ?", arNow.Unix()+3600, heldFuture)
	heldPast := e.item(e.feed, old)
	e.exec("UPDATE items SET retain_until = ? WHERE id = ?", arNow.Unix()-3600, heldPast)
	already := e.item(e.feed, old)
	e.exec("UPDATE items SET read = 1, read_at = 5 WHERE id = ?", already)

	res, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow})
	require.NoError(t, err)
	require.Equal(t, 2, res.Items, "the plain one and the one whose hold has lapsed")
	require.True(t, e.read(plain) && e.read(heldPast))
	require.False(t, e.read(starred) || e.read(heldFuture))
	require.Equal(t, 1, scalar[int](t, e.db.Reader(), "SELECT muted_by IS NOT NULL FROM items WHERE id = ?", muted))
	require.EqualValues(t, 5, scalar[int](t, e.db.Reader(), "SELECT read_at FROM items WHERE id = ?", already), "an already-read item is not rewritten")
	require.Equal(t, arNow.Unix(), int64(scalar[int](t, e.db.Reader(), "SELECT read_at FROM items WHERE id = ?", plain)))
}

func TestAutoReadCatchUpEqualsPreviewAndInheritsTheGlobalDefault(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	other := e.addFeed("https://b/f")
	off := e.addFeed("https://c/f")
	own := e.addFeed("https://d/f")
	e.setDays(off, 0)   // off for this feed even though the global default is on
	e.setDays(own, 100) // its own, longer threshold
	require.NoError(t, e.db.SetSettings(ctx, map[string]any{SettingAutoReadDays: 30}))
	for range 3 {
		e.item(e.feed, 40*24*h)
		e.item(other, 31*24*h)
	}
	for range 4 {
		e.item(off, 400*24*h)
		e.item(e.feed, 10*24*h) // younger than 30 days
	}
	e.item(own, 60*24*h) // under its own 100 days
	e.item(own, 101*24*h)

	pv, err := e.db.PreviewAutoRead(ctx, arNow, 0, nil)
	require.NoError(t, err)
	require.Equal(t, 7, pv.Total)
	require.Len(t, pv.Feeds, 3)
	require.Equal(t, []int{3, 3, 1}, []int{pv.Feeds[0].Count, pv.Feeds[1].Count, pv.Feeds[2].Count})
	require.Equal(t, 100, pv.Feeds[2].Days)

	// "What if" previews change nothing stored: the global default 0 turns everything but own off.
	zero := 0
	pv0, err := e.db.PreviewAutoRead(ctx, arNow, 0, &zero)
	require.NoError(t, err)
	require.Equal(t, 1, pv0.Total)
	one, err := e.db.PreviewAutoRead(ctx, arNow, e.feed, ptr(20))
	require.NoError(t, err)
	require.Equal(t, 3, one.Total)

	var batches []int
	res, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow, Batch: 2, OnBatch: func(r StateResult) { batches = append(batches, len(r.Changed)) }})
	require.NoError(t, err)
	require.Equal(t, pv.Total, res.Items, "the catch-up changes exactly what the preview counted")
	require.Equal(t, 3, res.Feeds)
	require.Equal(t, []int{2, 1, 2, 1, 1}, batches, "batches of at most 2, feed by feed, no empty batch reported")
	again, err := e.db.PreviewAutoRead(ctx, arNow, 0, nil)
	require.NoError(t, err)
	require.Zero(t, again.Total)
}

func ptr[T any](v T) *T { return &v }

func TestAutoReadMarksTheTrimmedLedgerAndUnreadCountsAgree(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	e.setDays(e.feed, 10)
	for range 3 {
		e.item(e.feed, 20*24*h)
	}
	e.item(e.feed, 1*h)
	ledOld := arNow.Add(-40 * 24 * h).UnixMicro()
	ledNew := arNow.Add(-2 * 24 * h).UnixMicro()
	e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (?1, ?3, 'l1', 0, 1, 1), (?2, ?3, 'l2', 0, 1, 1)`, ledOld, ledNew, e.feed)

	before, err := e.db.UnreadCounts(ctx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 4, before[0].Count)
	res, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow})
	require.NoError(t, err)
	require.Equal(t, 3, res.Items)
	require.Equal(t, 1, res.Ledger)
	after, err := e.db.UnreadCounts(ctx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, after[0].Count, "what the Reader API reports as unread")
	total, err := e.db.UnreadTotal(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, 0, scalar[int](t, e.db.Reader(), "SELECT read FROM trimmed_items WHERE uid = 'l2'"), "a young ledger row stays unread")
	require.Equal(t, 1, scalar[int](t, e.db.Reader(), "SELECT read FROM trimmed_items WHERE uid = 'l1'"))
}

func TestAutoReadNeverTouchesTheArchiveFeed(t *testing.T) {
	e := newAREnv(t)
	arch := e.addFeed("https://arch/f")
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive' WHERE id = ?", arch)
	require.NoError(t, e.db.SetSettings(context.Background(), map[string]any{SettingAutoReadDays: 1}))
	id := e.item(arch, 90*24*h)
	res, err := e.db.RunAutoRead(context.Background(), AutoReadOptions{Now: arNow})
	require.NoError(t, err)
	require.Zero(t, res.Items)
	require.False(t, e.read(id))
}

func TestAutoReadLastRunRoundTrips(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	_, ok, err := AutoReadLastRun(ctx, e.db.Reader())
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, e.db.RecordAutoReadRun(ctx, arNow))
	got, ok, err := AutoReadLastRun(ctx, e.db.Reader())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, arNow.Unix(), got.Unix())
	m, err := e.db.MergedSettings(ctx)
	require.NoError(t, err)
	require.NotContains(t, m, settingAutoReadLastRun, "sys.* rows never reach the UI")
}

func TestAutoReadCancelledContextStops(t *testing.T) {
	e := newAREnv(t)
	e.setDays(e.feed, 1)
	e.item(e.feed, 10*24*h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow})
	require.Error(t, err)
}

// A failed settings read is an error, never "off" or "never ran": read as 0 it would give the
// nightly step an empty window that recording the run then closes for good.
func TestAutoReadSettingsReadFailureIsAnError(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	require.NoError(t, e.db.SetSettings(ctx, map[string]any{SettingAutoReadDays: 7}))
	require.NoError(t, e.db.RecordAutoReadRun(ctx, arNow))

	rename := func(q string) {
		require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, q)
			return err
		}))
	}
	rename("ALTER TABLE settings RENAME TO settings_away") // every settings read now fails with a real SQL error
	repair := func() { rename("ALTER TABLE settings_away RENAME TO settings") }
	_, err := e.db.GlobalAutoReadDays(ctx)
	require.Error(t, err)
	_, _, err = AutoReadLastRun(ctx, e.db.Reader())
	require.Error(t, err)
	_, err = e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow, Since: arNow.Add(-time.Hour)})
	require.Error(t, err, "no targets can be chosen without the global days")
	_, err = e.db.PreviewAutoRead(ctx, arNow, 0, nil)
	require.Error(t, err)
	repair()

	days, err := e.db.GlobalAutoReadDays(ctx)
	require.NoError(t, err)
	require.Equal(t, 7, days)
}

// A run that stops at feed 2 leaves feed 1 with its own finished window: a reader's mark-unread in
// feed 1 is not undone by the rerun, which still does feed 2.
func TestAutoReadPerFeedMarksMakeAFailedRunIdempotent(t *testing.T) {
	e := newAREnv(t)
	f2 := e.addFeed("https://b/f")
	e.setDays(e.feed, 10)
	e.setDays(f2, 10)
	since := arNow.Add(-24 * h)
	ten := 10 * 24 * h
	x := e.item(e.feed, ten+12*h) // crossed 10 days 12 h ago, inside (since, now]
	y := e.item(f2, ten+6*h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := AutoReadOptions{Now: arNow, Since: since, PerFeedMarks: true}
	o.OnBatch = func(StateResult) { cancel() } // the run is cut right after feed 1's batch
	res, err := e.db.RunAutoRead(ctx, o)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, res.Items)
	require.True(t, e.read(x))
	require.False(t, e.read(y))

	e.exec("UPDATE items SET read = 0, read_at = NULL WHERE id = ?", x) // the reader marks it unread
	res, err = e.db.RunAutoRead(context.Background(), AutoReadOptions{Now: arNow.Add(time.Hour), Since: since, PerFeedMarks: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.Items, "only feed 2 is redone")
	require.False(t, e.read(x), "feed 1's finished window is not repeated")
	require.True(t, e.read(y))

	// Completing the run clears the marks.
	require.NoError(t, e.db.RecordAutoReadRun(context.Background(), arNow.Add(time.Hour)))
	m, err := loadAutoReadMarks(context.Background(), e.db.Reader())
	require.NoError(t, err)
	require.Empty(t, m)
}

// Feeds that finished with nothing to mark are remembered too (stored when the run ends early).
func TestAutoReadPerFeedMarksCoverFeedsWithNothingToMark(t *testing.T) {
	e := newAREnv(t)
	f2 := e.addFeed("https://b/f")
	e.setDays(e.feed, 10)
	e.setDays(f2, 10)
	since := arNow.Add(-24 * h)
	y := e.item(f2, 10*24*h+6*h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := AutoReadOptions{Now: arNow, Since: since, PerFeedMarks: true}
	cancel() // cut before anything completes
	_, err := e.db.RunAutoRead(ctx, o)
	require.ErrorIs(t, err, context.Canceled)
	m, err := loadAutoReadMarks(context.Background(), e.db.Reader())
	require.NoError(t, err)
	require.Empty(t, m, "nothing completed before the cut")

	// Feed 1 completes with nothing to mark; a write error in feed 2 stops the run.
	e.exec(`CREATE TRIGGER boom BEFORE UPDATE ON items WHEN NEW.feed_id = ` + fmt.Sprint(f2) + ` BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	_, err = e.db.RunAutoRead(context.Background(), AutoReadOptions{Now: arNow, Since: since, PerFeedMarks: true})
	require.Error(t, err)
	m, err = loadAutoReadMarks(context.Background(), e.db.Reader())
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{e.feed: arNow.Unix()}, m)
	require.False(t, e.read(y))
}

// A night with nothing to mark takes the commit gate never; a night with candidates does.
func TestAutoReadNothingToMarkTakesNoGate(t *testing.T) {
	e := newAREnv(t)
	for i := 0; i < 5; i++ {
		e.setDays(e.addFeed(fmt.Sprint("https://n", i, "/f")), 10)
	}
	e.setDays(e.feed, 10)
	e.item(e.feed, 3*24*h) // far too young
	release, err := e.db.AcquireGate(context.Background())
	require.NoError(t, err)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	o := AutoReadOptions{Now: arNow, Since: arNow.Add(-24 * h), PerFeedMarks: true}
	res, err := e.db.RunAutoRead(ctx, o)
	require.NoError(t, err, "no candidate, so no gate wait")
	require.Zero(t, res.Batches)

	old := e.item(e.feed, 10*24*h+6*h)
	short, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	_, err = e.db.RunAutoRead(short, o)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a candidate needs the gate, which is held")
	require.False(t, e.read(old))
}

// A disabled feed can never clear itself (nothing new arrives, so nothing is read by scrolling
// past), so auto-read includes it; an archived feed is a keepsake and is left alone.
func TestAutoReadIncludesDisabledFeedsAndSkipsArchived(t *testing.T) {
	e := newAREnv(t)
	ctx := context.Background()
	require.NoError(t, e.db.SetSettings(ctx, map[string]any{SettingAutoReadDays: 7}))
	disabled := e.addFeed("https://dis/f")
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'user' WHERE id = ?", disabled)
	broken := e.addFeed("https://brk/f")
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'gone' WHERE id = ?", broken)
	archive := e.addFeed("https://arc/f")
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive' WHERE id = ?", archive)
	a, b, c := e.item(disabled, 30*24*h), e.item(broken, 30*24*h), e.item(archive, 30*24*h)

	_, err := e.db.RunAutoRead(ctx, AutoReadOptions{Now: arNow})
	require.NoError(t, err)
	require.True(t, e.read(a) && e.read(b), "disabled feeds are included")
	require.False(t, e.read(c), "archived feeds are left alone")
}
