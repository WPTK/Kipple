package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The restore window is one settings read, not the eight of the fetch settings, and it keeps its default and
// its clamp in one place.
func TestRestoreDaysReadsOneSettingWithDefaultAndClamp(t *testing.T) {
	e := newEnv(t)
	cq := &countingQuerier{Querier: e.db.Reader()}
	n, err := restoreDaysErr(e.ctx, cq)
	require.NoError(t, err)
	require.Equal(t, 90, n, "the default")
	require.Equal(t, 1, cq.rows, "one statement")

	set := func(v string) {
		e.exec(`INSERT INTO settings (key, value) VALUES ('retention.restore_days', ?1)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, v)
	}
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"30", 30}, {"0", 0}, {"-5", 0}, {"100000", MaxRestoreDays}, {"12.5", 90}, {`"x"`, 90},
	} {
		set(tc.raw)
		got, err := restoreDaysErr(e.ctx, e.db.Reader())
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
		require.Equal(t, got, restoreDays(e.ctx, e.db.Reader()), tc.raw)
		require.Equal(t, tc.want, LoadFetchSettings(e.ctx, e.db.Reader()).RestoreDays, "the fetch settings agree: %s", tc.raw)
	}
}

// ItemKnown is true for a live item and for a stub inside the restore window, and for nothing else.
func TestItemKnownHonoursTheRestoreWindow(t *testing.T) {
	e := newEnv(t)
	feed := e.addFeed("https://ex.com/a")
	now := int64(1_800_000_000)
	e.exec(`INSERT INTO items (id, feed_id, read, published_at, sort_at, uid, content_hash, text_hash)
		VALUES (1, ?1, 0, 1, 1, 'u1', 'c', 't')`, feed)
	seedLedger(t, e.db, feed, 2, now-10*day, now, true)  // stub inside 90 days
	seedLedger(t, e.db, feed, 3, now-100*day, now, true) // stub outside 90 days
	seedLedger(t, e.db, feed, 4, now-10*day, now, false) // ledger row without a stub

	known := func(id int64) bool {
		ok, err := e.db.ItemKnown(e.ctx, id, now)
		require.NoError(t, err)
		return ok
	}
	require.True(t, known(1))
	require.True(t, known(2))
	require.False(t, known(3))
	require.False(t, known(4))
	require.False(t, known(99))

	e.exec(`INSERT INTO settings (key, value) VALUES ('retention.restore_days', '120')`)
	require.True(t, known(3), "a longer window reaches the older stub")
	e.exec(`UPDATE settings SET value = '5' WHERE key = 'retention.restore_days'`)
	require.False(t, known(2), "a shorter window drops the newer one")
	require.True(t, known(1), "a live item needs no window")
}
