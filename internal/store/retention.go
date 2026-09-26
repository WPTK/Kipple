package store

import (
	"context"
	"database/sql"
	"fmt"
)

// trimFeed applies design §5 to one feed inside a write transaction: rank the
// non-starred, non-held items with real (not muted) items first, then by sort_at DESC, id DESC, tombstone everything
// past N into trimmed_items (plus restore stubs when restore_days > 0) and
// delete them. N comes from the feed override or retention.default; N = 0 skips.
// firstNewID is the first id allocated by the surrounding fetch (MaxInt64 when
// none): unread items trimmed at or past it are not counted in
// trimmed_unread_count. It returns the number of items removed.
func trimFeed(ctx context.Context, tx *sql.Tx, feedID, now, firstNewID int64) (int64, error) {
	var override sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT retention FROM feeds WHERE id = ?", feedID).Scan(&override); err != nil {
		return 0, fmt.Errorf("retention: read feed: %w", err)
	}
	set := LoadFetchSettings(ctx, tx)
	n := set.RetentionDefault
	if override.Valid {
		n = int(override.Int64)
	}
	if n <= 0 {
		return 0, nil
	}

	stmts := []struct {
		sql  string
		args []any
	}{
		{`CREATE TEMP TABLE IF NOT EXISTS trim_set (id INTEGER PRIMARY KEY) STRICT`, nil},
		{`DELETE FROM temp.trim_set`, nil},
		{`INSERT INTO temp.trim_set(id)
		    SELECT id FROM items
		    WHERE feed_id = ?1 AND starred = 0 AND (retain_until IS NULL OR retain_until <= ?2)
		    ORDER BY (muted_by IS NULL) DESC, sort_at DESC, id DESC
		    LIMIT -1 OFFSET ?3`, []any{feedID, now, n}},
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

// TrimOnly runs the retention transaction for one feed and logs a trim_only
// fetch_log row. It takes the commit gate like a fetch commit.
func (d *DB) TrimOnly(ctx context.Context, feedID int64, trigger string) (int64, error) {
	release, err := d.AcquireGate(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var trimmed int64
	started := d.clock.Now()
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if trimmed, err = trimFeed(ctx, tx, feedID, started.Unix(), maxInt64); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fetch_log (feed_id, trigger, started_at, duration_ms, outcome, trimmed_items)
			VALUES (?, ?, ?, ?, 'trim_only', ?)`, feedID, trigger, started.Unix(), d.clock.Now().Sub(started).Milliseconds(), trimmed); err != nil {
			return err
		}
		return capFetchLog(ctx, tx, feedID, started.Unix())
	})
	return trimmed, err
}

const maxInt64 = int64(^uint64(0) >> 1)

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
