// Package stats records reading events (design §8). Recorder is the only way
// to write stats_events, and it is injected only into the web open handler, the
// web star handler, the stats ingest handler and the Reader API edit-tag
// handler. The mark-read paths never receive one.
package stats

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/WPTK/kipple/internal/store"
)

// Event kinds (the stats_events CHECK is the whitelist).
const (
	KindOpen         = "open"
	KindReadTime     = "read_time"
	KindScroll       = "scroll"
	KindStar         = "star"
	KindUnstar       = "unstar"
	KindOpenOriginal = "open_original"
	KindShare        = "share"
)

var clients = map[string]bool{"web": true, "pwa": true, "reeder": true, "netnewswire": true, "unread": true, "api": true}

// ErrDropped means the event failed validation and was not recorded. It is not
// a database failure: the transaction may continue.
var ErrDropped = errors.New("stats: event dropped")

const (
	sessionWindow = 12 * time.Hour
	maxReadTime   = 3600
)

// Event is one stat to record.
type Event struct {
	Kind       string
	Client     string
	Inferred   bool
	ItemID     int64
	Value      int64 // read_time seconds, scroll percent
	HasValue   bool
	SessionKey string
	EventID    string // client-generated id (see ValidEventID); "" = none. A repeated id is dropped
}

// Recorder writes one event inside the caller's write transaction.
type Recorder interface {
	Record(tx *sql.Tx, ev Event) error
	// RecordMany records a batch of events from one request: it reads stats.enabled and the time
	// zone once, and drops invalid events like Record. Only a database failure is an error.
	RecordMany(tx *sql.Tx, evs []Event) error
	// RecordStars records one star or unstar event per id (kind is KindStar or
	// KindUnstar) for a bulk edit, in one pass; ErrDropped ids are skipped.
	RecordStars(tx *sql.Tx, kind, client string, ids []int64) error
}

// SQL is the Recorder over the store's stats_events table.
type SQL struct {
	now func() time.Time
}

// New returns a Recorder; now defaults to the wall clock.
func New(now func() time.Time) *SQL {
	if now == nil {
		now = time.Now
	}
	return &SQL{now: now}
}

// NewSessionKey returns a fresh random session key for an open event.
func NewSessionKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails on supported platforms
	return hex.EncodeToString(b)
}

// Record validates ev (design §8 rule 4) and appends it. An event that fails
// validation returns ErrDropped and writes nothing.
func (r *SQL) Record(tx *sql.Tx, ev Event) error {
	ctx := context.Background() // tx is already bound to the WithWrite deadline
	if on, err := store.StatsEnabled(ctx, tx); err != nil {
		return err
	} else if !on {
		return ErrDropped
	}
	return r.record(ctx, tx, ev, store.LoadLocation(ctx, tx))
}

// RecordMany records a batch in one transaction, reading stats.enabled and the time zone once.
// Invalid and duplicate events are dropped; only a database failure is an error.
func (r *SQL) RecordMany(tx *sql.Tx, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	ctx := context.Background()
	if on, err := store.StatsEnabled(ctx, tx); err != nil {
		return err
	} else if !on {
		return nil
	}
	loc := store.LoadLocation(ctx, tx)
	for _, ev := range evs {
		if err := r.record(ctx, tx, ev, loc); err != nil && !errors.Is(err, ErrDropped) {
			return err
		}
	}
	return nil
}

// ValidEventID reports whether id is an acceptable client event id: 8 to 64 characters of
// [A-Za-z0-9_-].
func ValidEventID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// record is Record after the enabled check and the time-zone load.
func (r *SQL) record(ctx context.Context, tx *sql.Tx, ev Event, loc *time.Location) error {
	if !clients[ev.Client] {
		return ErrDropped
	}
	if !ValidEventID(ev.EventID) {
		ev.EventID = "" // a malformed id counts as absent
	}
	if ev.EventID != "" {
		// A repeated event id is a retried flush: drop it before anything (the cap included) sees it.
		if seen, err := store.StatEventSeen(ctx, tx, ev.EventID); err != nil {
			return err
		} else if seen {
			return ErrDropped
		}
	}
	now := r.now()
	snap, ok, err := store.StatItemSnapshot(ctx, tx, ev.ItemID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDropped
	}
	var value sql.NullInt64
	switch ev.Kind {
	case KindOpen:
		if ev.SessionKey == "" {
			return ErrDropped
		}
	case KindStar, KindUnstar, KindOpenOriginal, KindShare:
		ev.SessionKey = "" // session keys only belong to open, read_time and scroll
	case KindReadTime, KindScroll:
		if ev.SessionKey == "" || !ev.HasValue {
			return ErrDropped
		}
		openTS, ok, err := store.StatOpenTS(ctx, tx, ev.SessionKey, ev.ItemID, now.Add(-sessionWindow).Unix())
		if err != nil {
			return err
		}
		if !ok {
			return ErrDropped
		}
		if ev.Kind == KindReadTime {
			if ev.Value < 1 || ev.Value > 60 {
				return ErrDropped
			}
			sum, err := store.StatReadTimeSum(ctx, tx, ev.SessionKey)
			if err != nil {
				return err
			}
			if total := sum + ev.Value; total > now.Unix()-openTS+5 || total > maxReadTime {
				return ErrDropped
			}
		} else {
			ev.Value = min(max(ev.Value, 0), 100)
			existed, err := store.StatSetScroll(ctx, tx, ev.SessionKey, ev.Value)
			if err != nil || existed {
				return err
			}
		}
		value = sql.NullInt64{Int64: ev.Value, Valid: true}
	default:
		return ErrDropped
	}
	lt := now.In(loc)
	return store.InsertStat(ctx, tx, store.StatRow{
		TS: now.Unix(), LocalDate: lt.Format("2006-01-02"), LocalHour: lt.Hour(), LocalWeekday: int(lt.Weekday()),
		Kind: ev.Kind, Client: ev.Client, Inferred: ev.Inferred, ItemID: ev.ItemID,
		FeedID: snap.FeedID, FeedTitle: snap.FeedTitle, FolderID: snap.FolderID, FolderName: snap.FolderName,
		ItemTitle: snap.ItemTitle, ItemURL: snap.ItemURL, Value: value, SessionKey: ev.SessionKey, EventID: ev.EventID,
	})
}

// RecordStars records one star or unstar event per id in a single transaction.
// It loads the time zone once and looks each feed up once, so a 10k-id bulk
// edit stays inside the write deadline. ErrDropped ids are skipped.
func (r *SQL) RecordStars(tx *sql.Tx, kind, client string, ids []int64) error {
	if !clients[client] || (kind != KindStar && kind != KindUnstar) {
		return nil
	}
	ctx := context.Background()
	if on, err := store.StatsEnabled(ctx, tx); err != nil {
		return err
	} else if !on {
		return nil
	}
	now := r.now()
	lt := now.In(store.LoadLocation(ctx, tx))
	feeds := map[int64]store.StatSnapshot{}
	for _, id := range ids {
		feedID, title, url, ok, err := store.StatItemBasics(ctx, tx, id)
		if err != nil {
			return err
		}
		var snap store.StatSnapshot
		if ok {
			var cached bool
			if snap, cached = feeds[feedID]; !cached {
				if snap, err = store.StatFeedSnapshot(ctx, tx, feedID); err != nil {
					return err
				}
				feeds[feedID] = snap
			}
			snap.ItemTitle, snap.ItemURL = title, url
		} else if snap, ok, err = store.StatItemSnapshot(ctx, tx, id); err != nil {
			return err
		} else if !ok {
			continue
		}
		if err := store.InsertStat(ctx, tx, store.StatRow{
			TS: now.Unix(), LocalDate: lt.Format("2006-01-02"), LocalHour: lt.Hour(), LocalWeekday: int(lt.Weekday()),
			Kind: kind, Client: client, ItemID: id,
			FeedID: snap.FeedID, FeedTitle: snap.FeedTitle, FolderID: snap.FolderID, FolderName: snap.FolderName,
			ItemTitle: snap.ItemTitle, ItemURL: snap.ItemURL,
		}); err != nil {
			return err
		}
	}
	return nil
}
