package stats

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/clock"
	"github.com/WPTK/kipple/internal/store"
)

const itemID = int64(1790251200) * 1_000_000

type rig struct {
	t   *testing.T
	db  *store.DB
	clk *clock.Fake
	rec *SQL
}

func newRig(t *testing.T) *rig {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 1, 15, 3, 30, 0, 0, time.UTC)) // 22:30 the day before in New York
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "k.db"), Clock: clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO feeds (id, folder_id, url, url_key, host, title, next_fetch_at)
			VALUES (1, 1, 'https://a.example/f', 'a.example/f', 'a.example', 'Feed A', 0)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, url, title)
			VALUES (?, 1, 1, 1, 'g:1', 'c', 't', 'https://a.example/1', 'Hello')`, itemID)
		return err
	}))
	return &rig{t: t, db: db, clk: clk, rec: New(clk.Now)}
}

// record runs one event in its own write transaction and returns Record's error.
func (r *rig) record(ev Event) error {
	r.t.Helper()
	var rerr error
	require.NoError(r.t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		rerr = r.rec.Record(tx, ev)
		if rerr == ErrDropped {
			return nil
		}
		return rerr
	}))
	return rerr
}

func (r *rig) count(where string, args ...any) int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.db.Reader().QueryRow("SELECT count(*) FROM stats_events WHERE "+where, args...).Scan(&n))
	return n
}

func TestRecordSnapshotAndLocalTime(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.db.SetSettings(context.Background(), map[string]any{"tz": "America/New_York"}))
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk1"}))
	var date, feed, title, folder string
	var hour, wd int
	require.NoError(t, r.db.Reader().QueryRow(`SELECT local_date, local_hour, local_weekday, feed_title, item_title, folder_name
		FROM stats_events`).Scan(&date, &hour, &wd, &feed, &title, &folder))
	require.Equal(t, "2026-01-14", date, "local date is settings.tz (America/New_York), not UTC")
	require.Equal(t, 22, hour)
	require.Equal(t, 3, wd) // Wednesday
	require.Equal(t, "Feed A", feed)
	require.Equal(t, "Hello", title)
	require.Equal(t, "Uncategorized", folder)
}

func TestRecordValidation(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk1"}))
	r.clk.Advance(30 * time.Second)

	tests := []struct {
		name string
		ev   Event
		want int // rows of that kind afterwards
		kind string
		err  error
	}{
		{"read_time ok", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 20, HasValue: true}, 1, "read_time", nil},
		{"read_time zero", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 0, HasValue: true}, 1, "read_time", ErrDropped},
		{"read_time over 60", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 61, HasValue: true}, 1, "read_time", ErrDropped},
		{"read_time exceeds elapsed+5", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 20, HasValue: true}, 1, "read_time", ErrDropped},
		{"read_time within elapsed+5", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 15, HasValue: true}, 2, "read_time", nil},
		{"unknown session", Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "nope", Value: 1, HasValue: true}, 2, "read_time", ErrDropped},
		{"session of another item", Event{Kind: KindScroll, Client: "web", ItemID: itemID + 1, SessionKey: "sk1", Value: 5, HasValue: true}, 0, "scroll", ErrDropped},
		{"scroll clamps", Event{Kind: KindScroll, Client: "web", ItemID: itemID, SessionKey: "sk1", Value: 250, HasValue: true}, 1, "scroll", nil},
		{"unknown item", Event{Kind: KindShare, Client: "web", ItemID: 42}, 0, "share", ErrDropped},
		{"share ok", Event{Kind: KindShare, Client: "web", ItemID: itemID}, 1, "share", nil},
		{"bad kind", Event{Kind: "read", Client: "web", ItemID: itemID}, 0, "read", ErrDropped},
		{"bad client", Event{Kind: KindStar, Client: "bogus", ItemID: itemID}, 0, "star", ErrDropped},
		{"open without key", Event{Kind: KindOpen, Client: "web", ItemID: itemID}, 1, "open", ErrDropped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.err, r.record(tc.ev))
			require.Equal(t, tc.want, r.count("kind = ?", tc.kind))
		})
	}
}

func TestScrollKeepsMaxPerSession(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	for _, v := range []int64{40, 20, 70, 60} {
		require.NoError(t, r.record(Event{Kind: KindScroll, Client: "web", ItemID: itemID, SessionKey: "sk", Value: v, HasValue: true}))
	}
	require.Equal(t, 1, r.count("kind = 'scroll'"))
	require.Equal(t, 1, r.count("kind = 'scroll' AND value = 70"))
}

func TestSessionExpiresAfter12Hours(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	r.clk.Advance(13 * time.Hour)
	require.Equal(t, ErrDropped, r.record(Event{Kind: KindScroll, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 5, HasValue: true}))
}

func TestReadTimeCappedAt3600(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	r.clk.Advance(5 * time.Hour)
	for i := 0; i < 60; i++ { // 60 x 60 s = 3600 s
		require.NoError(t, r.record(Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 60, HasValue: true}))
	}
	require.Equal(t, ErrDropped, r.record(Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 1, HasValue: true}))
}

func TestLedgerItemsRecordWithStubSnapshot(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (77, 1, 'g:77', 0, 1, 1)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash, url, title, author, content_html, content_text)
			VALUES (77, 1, 1, 0, 'c', 't', 'https://a.example/77', 'Old one', '', '', '')`)
		return err
	}))
	require.NoError(t, r.record(Event{Kind: KindOpenOriginal, Client: "pwa", ItemID: 77}))
	var title string
	require.NoError(t, r.db.Reader().QueryRow("SELECT item_title FROM stats_events WHERE item_id = 77").Scan(&title))
	require.Equal(t, "Old one", title)
}

func (r *rig) setSetting(k string, v any) {
	r.t.Helper()
	require.NoError(r.t, r.db.SetSettings(context.Background(), map[string]any{k: v}))
}

func TestStatsDisabledRecordsNothing(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	r.clk.Advance(30 * time.Second)
	r.setSetting("stats.enabled", false)
	for _, ev := range []Event{
		{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk2"},
		{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 10, HasValue: true},
		{Kind: KindScroll, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 10, HasValue: true},
		{Kind: KindStar, Client: "web", ItemID: itemID},
		{Kind: KindUnstar, Client: "web", ItemID: itemID},
		{Kind: KindShare, Client: "web", ItemID: itemID},
		{Kind: KindOpenOriginal, Client: "web", ItemID: itemID},
	} {
		require.Equal(t, ErrDropped, r.record(ev), ev.Kind)
	}
	require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return r.rec.RecordStars(tx, KindStar, "reeder", []int64{itemID})
	}))
	require.Equal(t, 1, r.count("1 = 1"), "only the row from before the switch")

	// Back on: recording resumes, and the old session is still valid.
	r.setSetting("stats.enabled", nil)
	require.NoError(t, r.record(Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk", Value: 10, HasValue: true}))
	require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return r.rec.RecordStars(tx, KindStar, "reeder", []int64{itemID})
	}))
	require.Equal(t, 3, r.count("1 = 1"))
}

func TestEventIDDedup(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	r.clk.Advance(30 * time.Second)
	mk := func(kind, id string, v int64) Event {
		return Event{Kind: kind, Client: "web", ItemID: itemID, SessionKey: "sk", Value: v, HasValue: true, EventID: id}
	}
	require.NoError(t, r.record(mk(KindReadTime, "evt-read-1", 10)))
	require.Equal(t, ErrDropped, r.record(mk(KindReadTime, "evt-read-1", 10)), "same id")
	require.Equal(t, ErrDropped, r.record(mk(KindReadTime, "evt-read-1", 5)), "same id, different value")
	// A duplicate must not consume the cumulative cap: 10 + 15 = 25 <= 30 + 5 still fits.
	require.NoError(t, r.record(mk(KindReadTime, "evt-read-2", 15)))
	require.Equal(t, 2, r.count("kind = 'read_time'"))
	// Ids are global across kinds and sessions.
	require.Equal(t, ErrDropped, r.record(Event{Kind: KindShare, Client: "web", ItemID: itemID, EventID: "evt-read-1"}))
	for _, k := range []string{KindShare, KindOpenOriginal} {
		id := "evt-" + k + "-1"
		require.NoError(t, r.record(Event{Kind: k, Client: "web", ItemID: itemID, EventID: id}))
		require.Equal(t, ErrDropped, r.record(Event{Kind: k, Client: "web", ItemID: itemID, EventID: id}), k)
		require.Equal(t, 1, r.count("kind = ?", k))
	}
	require.NoError(t, r.record(mk(KindScroll, "evt-scroll-1", 20)))
	require.Equal(t, ErrDropped, r.record(mk(KindScroll, "evt-scroll-1", 90)))
	require.Equal(t, 1, r.count("kind = 'scroll' AND value = 20 AND event_id = 'evt-scroll-1'"))
	// No id: works as before, any number may coexist.
	require.NoError(t, r.record(Event{Kind: KindShare, Client: "web", ItemID: itemID}))
	require.NoError(t, r.record(Event{Kind: KindShare, Client: "web", ItemID: itemID}))
	// A malformed id counts as absent: stored NULL, and repeats land.
	for i := 0; i < 2; i++ {
		require.NoError(t, r.record(Event{Kind: KindShare, Client: "web", ItemID: itemID, EventID: "short"}))
		require.NoError(t, r.record(Event{Kind: KindShare, Client: "web", ItemID: itemID, EventID: "has space in it"}))
	}
	require.Equal(t, 0, r.count("event_id IN ('short', 'has space in it')"))
	require.Equal(t, 1+6, r.count("kind = 'share' AND event_id IS NULL")+r.count("kind = 'share' AND event_id IS NOT NULL"))
	// The unique index is the backstop: a raced duplicate insert is skipped, never an error.
	require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		row := store.StatRow{TS: 1, LocalDate: "2026-01-01", Kind: KindShare, Client: "web", ItemID: itemID, FeedID: 1, FeedTitle: "F", EventID: "evt-race-01"}
		if err := store.InsertStat(ctx, tx, row); err != nil {
			return err
		}
		return store.InsertStat(ctx, tx, row)
	}))
	require.Equal(t, 1, r.count("event_id = 'evt-race-01'"))
}

func TestRecordManyBatch(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "sk"}))
	r.clk.Advance(30 * time.Second)
	many := func(evs ...Event) {
		require.NoError(t, r.db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			return r.rec.RecordMany(tx, evs)
		}))
	}
	rt := func(id string, v int64) Event {
		return Event{Kind: KindReadTime, Client: "web", ItemID: itemID, SessionKey: "sk", Value: v, HasValue: true, EventID: id}
	}
	share := Event{Kind: KindShare, Client: "web", ItemID: itemID, EventID: "evt-share-1"}
	// In-batch duplicates of every kind, an invalid event and a bad kind in the middle: all fine.
	many(rt("evt-rt-001", 10), rt("evt-rt-001", 10), Event{Kind: "bogus", Client: "web", ItemID: itemID},
		rt("evt-rt-002", 15), share, share, Event{Kind: KindShare, Client: "web", ItemID: 42})
	require.Equal(t, 2, r.count("kind = 'read_time'"))
	require.Equal(t, 1, r.count("kind = 'share'"))
	many() // empty batch is a no-op

	r.setSetting("stats.enabled", false)
	many(rt("evt-rt-003", 1), Event{Kind: KindShare, Client: "web", ItemID: itemID})
	require.Equal(t, 2, r.count("kind = 'read_time'"))
	require.Equal(t, 1, r.count("kind = 'share'"))
}

// A new install records in UTC (the default); a tz change applies to the next row
// without a restart and leaves every earlier row exactly as it was; a set TZ
// environment variable wins over the setting.
func TestZoneResolverDrivesNewRowsOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	rows := func() string {
		var s string
		require.NoError(t, r.db.Reader().QueryRow(`SELECT group_concat(id || '|' || ts || '|' || local_date || '|' || local_hour || '|' || local_weekday, ';')
			FROM (SELECT * FROM stats_events ORDER BY id)`).Scan(&s))
		return s
	}
	lastDate := func() (string, int) {
		var d string
		var h int
		require.NoError(t, r.db.Reader().QueryRow(`SELECT local_date, local_hour FROM stats_events ORDER BY id DESC LIMIT 1`).Scan(&d, &h))
		return d, h
	}
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "a"}))
	d, h := lastDate()
	require.Equal(t, "2026-01-15", d, "UTC by default")
	require.Equal(t, 3, h)
	before := rows()

	require.NoError(t, r.db.SetSettings(ctx, map[string]any{"tz": "America/New_York"}))
	require.NoError(t, r.record(Event{Kind: KindOpen, Client: "web", ItemID: itemID, SessionKey: "b"}))
	d, h = lastDate()
	require.Equal(t, "2026-01-14", d, "the new zone applies to the next row at once")
	require.Equal(t, 22, h)
	require.True(t, len(rows()) > len(before) && rows()[:len(before)] == before, "earlier rows are untouched")
}
