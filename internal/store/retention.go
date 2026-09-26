package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// trimBatch bounds the items one transaction trims (or deletes with a feed): at
// roughly 100 µs per item (the FTS delete trigger dominates) 2000 rows is about
// 0.2 s, far inside the writer's 10 s deadline however large the feed is. The
// rest is left for the next batch. A variable so tests can shrink it.
var trimBatch = 2000

// trimFeedBatch applies design §5 to one feed inside a write transaction: keep the newest
// (sort_at DESC, id DESC) real items and the newest muted ones under the muted allowance
// (see mutedAllowance), tombstone everything else into trimmed_items (plus restore stubs when
// restore_days > 0) and delete it. N comes from the feed override or retention.default; N = 0 skips.
// firstNewID is the first id allocated by the surrounding fetch (MaxInt64 when
// none): unread items trimmed at or past it are not counted in
// trimmed_unread_count.
//
// At most limit items go per call, the oldest (sort_at ASC, id ASC) of the trim
// set first, so the transaction stays short on a feed with a huge backlog.
// Repeating the call converges on exactly the one-shot result: the kept items
// never enter the trim set, so the muted allowance recomputed from the smaller
// counts does not change. more reports a full batch, so another call may find
// more to trim (TrimOnly loops on it; a fetch commit reports it as
// CommitInfo.TrimPending and the scheduler queues a trim job for the feed).
func trimFeedBatch(ctx context.Context, tx *sql.Tx, feedID, now, firstNewID int64, limit int) (n int64, more bool, err error) {
	n, err = trimFeedLimit(ctx, tx, feedID, now, firstNewID, limit)
	return n, err == nil && n >= int64(limit), err
}

func trimFeedLimit(ctx context.Context, tx *sql.Tx, feedID, now, firstNewID int64, limit int) (int64, error) {
	var override sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT retention FROM feeds WHERE id = ?", feedID).Scan(&override); err != nil {
		return 0, fmt.Errorf("retention: read feed: %w", err)
	}
	set, err := LoadFetchSettingsErr(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("retention: settings: %w", err)
	}
	n := set.RetentionDefault
	if override.Valid {
		n = int(override.Int64)
	}
	if n <= 0 {
		return 0, nil
	}

	var real, muted int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE muted_by IS NULL), count(*) FILTER (WHERE muted_by IS NOT NULL)
		FROM items WHERE feed_id = ?1 AND starred = 0 AND (retain_until IS NULL OR retain_until <= ?2)`, feedID, now).Scan(&real, &muted); err != nil {
		return 0, fmt.Errorf("retention: count: %w", err)
	}
	keepMuted := mutedAllowance(n, real, muted)

	stmts := []struct {
		sql  string
		args []any
	}{
		{`CREATE TEMP TABLE IF NOT EXISTS trim_set (id INTEGER PRIMARY KEY) STRICT`, nil},
		{`DELETE FROM temp.trim_set`, nil},
		{`INSERT INTO temp.trim_set(id)
		    SELECT id FROM (
		      SELECT id, sort_at, muted_by IS NOT NULL AS m,
		             row_number() OVER (PARTITION BY muted_by IS NOT NULL ORDER BY sort_at DESC, id DESC) AS rn
		      FROM items
		      WHERE feed_id = ?1 AND starred = 0 AND (retain_until IS NULL OR retain_until <= ?2))
		    WHERE rn > CASE WHEN m THEN ?3 ELSE ?4 END
		    ORDER BY sort_at ASC, id ASC LIMIT ?5`, []any{feedID, now, keepMuted, n - keepMuted, limit}},
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s.sql, s.args...); err != nil {
			return 0, fmt.Errorf("retention: %w", err)
		}
	}
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM temp.trim_set").Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}

	rest := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
		    SELECT i.id, i.feed_id, i.uid, i.read, ?1, ?1
		    FROM temp.trim_set t JOIN items i ON i.id = t.id
		    WHERE true
		  ON CONFLICT (feed_id, uid) DO UPDATE SET
		    id = excluded.id, read = excluded.read, trimmed_at = excluded.trimmed_at, last_seen_at = excluded.last_seen_at`, []any{now}},
		{`INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
		                              url, title, author, image_url, origin_title, fulltext_mode,
		                              content_html, content_text, enclosures_json, categories_json)
		    SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash,
		           i.url, i.title, i.author, i.image_url, i.origin_title, i.fulltext_mode,
		           c.content_html, c.content_text, c.enclosures_json, c.categories_json
		    FROM temp.trim_set t JOIN items i ON i.id = t.id JOIN item_content c ON c.item_id = i.id
		    WHERE ?1 > 0
		  ON CONFLICT (id) DO NOTHING`, []any{set.RestoreDays}},
		{`UPDATE feeds SET
		    trimmed_unread_count = trimmed_unread_count +
		      (SELECT count(*) FROM temp.trim_set t JOIN items i ON i.id = t.id WHERE i.read = 0 AND i.id < ?2),
		    trimmed_unread_since = COALESCE(trimmed_unread_since, ?3)
		  WHERE id = ?1 AND EXISTS (SELECT 1 FROM temp.trim_set t JOIN items i ON i.id = t.id WHERE i.read = 0 AND i.id < ?2)`,
			[]any{feedID, firstNewID, now}},
	}
	for _, s := range rest {
		if _, err := tx.ExecContext(ctx, s.sql, s.args...); err != nil {
			return 0, fmt.Errorf("retention: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM items WHERE id IN (SELECT id FROM temp.trim_set)")
	if err != nil {
		return 0, fmt.Errorf("retention: delete: %w", err)
	}
	return res.RowsAffected()
}

// mutedAllowance is how many muted items a feed keeps under cap n, given its real and muted
// counts (starred and held items excluded). Muted items count against n, but they are kept
// separately from the real ones so noise cannot displace real items nor be trimmed the moment it
// arrives: the newest n/5 muted items are always kept, and more while the real items leave room
// (n - real). Real items get the rest, so a feed never keeps more than n of them together.
func mutedAllowance(n, real, muted int) int {
	return min(muted, max(n/5, n-real))
}

// TrimOnly runs the retention trim for one feed and logs one trim_only
// fetch_log row. It trims in batches of at most trimBatch items, each its own
// transaction behind the commit gate (like a fetch commit chunk), so fetch
// commits and edit-tags interleave and no single write nears the writer's
// deadline. The first batch writes the log row and each later one adds to it, so
// the row always matches what committed. Every batch is durable: when ctx ends
// between batches TrimOnly returns the items trimmed so far with ctx's error,
// and the next trim resumes where it stopped.
func (d *DB) TrimOnly(ctx context.Context, feedID int64, trigger string) (int64, error) {
	n, _, err := d.TrimOnlyBudget(ctx, feedID, trigger, TrimBudget{})
	return n, err
}

// TrimBudget bounds one trim job (TrimOnlyBudget).
type TrimBudget struct {
	// Total is the job's time budget: no batch starts once it is spent (the first
	// always runs). Zero means until done.
	Total time.Duration
	// PerBatch is each batch's own deadline, gate wait included. When set, a batch
	// is detached from ctx's cancellation (a started batch finishes, as a fetch
	// commit does at shutdown) and ctx is only checked between batches. Zero runs
	// each batch under ctx itself.
	PerBatch time.Duration
}

// TrimOnlyBudget is TrimOnly within a budget: it trims batch after batch until the
// feed is within its cap, ctx ends or the budget is spent, and reports in more
// whether it stopped with a full last batch (so more may be left: the scheduler
// queues another trim job). A ctx that ends between batches returns its error with
// more set when a batch already ran.
func (d *DB) TrimOnlyBudget(ctx context.Context, feedID int64, trigger string, b TrimBudget) (total int64, more bool, err error) {
	started := d.clock.Now()
	wall := time.Now()
	var logID int64
	for batches := 0; ; batches++ {
		if err := ctx.Err(); err != nil {
			return total, batches > 0, err // the loop only goes on after a full batch
		}
		if b.Total > 0 && batches > 0 && time.Since(wall) >= b.Total {
			return total, true, nil
		}
		var newLogID int64
		bctx, cancel := ctx, context.CancelFunc(func() {})
		if b.PerBatch > 0 {
			bctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), b.PerBatch)
		}
		trimmed, err := d.batch(bctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
			trimmed, m, err := trimFeedBatch(ctx, tx, feedID, started.Unix(), maxInt64, trimBatch)
			if err != nil {
				return 0, err
			}
			more = m
			dur := d.clock.Now().Sub(started).Milliseconds()
			if logID != 0 {
				_, err := tx.ExecContext(ctx, `UPDATE fetch_log SET trimmed_items = trimmed_items + ?2, duration_ms = ?3
					WHERE id = ?1`, logID, trimmed, dur)
				return trimmed, err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, trimmed_items)
				VALUES (?, ?, ?, ?, 'trim_only', ?)`, feedID, trigger, started.Unix(), dur, trimmed)
			if err != nil {
				return 0, err
			}
			if newLogID, err = res.LastInsertId(); err != nil {
				return 0, err
			}
			return trimmed, capFetchLog(ctx, tx, feedID, started.Unix())
		})
		cancel()
		if err != nil {
			return total, false, err
		}
		total += trimmed
		if logID == 0 {
			logID = newLogID
		}
		if afterTrimBatch != nil {
			afterTrimBatch()
		}
		if !more {
			return total, false, nil
		}
	}
}

const maxInt64 = int64(^uint64(0) >> 1)

// A feed deletion runs in three steps so a large feed never needs one long
// transaction, and so an interruption can never leave a subscribed feed with its
// history gone:
//
//  1. markFeedsDeleting, one short transaction, disables the feed and moves its
//     fetch URL to deletingURLPrefix+id. The scheduler only picks enabled feeds,
//     and a fetch already in flight finds the URL changed under it, so its commit
//     (or error bookkeeping) is dropped as stale: nothing is fetched or stored for
//     the feed again. The Reader API id (feed/<id>) does not change, and the old
//     URL is free at once for a new subscription.
//  2. purgeFeedItems empties it in bounded batches.
//  3. removeFeed deletes the row in one short transaction.
//
// Interrupted anywhere (a client that gives up, a writer deadline, a restart),
// the feed stays marked, never fetched, and deleting it again (DeleteFeed, a
// Reader API unsubscribe) or ResumeFeedDeletes finishes the job. Without the mark
// the scheduler would have refetched the emptied feed and stored its whole
// document again as new unread items under new ids.
const deletingURLPrefix = "kipple:deleting:"

// markFeedsDeleting applies step 1 to ids inside tx. The archive feed (never
// fetched) and ids that are gone are left alone; a feed already marked stays
// marked.
func markFeedsDeleting(ctx context.Context, tx *sql.Tx, ids []int64, now int64) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE feeds SET enabled = 0, disabled_reason = 'user',
			url = ?2 || id, url_key = ?2 || id, url_original = NULL, url_original_key = NULL,
			etag = NULL, last_modified = NULL, redirect_to = NULL, redirect_kind = NULL, redirect_count = 0,
			updated_at = ?3
			WHERE id = ?1 AND disabled_reason IS NOT 'archive' AND url NOT LIKE ?2 || '%'`, id, deletingURLPrefix, now); err != nil {
			return fmt.Errorf("store: mark feed %d for deletion: %w", id, err)
		}
	}
	return nil
}

// isDeleting reports whether a feed row is marked for deletion (step 1 ran).
func isDeleting(url string) bool { return strings.HasPrefix(url, deletingURLPrefix) }

// deleteBudget bounds a deletion that runs detached from its request (the UI
// delete, a Reader API unsubscribe) or from startup: at about 0.2 s per batch of
// trimBatch items it covers millions of items, and a deletion it cuts short is
// resumed by the next one.
const deleteBudget = 5 * time.Minute

// DeleteContext detaches ctx from its cancellation (a client that gives up must
// not cut a deletion between batches) and bounds it with deleteBudget.
func DeleteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), deleteBudget)
}

// ResumeFeedDeletes finishes every feed deletion that was interrupted after its
// first step (see deletingURLPrefix): purge, then remove, starred items going to
// the archive feed (the safe choice: the original request's delete_starred is
// not stored). It returns the ids it removed. Call it at startup; deleting the
// feed again does the same for one feed.
func (d *DB) ResumeFeedDeletes(ctx context.Context) ([]int64, error) {
	rows, err := d.reader.QueryContext(ctx, "SELECT id FROM feeds WHERE url LIKE ? || '%' ORDER BY id", deletingURLPrefix)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var done []int64
	for _, id := range ids {
		if err := d.DeleteFeed(ctx, id, false); err != nil && !errors.Is(err, ErrFeedNotFound) {
			return done, err
		}
		done = append(done, id)
	}
	return done, nil
}

// purgeFeedItems empties a feed that is about to be removed (removeFeed), in
// transactions of at most trimBatch rows each behind the commit gate: first its
// items (the FTS delete trigger makes these the expensive rows), then its
// trimmed ledger rows (their stubs cascade). What is left for removeFeed's own
// transaction is then small however large the feed was. With archiveStarred
// starred items are left alone for removeFeed to re-parent, and the archive feed
// itself is not touched (removeFeed decides whether it may go, and refuses with
// nothing changed while it holds starred items). The caller marks the feed first
// (markFeedsDeleting), so it is never fetched again while it is emptied. Each
// batch is durable and a feed that is gone ends the purge, so an interrupted
// delete simply resumes on retry.
// afterPurgeBatch and afterTrimBatch, when set (tests only), run after each
// committed purge or TrimOnly batch.
var afterPurgeBatch, afterTrimBatch func()

func (d *DB) purgeFeedItems(ctx context.Context, id int64, archiveStarred bool) error {
	for _, q := range []string{
		`DELETE FROM items WHERE id IN (SELECT id FROM items WHERE feed_id = ?1 AND (starred = 0 OR NOT ?2) LIMIT ?3)`,
		`DELETE FROM trimmed_items WHERE id IN (SELECT id FROM trimmed_items WHERE feed_id = ?1 LIMIT ?3)`,
	} {
		for {
			n, err := d.batch(ctx, func(ctx context.Context, tx *sql.Tx) (int64, error) {
				var reason sql.NullString
				switch err := tx.QueryRowContext(ctx, "SELECT disabled_reason FROM feeds WHERE id = ?", id).Scan(&reason); {
				case errors.Is(err, sql.ErrNoRows):
					return 0, nil
				case err != nil:
					return 0, err
				}
				if archiveStarred && reason.String == "archive" {
					return 0, nil
				}
				res, err := tx.ExecContext(ctx, q, id, archiveStarred, trimBatch)
				if err != nil {
					return 0, fmt.Errorf("store: purge feed %d: %w", id, err)
				}
				return res.RowsAffected()
			})
			if err != nil {
				return err
			}
			if afterPurgeBatch != nil {
				afterPurgeBatch()
			}
			if n < int64(trimBatch) {
				break
			}
		}
	}
	return nil
}

// capFetchLog applies the fetch_log retention: 14 days with a 50-row floor,
// keeping the last 10 error rows and every keep = 1 row.
func capFetchLog(ctx context.Context, tx *sql.Tx, feedID, now int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM fetch_log WHERE feed_id = ?1 AND keep = 0
	  AND started_at < ?2 - 14*86400
	  AND id < (SELECT id FROM fetch_log WHERE feed_id = ?1 ORDER BY id DESC LIMIT 1 OFFSET 49)
	  AND id NOT IN (SELECT id FROM fetch_log WHERE feed_id = ?1 AND outcome = 'error' ORDER BY id DESC LIMIT 10)`,
		feedID, now)
	return err
}
