package store

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"time"
)

// Auto-read (backend-additions-round2 1.13, F5): articles that stay unread longer than N days
// are marked read by the nightly job. "Unread for N days in Kipple" is measured from crawl time,
// which is the item id (unix microseconds). The job never rescans the whole library: it marks the
// items whose id crossed the threshold since the last run, the window
//
//	(lastRun - N days, now - N days]
//
// so every item crosses once, and an article the reader marks unread again afterwards is not
// touched a second time (it is already behind the window). A window is plain unix arithmetic, so
// a DST change moves nothing. The explicit "catch up" (RunAutoRead with a zero Since) has no lower
// bound: it marks every unread item older than N days, and is only ever started by the user
// through POST /api/library/auto-read/run, after a preview.

// Auto-read settings and keys.
const (
	// SettingAutoReadDays is library.auto_read_days: 0 = off (default), else 1 to 365. A feed's
	// own feeds.auto_read_days overrides it (NULL inherits, 0 = off for that feed).
	SettingAutoReadDays = "library.auto_read_days"
	// settingAutoReadLastRun is the unix time the nightly job last ran, written even when nothing
	// is enabled, so turning the feature on later never reaches back before that instant.
	settingAutoReadLastRun = "sys.auto_read_last_run"
	// AutoReadBatch is the ids marked per write transaction, behind the commit gate.
	AutoReadBatch = 500
	// autoReadDay is a day in seconds.
	autoReadDay = 86400
)

// AutoReadFeed is one feed's line of a preview.
type AutoReadFeed struct {
	FeedID int64  `json:"feed_id,string"`
	Title  string `json:"title"`
	Days   int    `json:"days"`
	Count  int    `json:"count"`
}

// AutoReadPreview is what a catch-up run would mark read: only feeds with something to mark,
// biggest first, and the total.
type AutoReadPreview struct {
	Total int            `json:"total"`
	Feeds []AutoReadFeed `json:"feeds"`
}

type autoReadTarget struct {
	id    int64
	title string
	days  int
}

// GlobalAutoReadDays is library.auto_read_days (0 = off).
func (d *DB) GlobalAutoReadDays(ctx context.Context) int {
	return settingInt(ctx, d.reader, SettingAutoReadDays, 0)
}

// autoReadTargets lists the feeds with auto-read on and their effective days. feedID != 0 limits it
// to that feed. days, when not nil, replaces the effective value of what it names: of the one feed
// when feedID != 0, else of the global default (feeds with their own value keep it), so a preview can
// answer "what if I set this?" before anything is saved.
func (d *DB) autoReadTargets(ctx context.Context, feedID int64, days *int) ([]autoReadTarget, error) {
	global := d.GlobalAutoReadDays(ctx)
	if days != nil && feedID == 0 {
		global = *days
	}
	q := `SELECT f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.auto_read_days
		FROM feeds f WHERE f.disabled_reason IS NOT 'archive'`
	var args []any
	if feedID != 0 {
		q += " AND f.id = ?"
		args = append(args, feedID)
	}
	rows, err := d.reader.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []autoReadTarget
	for rows.Next() {
		var t autoReadTarget
		var own sql.NullInt64
		if err := rows.Scan(&t.id, &t.title, &own); err != nil {
			return nil, err
		}
		t.days = global
		if own.Valid {
			t.days = int(own.Int64)
		}
		if days != nil && feedID != 0 {
			t.days = *days
		}
		if t.days > 0 {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// autoReadBounds is the id window of a feed with n days: ids above lo and up to hi. since is the
// lower instant (zero = no lower bound, the catch-up).
func autoReadBounds(now, since time.Time, n int) (lo, hi int64) {
	hi = (now.Unix() - int64(n)*autoReadDay) * 1_000_000
	if !since.IsZero() {
		lo = (since.Unix() - int64(n)*autoReadDay) * 1_000_000
	}
	return lo, hi
}

// The candidate condition: unread, not starred, not muted and not held by a restore. The literal
// `read = 0` keeps idx_items_unread_feed usable.
const autoReadWhere = `feed_id = ?1 AND read = 0 AND starred = 0 AND muted_by IS NULL
	AND (retain_until IS NULL OR retain_until <= ?2) AND id > ?3 AND id <= ?4`

// PreviewAutoRead counts, per feed, the items a catch-up run would mark read at now (see
// RunAutoRead). feedID and days are as in autoReadTargets. It writes nothing.
func (d *DB) PreviewAutoRead(ctx context.Context, now time.Time, feedID int64, days *int) (AutoReadPreview, error) {
	out := AutoReadPreview{Feeds: []AutoReadFeed{}}
	targets, err := d.autoReadTargets(ctx, feedID, days)
	if err != nil {
		return out, err
	}
	for _, t := range targets {
		lo, hi := autoReadBounds(now, time.Time{}, t.days)
		var n int
		if err := d.reader.QueryRowContext(ctx, "SELECT count(*) FROM items WHERE "+autoReadWhere, t.id, now.Unix(), lo, hi).Scan(&n); err != nil {
			return out, err
		}
		if n > 0 {
			out.Feeds = append(out.Feeds, AutoReadFeed{FeedID: t.id, Title: t.title, Days: t.days, Count: n})
			out.Total += n
		}
	}
	sort.SliceStable(out.Feeds, func(i, j int) bool { return out.Feeds[i].Count > out.Feeds[j].Count })
	return out, nil
}

// AutoReadOptions configures RunAutoRead.
type AutoReadOptions struct {
	Now time.Time
	// Since is the lower instant of the window (the last run). Zero is the catch-up: no lower bound.
	Since  time.Time
	FeedID int64 // 0 = every feed with auto-read on
	Days   *int  // see autoReadTargets; nil = the stored settings
	Batch  int   // ids per transaction; default AutoReadBatch
	Pause  time.Duration
	// OnBatch is called after each committed batch of items with the ids that flipped (no lock held).
	OnBatch func(StateResult)
	// Progress is called after each batch with the items marked so far.
	Progress func(done int)
}

// AutoReadResult is the outcome of a run.
type AutoReadResult struct {
	Items   int // items marked read
	Ledger  int // trimmed-ledger rows marked read (not items; Reader clients count them as unread ids)
	Feeds   int // feeds that had something marked
	Batches int
}

// RunAutoRead marks read the qualifying items of every enabled feed (in id order, o.Batch per
// gated write transaction, no stats), and the matching rows of the trimmed ledger. Each batch is one
// UPDATE that selects its own rows inside the write transaction, so an item starred, muted or
// marked read between batches is simply not selected. It stops at the first error or when ctx ends.
func (d *DB) RunAutoRead(ctx context.Context, o AutoReadOptions) (AutoReadResult, error) {
	var res AutoReadResult
	if o.Batch <= 0 {
		o.Batch = AutoReadBatch
	}
	targets, err := d.autoReadTargets(ctx, o.FeedID, o.Days)
	if err != nil {
		return res, err
	}
	pause := func() error {
		if o.Pause <= 0 {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Pause):
			return nil
		}
	}
	for _, t := range targets {
		lo, hi := autoReadBounds(o.Now, o.Since, t.days)
		if hi <= lo {
			continue
		}
		marked := 0
		for {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			var changed []int64
			_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
				rows, err := tx.QueryContext(ctx, `UPDATE items SET read = 1, read_at = ?5 WHERE id IN (
					SELECT id FROM items WHERE `+autoReadWhere+` ORDER BY id LIMIT ?6) RETURNING id, feed_id`,
					t.id, o.Now.Unix(), lo, hi, o.Now.Unix(), o.Batch)
				if err != nil {
					return 0, err
				}
				if changed, err = scanIDs(rows); err != nil {
					return 0, err
				}
				return int64(len(changed)), nil
			})
			if err != nil {
				return res, err
			}
			res.Batches++
			res.Items += len(changed)
			marked += len(changed)
			if len(changed) > 0 {
				if o.OnBatch != nil {
					o.OnBatch(StateResult{Changed: changed})
				}
				if o.Progress != nil {
					o.Progress(res.Items)
				}
			}
			if len(changed) < o.Batch {
				break
			}
			if err := pause(); err != nil {
				return res, err
			}
		}
		// The ledger: rows of items trimmed earlier. Read state only, so they are not events.
		for {
			if err := ctx.Err(); err != nil {
				return res, err
			}
			n, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
				r, err := tx.ExecContext(ctx, `UPDATE trimmed_items SET read = 1 WHERE id IN (
					SELECT id FROM trimmed_items WHERE feed_id = ?1 AND read = 0 AND id > ?2 AND id <= ?3 LIMIT ?4)`,
					t.id, lo, hi, o.Batch)
				if err != nil {
					return 0, err
				}
				return r.RowsAffected()
			})
			if err != nil {
				return res, err
			}
			res.Ledger += int(n)
			marked += int(n)
			if n < int64(o.Batch) {
				break
			}
			if err := pause(); err != nil {
				return res, err
			}
		}
		if marked > 0 {
			res.Feeds++
		}
	}
	return res, nil
}

// AutoReadLastRun is when the nightly auto-read step last ran; ok is false when it never has.
func AutoReadLastRun(ctx context.Context, q Querier) (time.Time, bool) {
	n := settingInt64(ctx, q, settingAutoReadLastRun)
	if n <= 0 {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// RecordAutoReadRun stores the instant the nightly auto-read step ran.
func (d *DB) RecordAutoReadRun(ctx context.Context, now time.Time) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES(?1, ?2, ?3)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			settingAutoReadLastRun, strconv.FormatInt(now.Unix(), 10), now.Unix())
		return err
	})
}

func settingInt64(ctx context.Context, q Querier, key string) int64 {
	var s string
	if err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&s); err != nil {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
