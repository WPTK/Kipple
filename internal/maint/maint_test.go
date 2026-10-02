package maint

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata" // the tz setting resolves IANA names even where the OS has no zoneinfo

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/imgcache"
	"github.com/WPTK/kipple/internal/store"
)

const day = int64(86400)

type env struct {
	t    *testing.T
	db   *store.DB
	clk  *clock.Fake
	feed int64
	jobs chan Job
}

// newEnv opens a store on a fake clock set to at (local time).
func newEnv(t *testing.T, at time.Time) *env {
	t.Helper()
	clk := clock.NewFake(at)
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e := &env{t: t, db: db, clk: clk, jobs: make(chan Job, 64)}
	// An existing install's zone (migration 0010 writes it); a new one defaults to UTC.
	require.NoError(t, db.SetSettings(context.Background(), map[string]any{"tz": "America/New_York"}))
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES ('https://a/f','a/f','a') RETURNING id`).Scan(&e.feed)
	}))
	return e
}

func (e *env) start(o Options) *Maint {
	o.DB, o.Clock = e.db, e.clk
	o.OnJob = func(j Job) { e.jobs <- j }
	m := New(o)
	m.Start()
	e.t.Cleanup(m.Stop)
	return m
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}))
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.Reader().QueryRowContext(context.Background(), q, args...).Scan(&n))
	return n
}

// ledger seeds ledger rows first..first+n-1 (with stubs when stub).
func (e *env) ledger(first, n, trimmedAt, lastSeen int64, stub bool) {
	e.t.Helper()
	e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?2 - 1)
		INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
		SELECT i, ?3, 'u' || i, 0, ?4, ?5 FROM s`, first, n, e.feed, trimmedAt, lastSeen)
	if stub {
		e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?2 - 1)
			INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash,
			  url, title, author, content_html, content_text)
			SELECT i, 1, 1, 1, 'c', 't', 'https://a/x', 'T', '', '<p>x</p>', 'x' FROM s`, first, n)
	}
}

func (e *env) waitJob(name string) Job {
	e.t.Helper()
	for {
		select {
		case j := <-e.jobs:
			if j.Name == name {
				return j
			}
		case <-time.After(10 * time.Second):
			e.t.Fatalf("no %s job within 10s", name)
		}
	}
}

func (e *env) noJob(d time.Duration) {
	e.t.Helper()
	select {
	case j := <-e.jobs:
		e.t.Fatalf("unexpected job %s", j.Name)
	case <-time.After(d):
	}
}

// local returns a local-zone time on a fixed 2026 date (23 Sep is a Wednesday,
// 27 Sep a Sunday).
// newEnv sets the tz setting to America/New_York; the tests do not depend on the machine's TZ.
var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return l
}()

// noNightly fails on any job but the hourly checkpoint within d.
func (e *env) noNightly(d time.Duration) {
	e.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case j := <-e.jobs:
			if j.Name != "checkpoint" {
				e.t.Fatalf("unexpected job %s", j.Name)
			}
		case <-deadline:
			return
		}
	}
}

func local(day, h, m int) time.Time { return time.Date(2026, 9, day, h, m, 0, 0, newYork) }

// A Start after Stop (an account created while the server shuts down) must not
// bring maintenance back.
func TestStartAfterStopStaysStopped(t *testing.T) {
	e := newEnv(t, local(23, 12, 0))
	m := New(Options{DB: e.db, Clock: e.clk})
	m.Stop()
	m.Start()
	require.Nil(t, m.cancel, "Start after Stop started nothing")
	m.Stop()
}

func TestHourlyCheckpointFires(t *testing.T) {
	e := newEnv(t, local(23, 12, 0))
	e.start(Options{})

	e.clk.Advance(30 * time.Minute)
	e.noJob(150 * time.Millisecond) // not an hour yet, not 04:10

	e.clk.Advance(31 * time.Minute)
	require.NoError(t, e.waitJob("checkpoint").Err)

	e.clk.Advance(time.Hour)
	require.NoError(t, e.waitJob("checkpoint").Err)
}

func TestNightlyPurgesExactlyTheExpiredRows(t *testing.T) {
	e := newEnv(t, local(27, 4, 0)) // a Sunday: the FTS integrity check runs too
	now := e.clk.Now().Unix()

	e.ledger(1, 3, now-91*day, now, true)            // stub expired, ledger kept
	e.ledger(10, 3, now-10*day, now, true)           // unexpired stubs
	e.ledger(20, 2, now-300*day, now-181*day, true)  // ledger expired (stub goes with it)
	e.ledger(30, 2, now-200*day, now-179*day, false) // ledger still inside 180 days
	// Starred and held items are live rows, never touched by any purge.
	e.exec(`INSERT INTO items (id, feed_id, uid, starred, published_at, sort_at, content_hash, text_hash, url, title, author)
		VALUES (100, ?1, 's', 1, 1, 1, 'c', 't', 'https://a/s', 'starred', '')`, e.feed)
	e.exec(`INSERT INTO items (id, feed_id, uid, retain_until, published_at, sort_at, content_hash, text_hash, url, title, author)
		VALUES (101, ?1, 'h', ?2, 1, 1, 'c', 't', 'https://a/h', 'held', '')`, e.feed, now+day)
	e.exec(`INSERT INTO sessions (id, created_at, last_seen_at, expires_at) VALUES ('old', 1, 1, ?1), ('live', 1, 1, ?2)`, now-day, now+day)

	e.exec(`INSERT INTO devices (id, created_at, last_seen_at) VALUES ('device-old-000000000', 1, ?1), ('device-new-000000000', 1, ?2)`,
		now-401*day, now-399*day)

	e.start(Options{})
	e.clk.Advance(11 * time.Minute)

	require.EqualValues(t, 3+2, e.waitJob("purge_stubs").Rows) // ids 1-3 and 20-21
	require.EqualValues(t, 2, e.waitJob("purge_ledger").Rows)
	require.EqualValues(t, 1, e.waitJob("purge_sessions").Rows)
	require.EqualValues(t, 1, e.waitJob("purge_devices").Rows)
	require.Equal(t, []int{0, 1}, []int{e.count("SELECT count(*) FROM devices WHERE id = 'device-old-000000000'"), e.count("SELECT count(*) FROM devices WHERE id = 'device-new-000000000'")})
	require.NoError(t, e.waitJob("optimize").Err)
	require.NoError(t, e.waitJob("snapshot").Err)

	require.Equal(t, 3, e.count("SELECT count(*) FROM trimmed_content"), "only the unexpired stubs remain")
	require.Equal(t, 3+3+2, e.count("SELECT count(*) FROM trimmed_items"), "ledger rows outlive their stubs until 180 days")
	require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE id IN (100, 101)"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM sessions WHERE id = 'live'"))
	require.Equal(t, 0, e.count("SELECT count(*) FROM sessions WHERE id = 'old'"))
	require.FileExists(t, filepath.Join(e.db.BackupDir(), store.SnapshotName))
	require.Equal(t, 1, e.count("SELECT count(*) FROM settings WHERE key = 'sys.last_snapshot_at'"))

	// The next run is tomorrow at 04:10, not on the next tick.
	e.clk.Advance(time.Minute)
	e.noJob(150 * time.Millisecond)
}

func TestPurgeRunsInBatchesAndYieldsTheWriter(t *testing.T) {
	e := newEnv(t, local(23, 4, 0))
	now := e.clk.Now().Unix()
	e.ledger(1, 5000, now-200*day, now, true) // 5000 expired stubs

	var (
		mu      sync.Mutex
		batches int
		once    sync.Once
		gotIn   bool
		left    int
	)
	e.start(Options{BatchSize: 1000, Pause: time.Nanosecond, AfterBatch: func(job string, n int64) {
		if job != "purge_stubs" {
			return
		}
		mu.Lock()
		batches++
		mu.Unlock()
		once.Do(func() {
			// Called between batches with no lock held: a writer (through the
			// commit gate, like a fetch commit) must get in while 4000 rows remain.
			left = e.count("SELECT count(*) FROM trimmed_content")
			release, err := e.db.AcquireGate(context.Background())
			require.NoError(t, err)
			defer release()
			require.NoError(t, e.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES ('probe', '1')`)
				return err
			}))
			gotIn = true
		})
	}})
	e.clk.Advance(11 * time.Minute)

	j := e.waitJob("purge_stubs")
	require.NoError(t, j.Err)
	require.EqualValues(t, 5000, j.Rows)
	require.GreaterOrEqual(t, j.Batches, 5)
	mu.Lock()
	require.GreaterOrEqual(t, batches, 5)
	mu.Unlock()
	require.True(t, gotIn, "a writer got in between batches")
	require.Equal(t, 4000, left)
	require.Equal(t, 0, e.count("SELECT count(*) FROM trimmed_content"))
}

func TestStopCancelsARunningPurgePromptly(t *testing.T) {
	e := newEnv(t, local(23, 4, 0))
	now := e.clk.Now().Unix()
	e.ledger(1, 5000, now-200*day, now, true)

	first := make(chan struct{}, 1)
	m := e.start(Options{BatchSize: 100, Pause: time.Hour, AfterBatch: func(string, int64) {
		select {
		case first <- struct{}{}:
		default:
		}
	}})
	e.clk.Advance(11 * time.Minute)
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("purge never started")
	}

	began := time.Now()
	m.Stop()
	require.Less(t, time.Since(began), 2*time.Second)
	m.Stop() // idempotent

	require.Equal(t, 4900, e.count("SELECT count(*) FROM trimmed_content"), "one batch ran, the rest was cancelled")
	// No snapshot is attempted after cancellation.
	require.NoFileExists(t, filepath.Join(e.db.BackupDir(), store.SnapshotName))
}

func TestNightlyPassedAndBaseline(t *testing.T) {
	at := DefaultNightlyAt
	require.False(t, nightlyPassed(local(23, 4, 9), at, newYork))
	require.True(t, nightlyPassed(local(23, 4, 10), at, newYork))
	day := func(t time.Time, loc *time.Location) string { return t.In(loc).Format(dateFmt) }
	require.Equal(t, "2026-09-22", day(baseline(local(23, 3, 59), at, newYork), newYork), "today's run is still ahead")
	require.Equal(t, "2026-09-23", day(baseline(local(23, 4, 10), at, newYork), newYork), "today's time has passed: the run is tomorrow's")
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	// 03:59 in New York is 16:59 in Tokyo the same date.
	require.True(t, nightlyPassed(local(23, 3, 59), at, tokyo))
	require.Equal(t, "2026-09-23", day(baseline(local(23, 3, 59), at, tokyo), tokyo))
}

func (e *env) setTZ(name string) {
	e.t.Helper()
	require.NoError(e.t, e.db.SetSettings(context.Background(), map[string]any{"tz": name}))
}

// nightlyOnce waits for the nightly to run (its optimize job) and then checks
// that no second one follows within d.
func (e *env) nightlyOnce(d time.Duration) {
	e.t.Helper()
	require.NoError(e.t, e.waitJob("snapshot").Err)
	e.noNightly(d)
}

// Changing tz right after the nightly run neither repeats that local date nor
// skips the next one, in a zone ahead of or behind the old one.
func TestTzChangeAfterTheRunDoesNotRunTwiceOrSkip(t *testing.T) {
	for _, tc := range []struct {
		zone string
		// minutes from 04:20 NY on the 23rd to the next expected run
		wait time.Duration
	}{
		{"Asia/Tokyo", 10*time.Hour + 50*time.Minute}, // Tokyo 04:10 on the 24th is 15:10 NY on the 23rd
		// Honolulu is behind: see TestTzChangeToAZoneBehindDoesNotSkipALocalDate.
		{"America/Los_Angeles", 26*time.Hour + 50*time.Minute},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			e := newEnv(t, local(23, 3, 59))
			e.start(Options{})
			e.clk.Advance(12 * time.Minute) // 04:11 NY: the run for the 23rd
			e.nightlyOnce(200 * time.Millisecond)

			e.setTZ(tc.zone)
			e.clk.Advance(9 * time.Minute) // 04:20 NY: the new zone's date is not later than the last run's
			e.noNightly(300 * time.Millisecond)
			e.clk.Advance(tc.wait - time.Minute)
			e.noNightly(300 * time.Millisecond)
			e.clk.Advance(time.Minute)
			e.nightlyOnce(200 * time.Millisecond)
		})
	}
}

// The last run was recorded as an instant, so a zone further behind reads it in
// ITS calendar: the run at 04:11 NY on the 23rd is 22:11 on the 22nd in Honolulu, and
// Honolulu's own 23rd has not run. Comparing the old zone's date string (the 23rd)
// used to skip that date and wait about a day longer.
func TestTzChangeToAZoneBehindDoesNotSkipALocalDate(t *testing.T) {
	e := newEnv(t, local(23, 3, 59))
	e.start(Options{})
	e.clk.Advance(12 * time.Minute) // 04:11 NY: the run for the 23rd
	e.nightlyOnce(200 * time.Millisecond)

	e.setTZ("Pacific/Honolulu")
	e.clk.Advance(9 * time.Minute) // 04:20 NY = 22:20 on the 22nd in Honolulu
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(5*time.Hour + 49*time.Minute) // 10:09 NY = 04:09 on the 23rd in Honolulu
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(time.Minute) // Honolulu 04:10 on the 23rd
	e.nightlyOnce(200 * time.Millisecond)
	e.clk.Advance(24 * time.Hour) // and the 24th
	e.nightlyOnce(200 * time.Millisecond)
}

// A night missed while the server was down runs a few minutes after startup, not
// on the first tick, so it does not overlap the startup fetch burst.
func TestCatchUpRunWaitsAfterStartup(t *testing.T) {
	e := newEnv(t, local(23, 3, 59))
	m := e.start(Options{})
	e.clk.Advance(12 * time.Minute)
	e.nightlyOnce(200 * time.Millisecond)
	m.Stop()

	e.clk.Advance(48 * time.Hour) // down across two run times: 04:11 on the 25th
	e.start(Options{})
	e.clk.Advance(time.Minute)
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(3 * time.Minute) // 4 minutes after start
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(time.Minute) // 5 minutes
	e.nightlyOnce(200 * time.Millisecond)
}

// The delay is only for the catch-up: an on-schedule run is not held back, and a
// database that recorded only the local date (before the instant existed) still works.
func TestOnScheduleRunHasNoDelayAndOldDateSettingStillWorks(t *testing.T) {
	e := newEnv(t, local(23, 3, 59))
	e.exec(`INSERT INTO settings(key, value, updated_at) VALUES('sys.last_nightly_date', '"2026-09-22"', 1)`)
	e.start(Options{})
	e.clk.Advance(12 * time.Minute) // 04:11: due, and the server was up over the run time
	e.nightlyOnce(200 * time.Millisecond)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM settings WHERE key = 'sys.last_nightly_at'`))
}

// Moving the zone earlier before the night's run must not skip that date: the
// run for the 23rd happens as soon as the new zone's 04:10 has passed.
func TestTzChangeBeforeTheRunDoesNotSkipTheDate(t *testing.T) {
	e := newEnv(t, local(23, 3, 59)) // NY: due at 04:10
	e.start(Options{})
	e.setTZ("Asia/Tokyo") // 16:59 on the 23rd in Tokyo, the 23rd has not run
	e.clk.Advance(time.Minute)
	e.nightlyOnce(200 * time.Millisecond)

	// The next one is Tokyo 04:10 on the 24th (15:10 NY on the 23rd).
	e.clk.Advance(11*time.Hour + 9*time.Minute)
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(2 * time.Minute)
	e.nightlyOnce(200 * time.Millisecond)
}

// An unknown tz keeps the previous zone (no run at the UTC fallback's 04:10,
// which is 00:10 NY... or here 09:10 NY) and logs a warning.
func TestUnknownTzKeepsThePreviousZone(t *testing.T) {
	var buf syncBuf
	e := newEnv(t, local(23, 3, 59))
	e.start(Options{Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	e.setTZ("Not/AZone")
	e.clk.Advance(6 * time.Minute) // 04:05 NY = 09:05 UTC: past 04:10 only under a UTC fallback
	e.noNightly(300 * time.Millisecond)
	e.clk.Advance(6 * time.Minute) // 04:11 NY: on time in the kept zone
	e.nightlyOnce(200 * time.Millisecond)
	require.Contains(t, buf.String(), "unknown tz setting")
	require.Equal(t, 1, strings.Count(buf.String(), "unknown tz setting"), "one warning per bad value")
}

// The last run's date survives a restart: no second run the same day, and a
// night missed while stopped runs on the first tick.
func TestNightlyDateSurvivesRestart(t *testing.T) {
	e := newEnv(t, local(23, 3, 59))
	m := e.start(Options{})
	e.clk.Advance(12 * time.Minute)
	e.nightlyOnce(200 * time.Millisecond)
	m.Stop()

	e.clk.Advance(time.Hour) // 05:20 the same day
	e.start(Options{})
	e.clk.Advance(time.Minute)
	e.noNightly(300 * time.Millisecond)

	e2 := e
	e2.clk.Advance(48 * time.Hour) // down across two runs
	e2.clk.Advance(time.Minute)
	e2.nightlyOnce(200 * time.Millisecond)
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestNightlySweepsTheImageCache(t *testing.T) {
	e := newEnv(t, local(27, 4, 0))
	var skew atomic.Int64 // seconds the cache's clock runs ahead of the fake clock
	ic, err := imgcache.Open(imgcache.Options{
		Dir: filepath.Join(t.TempDir(), "imgcache"), MaxBytes: 1 << 20, NoBackground: true,
		Now:       func() time.Time { return e.clk.Now().Add(time.Duration(skew.Load()) * time.Second) },
		DiskSpace: func(string) (uint64, uint64, error) { return 500 << 30, 800 << 30, nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ic.Close() })
	url := "http://img.example/idle.png"
	key := imgcache.KeyOrig(0, url)
	w, err := ic.Begin(key, url, 0, 10)
	require.NoError(t, err)
	_, _ = w.Write([]byte("0123456789"))
	require.NoError(t, w.Commit(imgcache.Meta{ContentType: "image/png"}))
	skew.Store(61 * 24 * 3600) // idle past the 60 day expiry

	e.start(Options{ImgCache: ic})
	e.clk.Advance(11 * time.Minute)
	j := e.waitJob("imgcache_sweep")
	require.NoError(t, j.Err)
	require.EqualValues(t, 1, j.Rows)
	require.Zero(t, ic.Stats().Files)
}
