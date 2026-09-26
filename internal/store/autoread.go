package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
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
	// settingAutoReadFeedMarks is {"<feed id>": unix time}: each feed's last completed nightly
	// window end. It exists only while a run is unfinished (a run that completes clears it and
	// advances settingAutoReadLastRun), so a rerun repeats no window a feed already finished.
	settingAutoReadFeedMarks = "sys.auto_read_feed_marks"
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

// GlobalAutoReadDays is library.auto_read_days (0 = off). A failed read is an error, never 0:
// taken for "off" it would give the nightly step an empty window that RecordAutoReadRun then
// closes for good.
func (d *DB) GlobalAutoReadDays(ctx context.Context) (int, error) {
	return settingIntErr(ctx, d.reader, SettingAutoReadDays, 0)
}

// autoReadTargets lists the feeds with auto-read on and their effective days. feedID != 0 limits it
// to that feed. days, when not nil, replaces the effective value of what it names: of the one feed
// when feedID != 0, else of the global default (feeds with their own value keep it), so a preview can
// answer "what if I set this?" before anything is saved.
func (d *DB) autoReadTargets(ctx context.Context, feedID int64, days *int) ([]autoReadTarget, error) {
	global, err := d.GlobalAutoReadDays(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: auto-read days: %w", err)
	}
	if days != nil && feedID == 0 {
		global = *days
	}
	// Disabled feeds are included (they can never clear themselves); only the archive is skipped.
	q := `SELECT f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.auto_read_days
		FROM feeds f WHERE f.disabled_reason IS NOT 'archive'`
	var args []any
	if feedID != 0 {
		q += " AND f.id = ?"
		args = append(args, feedID)
	}
	rows, err := d.reader.QueryContext(ctx, q+" ORDER BY f.id", args...)
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
	// PerFeedMarks (the nightly step) keeps a per-feed high-water mark; see RunAutoRead.
	PerFeedMarks bool
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
//
// A feed with nothing to mark costs two reader-pool EXISTS probes and no gate: the write
// transactions (and the gate) are only taken for a feed, and for its ledger, with candidates.
//
// With o.PerFeedMarks (the nightly step) each feed keeps its own high-water mark, the instant of its
// last completed window (settingAutoReadFeedMarks): its window starts at the later of o.Since and
// its mark, and the mark moves to o.Now as the feed completes. A run that fails or is cancelled at
// feed k therefore leaves feeds 1 to k-1 behind their finished windows, and the rerun does not mark
// again what a reader has marked unread since.
func (d *DB) RunAutoRead(ctx context.Context, o AutoReadOptions) (AutoReadResult, error) {
	var res AutoReadResult
	if o.Batch <= 0 {
		o.Batch = AutoReadBatch
	}
	targets, err := d.autoReadTargets(ctx, o.FeedID, o.Days)
	if err != nil {
		return res, err
	}
	var marks map[int64]int64
	if o.PerFeedMarks {
		if marks, err = loadAutoReadMarks(ctx, d.reader); err != nil {
			return res, err
		}
	}
	// dirty: marks moved in memory but not stored. They are stored after each feed that marked
	// something, and when the run ends early (best effort, the caller's context may be the reason).
	dirty := false
	flush := func(ctx context.Context) error {
		if !dirty {
			return nil
		}
		if err := d.saveAutoReadMarks(ctx, marks); err != nil {
			return err
		}
		dirty = false
		return nil
	}
	fail := func(err error) (AutoReadResult, error) {
		if dirty {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if ferr := flush(fctx); ferr != nil {
				err = fmt.Errorf("%w (and the per-feed marks: %v)", err, ferr)
			}
		}
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
		since := o.Since
		if m, ok := marks[t.id]; ok && !since.IsZero() && time.Unix(m, 0).After(since) {
			since = time.Unix(m, 0)
		}
		lo, hi := autoReadBounds(o.Now, since, t.days)
		if hi <= lo {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		var hasItems, hasLedger bool
		if err := d.reader.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM items WHERE "+autoReadWhere+")",
			t.id, o.Now.Unix(), lo, hi).Scan(&hasItems); err != nil {
			return fail(err)
		}
		if err := d.reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM trimmed_items
			WHERE feed_id = ?1 AND read = 0 AND id > ?2 AND id <= ?3)`, t.id, lo, hi).Scan(&hasLedger); err != nil {
			return fail(err)
		}
		marked := 0
		for hasItems {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			var changed []int64
			_, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
				rows, err := tx.QueryContext(ctx, `UPDATE items SET read = 1, read_at = ?5, state_changed_at = ?5 WHERE id IN (
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
				return fail(err)
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
				return fail(err)
			}
		}
		// The ledger: rows of items trimmed earlier. Read state only, so they are not events.
		for hasLedger {
			if err := ctx.Err(); err != nil {
				return fail(err)
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
				return fail(err)
			}
			res.Batches++
			res.Ledger += int(n)
			marked += int(n)
			if n < int64(o.Batch) {
				break
			}
			if err := pause(); err != nil {
				return fail(err)
			}
		}
		if marked > 0 {
			res.Feeds++
		}
		if o.PerFeedMarks {
			marks[t.id] = o.Now.Unix()
			dirty = true
			if marked > 0 {
				if err := flush(ctx); err != nil {
					return fail(err)
				}
			}
		}
	}
	return res, nil
}

// loadAutoReadMarks reads the per-feed high-water marks: feed id to the unix time its last
// completed window ended. A missing or unreadable value is no marks (each feed then starts at the
// run's Since); a failed read is an error.
func loadAutoReadMarks(ctx context.Context, q Querier) (map[int64]int64, error) {
	out := map[int64]int64{}
	raw, ok, err := settingRawErr(ctx, q, settingAutoReadFeedMarks)
	if err != nil {
		return nil, fmt.Errorf("store: auto-read marks: %w", err)
	}
	if !ok {
		return out, nil
	}
	var m map[string]int64
	if json.Unmarshal(raw, &m) != nil {
		return out, nil
	}
	for k, v := range m {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil && v > 0 {
			out[id] = v
		}
	}
	return out, nil
}

// saveAutoReadMarks stores the marks (an empty map deletes the setting).
func (d *DB) saveAutoReadMarks(ctx context.Context, marks map[int64]int64) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return putAutoReadMarks(ctx, tx, marks, time.Now().Unix())
	})
}

func putAutoReadMarks(ctx context.Context, tx *sql.Tx, marks map[int64]int64, at int64) error {
	if len(marks) == 0 {
		_, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", settingAutoReadFeedMarks)
		return err
	}
	m := make(map[string]int64, len(marks))
	for id, v := range marks {
		m[strconv.FormatInt(id, 10)] = v
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES(?1, ?2, ?3)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingAutoReadFeedMarks, string(raw), at)
	return err
}

// AutoReadLastRun is when the nightly auto-read step last ran; ok is false when it never has. A
// failed read is an error, not "never": the caller must not record a run on it.
func AutoReadLastRun(ctx context.Context, q Querier) (t time.Time, ok bool, err error) {
	n, err := settingInt64Err(ctx, q, settingAutoReadLastRun)
	if err != nil {
		return time.Time{}, false, err
	}
	if n <= 0 {
		return time.Time{}, false, nil
	}
	return time.Unix(n, 0), true, nil
}

// RecordAutoReadRun stores the instant the nightly auto-read step ran and clears the per-feed
// marks (every feed has completed the window that ends at now).
func (d *DB) RecordAutoReadRun(ctx context.Context, now time.Time) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putAutoReadMarks(ctx, tx, nil, now.Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES(?1, ?2, ?3)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			settingAutoReadLastRun, strconv.FormatInt(now.Unix(), 10), now.Unix())
		return err
	})
}

// settingInt64Err reads an integer setting: 0 when it is missing or not a number, an error when
// the read itself failed.
func settingInt64Err(ctx context.Context, q Querier, key string) (int64, error) {
	raw, ok, err := settingRawErr(ctx, q, key)
	if err != nil || !ok {
		return 0, err
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if perr != nil {
		return 0, nil
	}
	return n, nil
}
