package maint

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the tz setting resolves IANA names even where the OS has no zoneinfo

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
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
// The default tz setting is America/New_York; the tests do not depend on the machine's TZ.
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

	e.start(Options{})
	e.clk.Advance(11 * time.Minute)

	require.EqualValues(t, 3+2, e.waitJob("purge_stubs").Rows) // ids 1-3 and 20-21
	require.EqualValues(t, 2, e.waitJob("purge_ledger").Rows)
	require.EqualValues(t, 1, e.waitJob("purge_sessions").Rows)
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

func TestNextNightly(t *testing.T) {
	at := DefaultNightlyAt
	require.Equal(t, local(23, 4, 10), nextNightly(local(23, 3, 59), at, newYork))
	require.Equal(t, local(24, 4, 10), nextNightly(local(23, 4, 10), at, newYork))
	require.Equal(t, local(24, 4, 10), nextNightly(local(23, 23, 0), at, newYork))
}

func TestNextNightlyUsesTheGivenZone(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	// 03:59 in New York is 16:59 in Tokyo: Tokyo's next 04:10 is 11 minutes past 04:00 NY + 11h.
	now := local(23, 3, 59)
	got := nextNightly(now, DefaultNightlyAt, tokyo)
	require.Equal(t, time.Date(2026, 9, 24, 4, 10, 0, 0, tokyo), got)
	require.True(t, got.After(local(23, 4, 10)))
}

// A changed tz setting moves the nightly run: the job follows the setting, not the process TZ.
func TestNightlyFollowsTheTzSetting(t *testing.T) {
	e := newEnv(t, local(23, 3, 59)) // default tz America/New_York: due at 04:10 NY
	e.start(Options{})
	require.NoError(t, e.db.SetSettings(context.Background(), map[string]any{"tz": "Asia/Tokyo"}))

	// 04:11 in New York is 17:11 in Tokyo: not due any more.
	e.clk.Advance(12 * time.Minute)
	e.noNightly(300 * time.Millisecond)

	// 15:09 NY is 04:09 next day in Tokyo, still not due.
	e.clk.Advance(11*time.Hour - 2*time.Minute)
	e.noNightly(300 * time.Millisecond)

	// 15:11 NY is 04:11 Tokyo: the nightly job runs.
	e.clk.Advance(2 * time.Minute)
	require.NoError(t, e.waitJob("optimize").Err)
}
