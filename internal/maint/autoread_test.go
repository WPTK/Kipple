package maint

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func (e *env) crawled(at time.Time) int64 {
	e.t.Helper()
	id := at.UnixMicro()
	e.exec(`INSERT INTO items (id, feed_id, uid, published_at, sort_at, content_hash, text_hash, url, title, author)
		VALUES (?1, ?2, 'u' || ?1, 1, 1, 'c', 't', 'https://a/x', 't', '')`, id, e.feed)
	return id
}

func (e *env) isRead(id int64) bool { return e.count("SELECT read FROM items WHERE id = ?", id) == 1 }

// The nightly auto-read step over three nights that straddle the fall-back DST change of 2026-11-01
// (the middle night is 25 hours after the first): the first night only records itself, the next
// marks what crossed 10 days in between, and a manual mark-unread sticks on the night after.
func TestNightlyAutoReadWindowAcrossDSTAndManualUnreadSticks(t *testing.T) {
	e := newEnv(t, time.Date(2026, 10, 31, 4, 0, 0, 0, newYork))
	e.exec("UPDATE feeds SET auto_read_days = 10 WHERE id = ?", e.feed)
	r1 := time.Date(2026, 10, 31, 4, 10, 0, 0, newYork)
	r2 := time.Date(2026, 11, 1, 4, 10, 0, 0, newYork)
	require.Equal(t, 25*time.Hour, r2.Sub(r1), "the DST night is 25 hours long")
	r3 := r2.Add(24 * time.Hour)
	ten := 10 * 24 * time.Hour
	before := e.crawled(r1.Add(-ten - 5*time.Hour)) // crossed before the first run: never marked
	x := e.crawled(r1.Add(-ten + 12*time.Hour))     // crosses between run 1 and run 2
	z := e.crawled(r2.Add(-ten + time.Hour))        // crosses between run 2 and run 3
	young := e.crawled(r3.Add(-2 * 24 * time.Hour)) // too young throughout

	var mu sync.Mutex
	var published [][]int64
	e.start(Options{OnAutoRead: func(r store.StateResult) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, r.Changed)
	}})

	e.clk.Advance(11 * time.Minute) // night 1, 04:11 EDT
	j := e.waitJob("auto_read")
	require.NoError(t, j.Err)
	require.Zero(t, j.Rows, "no recorded run: an empty window, nothing marked behind the reader's back")
	require.False(t, e.isRead(before) || e.isRead(x) || e.isRead(z))
	last, ok := store.AutoReadLastRun(context.Background(), e.db.Reader())
	require.True(t, ok)
	require.Equal(t, r1.Add(time.Minute).Unix(), last.Unix())

	e.clk.Advance(r2.Add(time.Minute).Sub(e.clk.Now())) // night 2
	j = e.waitJob("auto_read")
	require.NoError(t, j.Err)
	require.EqualValues(t, 1, j.Rows)
	require.True(t, e.isRead(x))
	require.False(t, e.isRead(before) || e.isRead(z) || e.isRead(young))
	mu.Lock()
	require.Equal(t, [][]int64{{x}}, published, "one items.state batch for the ids that flipped")
	mu.Unlock()

	// The reader marks x unread again; the next night's window has moved past it.
	e.exec("UPDATE items SET read = 0, read_at = NULL WHERE id = ?", x)
	e.clk.Advance(r3.Add(time.Minute).Sub(e.clk.Now())) // night 3
	j = e.waitJob("auto_read")
	require.NoError(t, j.Err)
	require.EqualValues(t, 1, j.Rows)
	require.True(t, e.isRead(z))
	require.False(t, e.isRead(x), "a manual mark-unread sticks")
	require.False(t, e.isRead(before) || e.isRead(young))
}

// A server that was down for several nights catches up on the next run: the window starts at the
// last completed run, so everything that crossed since is marked, and nothing older.
func TestNightlyAutoReadCatchesUpAfterDowntime(t *testing.T) {
	e := newEnv(t, time.Date(2026, 9, 20, 12, 0, 0, 0, newYork))
	require.NoError(t, e.db.SetSettings(context.Background(), map[string]any{store.SettingAutoReadDays: 7}))
	lastRun := time.Date(2026, 9, 20, 4, 10, 0, 0, newYork)
	require.NoError(t, e.db.RecordAutoReadRun(context.Background(), lastRun))
	now := time.Date(2026, 9, 24, 4, 11, 0, 0, newYork) // four nights later
	week := 7 * 24 * time.Hour
	older := e.crawled(lastRun.Add(-week - time.Hour))
	crossed := []int64{
		e.crawled(lastRun.Add(-week + time.Hour)),
		e.crawled(now.Add(-week - 30*time.Hour)),
		e.crawled(now.Add(-week - time.Hour)),
	}
	require.NoError(t, e.db.RecordNightlyDate(context.Background(), "2026-09-20", lastRun.Unix())) // a nightly is due
	e.clk.Advance(now.Sub(e.clk.Now()))
	e.start(Options{CatchUpDelay: -1})
	e.clk.Advance(time.Minute)
	j := e.waitJob("auto_read")
	require.NoError(t, j.Err)
	require.EqualValues(t, 3, j.Rows)
	for _, id := range crossed {
		require.True(t, e.isRead(id))
	}
	require.False(t, e.isRead(older))
}

func TestNightlyAutoReadIsOffByDefault(t *testing.T) {
	e := newEnv(t, local(23, 4, 0))
	old := e.crawled(local(1, 0, 0))
	require.NoError(t, e.db.RecordAutoReadRun(context.Background(), local(22, 4, 10)))
	e.start(Options{})
	e.clk.Advance(11 * time.Minute)
	j := e.waitJob("auto_read")
	require.NoError(t, j.Err)
	require.Zero(t, j.Rows)
	require.False(t, e.isRead(old))
}
