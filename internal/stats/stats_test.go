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
