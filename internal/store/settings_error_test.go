package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

// breakSettings makes every settings read fail with a real SQL error (no such
// table), the stand-in for a transient I/O failure inside a transaction. It
// returns the repair.
func breakSettings(e *env) (repair func()) {
	e.exec(`ALTER TABLE settings RENAME TO settings_away`)
	return func() { e.exec(`ALTER TABLE settings_away RENAME TO settings`) }
}

func TestLoadFetchSettingsReadFailure(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO settings(key, value) VALUES ('retention.default', '100')`)

	dead, cancel := context.WithCancel(e.ctx)
	cancel()
	set, err := LoadFetchSettingsErr(dead, e.db.reader)
	require.Error(t, err, "a cancelled context is a read failure, not 'no such setting'")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 250, set.RetentionDefault, "defaults fill in for what could not be read")

	// The lenient loader logs and uses defaults; it must not panic or hang.
	require.Equal(t, 250, LoadFetchSettings(dead, e.db.reader).RetentionDefault)
	// ...and the same reader reads the real value once the context is healthy.
	set, err = LoadFetchSettingsErr(e.ctx, e.db.reader)
	require.NoError(t, err)
	require.Equal(t, 100, set.RetentionDefault)

	// A missing row and an unparseable value stay non-errors.
	e.exec(`UPDATE settings SET value = '"junk"' WHERE key = 'retention.default'`)
	set, err = LoadFetchSettingsErr(e.ctx, e.db.reader)
	require.NoError(t, err)
	require.Equal(t, 250, set.RetentionDefault)
}

func TestTransactionalSettingsReadFailureFailsTheTransaction(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(250)...))
	require.Equal(t, 250, e.count(`SELECT count(*) FROM items`))
	e.exec(`INSERT INTO settings(key, value) VALUES ('retention.default', '100') ON CONFLICT(key) DO UPDATE SET value = excluded.value`)

	repair := breakSettings(e)
	_, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerScheduled)
	require.Error(t, err, "retention must not run with a default it could not check")
	repair()
	require.Equal(t, 250, e.count(`SELECT count(*) FROM items`), "nothing trimmed")
	require.Equal(t, 0, e.count(`SELECT count(*) FROM trimmed_items`))
	require.Equal(t, 0, e.count(`SELECT count(*) FROM fetch_log WHERE outcome = 'trim_only'`), "the failed batch rolled back its log row")

	n, err := e.db.TrimOnly(e.ctx, id, fetch.TriggerScheduled)
	require.NoError(t, err)
	require.EqualValues(t, 150, n, "the retry, with settings readable, trims to the real cap")
}

func TestPurgesFailOnSettingsReadFailure(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	e.fetchBody(id, rss(numbered(300)...)) // 50 trimmed into the ledger with stubs
	require.Equal(t, 50, e.count(`SELECT count(*) FROM trimmed_content`))
	// Age the ledger far past every window, so a purge that ran on defaults would delete.
	e.exec(`UPDATE trimmed_items SET trimmed_at = 1, last_seen_at = 1`)
	now := e.clk.Now().Unix()

	repair := breakSettings(e)
	_, err := e.db.PurgeStubs(e.ctx, now, 1000)
	require.Error(t, err)
	_, err = e.db.PurgeLedger(e.ctx, now, 1000)
	require.Error(t, err)
	err = e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := restoreTrimmed(ctx, tx, []int64{1}, "star", now)
		return err
	})
	require.Error(t, err)
	repair()
	require.Equal(t, 50, e.count(`SELECT count(*) FROM trimmed_content`), "no stub purged")
	require.Equal(t, 50, e.count(`SELECT count(*) FROM trimmed_items`), "no ledger row purged")

	n, err := e.db.PurgeStubs(e.ctx, now, 1000)
	require.NoError(t, err)
	require.EqualValues(t, 50, n)
	n, err = e.db.PurgeLedger(e.ctx, now, 1000)
	require.NoError(t, err)
	require.EqualValues(t, 50, n)
}

func TestPullInScheduleSettingsReadFailure(t *testing.T) {
	e := newEnv(t)
	repair := breakSettings(e)
	_, err := e.db.PullInSchedule(e.ctx, 10)
	require.Error(t, err)
	repair()
	_, err = e.db.PullInSchedule(e.ctx, 10)
	require.NoError(t, err)
}

func TestJSONTextReportsMarshalErrors(t *testing.T) {
	_, err := jsonText(make(chan int))
	require.Error(t, err)
	_, err = idsJSON(nil)
	require.NoError(t, err)
	_, err = optJSON(make(chan int), false)
	require.Error(t, err)
	v, err := optJSON(nil, true)
	require.NoError(t, err)
	require.Nil(t, v)
	v, err = optJSON([]string{"a"}, false)
	require.NoError(t, err)
	require.Equal(t, `["a"]`, v)
	v, err = categoriesJSON(nil)
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestHoldPendingEncoding(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, "[]", e.db.HoldPending())
	e.db.ftPend.mu.Lock()
	e.db.ftPend.ids = map[int64]struct{}{7: {}}
	e.db.ftPend.mu.Unlock()
	require.Equal(t, "[7]", e.db.HoldPending())
	e.db.ftPend.mu.Lock()
	e.db.ftPend.ids[42] = struct{}{}
	e.db.ftPend.mu.Unlock()
	require.Equal(t, "[7,42]", e.db.HoldPending())
}

// The bookkeeping commits and TrimOnly go through the commit gate: they wait for
// it (bounded by ctx), hold it for their transaction and release it after.
func TestGatedWritesWaitForTheGate(t *testing.T) {
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	snap := e.snap(id)
	res := &fetch.Result{Snap: snap, StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError, ErrMsg: "boom", ErrClass: "transient",
		NextFetchAt: e.clk.Now().Add(time.Hour), CurrentDelayS: 3600}
	ops := map[string]func(ctx context.Context) error{
		"CommitFetchError": func(ctx context.Context) error { return e.db.CommitFetchError(ctx, res) },
		"CommitSkip":       func(ctx context.Context) error { return e.db.CommitSkip(ctx, snap, "note") },
		"TrimOnly": func(ctx context.Context) error {
			_, err := e.db.TrimOnly(ctx, id, fetch.TriggerScheduled)
			return err
		},
	}
	for name, op := range ops {
		release, err := e.db.AcquireGate(e.ctx)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(e.ctx, 100*time.Millisecond)
		require.ErrorIs(t, op(ctx), context.DeadlineExceeded, name)
		cancel()
		release()

		require.NoError(t, op(e.ctx), name)
		// the gate was released: it can be taken again at once
		ctx, cancel = context.WithTimeout(e.ctx, 5*time.Second)
		r2, err := e.db.AcquireGate(ctx)
		cancel()
		require.NoError(t, err, name)
		r2()
	}
}

// KeyAndNormalize must agree with the pair of calls it replaced (Normalize, then
// Key of the normalized form).
func TestKeyAndNormalizeMatchesSeparateCalls(t *testing.T) {
	for _, raw := range []string{
		"http://Example.COM:80/feed.xml", "https://example.com:443/a%20b?x=1&y=2#frag", "  http://[::1]:8080/f  ",
		"https://ex.com/a/../b/./c?q=%7E", "http://ex.com", "http://ex.com:8080/é?ü=1",
	} {
		norm, err := feedurl.Normalize(raw)
		require.NoError(t, err, raw)
		wantKey, err := feedurl.Key(norm)
		require.NoError(t, err, raw)
		key, gotNorm, err := feedurl.KeyAndNormalize(raw)
		require.NoError(t, err, raw)
		require.Equal(t, norm, gotNorm, raw)
		require.Equal(t, wantKey, key, raw)
	}
	_, _, err := feedurl.KeyAndNormalize("ftp://ex.com/x")
	require.Error(t, err)
}
