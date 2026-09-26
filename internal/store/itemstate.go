package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// The functions here are the only paths that change read and starred state.
// They take a transaction and no client or stats argument, so they cannot
// record reading stats (design §8, structural rule 1).

// StateResult reports what a state change did.
type StateResult struct {
	// Changed are the item ids whose flag flipped (including restored ones).
	Changed []int64
	// Restored are ids that came back from the retention ledger.
	Restored []int64
	// LedgerRead are trimmed-ledger ids a scope mark flipped to read. They are not
	// items, so they are not in Changed; a client keeps them for undo
	// (UnreadLedger), since the Reader API still reports the ledger.
	LedgerRead []int64
}

func idsJSON(ids []int64) (string, error) {
	b, err := json.Marshal(ids)
	return string(b), err
}

// scanIDs drains and closes rows of (id, feed_id) before the next statement.
func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id, feed int64
		if err := rows.Scan(&id, &feed); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetRead marks ids read or unread (design §6.7, §5). Unknown ids are ignored.
// Marking read also updates the retention ledger; marking unread restores a
// trimmed item from its stub when one exists, else clears the ledger flag.
// The WHERE guards make replays no-ops.
func SetRead(ctx context.Context, tx *sql.Tx, ids []int64, read bool, now int64) (StateResult, error) {
	var res StateResult
	if len(ids) == 0 {
		return res, nil
	}
	js, err := idsJSON(ids)
	if err != nil {
		return res, err
	}
	if read {
		rows, err := tx.QueryContext(ctx, `UPDATE items SET read = 1, read_at = ?1
			WHERE id IN (SELECT value FROM json_each(?2)) AND read = 0 RETURNING id, feed_id`, now, js)
		if err != nil {
			return res, err
		}
		if res.Changed, err = scanIDs(rows); err != nil {
			return res, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE trimmed_items SET read = 1
			WHERE id IN (SELECT value FROM json_each(?1)) AND read = 0`, js)
		return res, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE items SET read = 0, read_at = NULL
		WHERE id IN (SELECT value FROM json_each(?1)) AND read = 1 RETURNING id, feed_id`, js)
	if err != nil {
		return res, err
	}
	if res.Changed, err = scanIDs(rows); err != nil {
		return res, err
	}
	restored, err := restoreTrimmed(ctx, tx, ids, "unread", now)
	if err != nil {
		return res, err
	}
	res.Restored = restored
	res.Changed = append(res.Changed, restored...)
	// Ledger ids with no stub: only the flag can change.
	_, err = tx.ExecContext(ctx, `UPDATE trimmed_items SET read = 0
		WHERE id IN (SELECT value FROM json_each(?1)) AND read = 1`, js)
	return res, err
}

// SetStarred stars or unstars ids. Starring a trimmed id restores it from its
// stub (a ledger id without a stub cannot be starred and is ignored).
func SetStarred(ctx context.Context, tx *sql.Tx, ids []int64, starred bool, now int64) (StateResult, error) {
	var res StateResult
	if len(ids) == 0 {
		return res, nil
	}
	js, err := idsJSON(ids)
	if err != nil {
		return res, err
	}
	if !starred {
		rows, err := tx.QueryContext(ctx, `UPDATE items SET starred = 0, starred_at = NULL
			WHERE id IN (SELECT value FROM json_each(?1)) AND starred = 1 RETURNING id, feed_id`, js)
		if err != nil {
			return res, err
		}
		res.Changed, err = scanIDs(rows)
		return res, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE items SET starred = 1, starred_at = ?1
		WHERE id IN (SELECT value FROM json_each(?2)) AND starred = 0 RETURNING id, feed_id`, now, js)
	if err != nil {
		return res, err
	}
	if res.Changed, err = scanIDs(rows); err != nil {
		return res, err
	}
	restored, err := restoreTrimmed(ctx, tx, ids, "star", now)
	if err != nil {
		return res, err
	}
	res.Restored = restored
	res.Changed = append(res.Changed, restored...)
	return res, nil
}

// restoreTrimmed puts trimmed items that still have a restore stub back into
// items and item_content with their original id, uid and sort_at (design §5).
// Only ledger rows trimmed within retention.restore_days qualify: the nightly
// purge deletes older stubs, but until it runs a stub can outlive the window,
// and a restore must not depend on when the purge last ran. mode is "star" or
// "unread". It returns the ids actually restored (inserted into items).
func restoreTrimmed(ctx context.Context, tx *sql.Tx, ids []int64, mode string, now int64) ([]int64, error) {
	if mode != "star" && mode != "unread" {
		return nil, fmt.Errorf("store: restore mode %q", mode)
	}
	js, err := idsJSON(ids)
	if err != nil {
		return nil, err
	}
	cutoff := now - int64(LoadFetchSettings(ctx, tx).RestoreDays)*86400
	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.feed_id FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id
		WHERE t.id IN (SELECT value FROM json_each(?1)) AND t.trimmed_at >= ?2`, js, cutoff)
	if err != nil {
		return nil, err
	}
	restorable, err := scanIDs(rows)
	if err != nil || len(restorable) == 0 {
		return nil, err
	}
	rjs, err := idsJSON(restorable)
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `INSERT INTO items (id, feed_id, uid, read, starred, read_at, starred_at, retain_until, fulltext_mode,
		                   published_at, updated_at, sort_at, word_count, content_hash, text_hash,
		                   url, title, author, image_url, origin_title)
		  SELECT t.id, t.feed_id, t.uid,
		         CASE WHEN ?1 = 'unread' THEN 0 ELSE t.read END,
		         CASE WHEN ?1 = 'star' THEN 1 ELSE 0 END,
		         CASE WHEN ?1 = 'unread' THEN NULL WHEN t.read = 1 THEN ?2 END,
		         CASE WHEN ?1 = 'star' THEN ?2 END,
		         CASE WHEN ?1 = 'unread' THEN ?2 + 7*86400 END,
		         c.fulltext_mode, c.published_at, c.updated_at, c.sort_at, c.word_count, c.content_hash, c.text_hash,
		         c.url, c.title, c.author, c.image_url, c.origin_title
		  FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id
		  WHERE t.id IN (SELECT value FROM json_each(?3))
		ON CONFLICT DO NOTHING RETURNING id, feed_id`, mode, now, rjs)
	if err != nil {
		return nil, fmt.Errorf("store: restore trimmed: %w", err)
	}
	inserted, err := scanIDs(rows)
	if err != nil {
		return nil, fmt.Errorf("store: restore trimmed: %w", err)
	}
	stmts := []string{
		`INSERT INTO item_content (item_id, content_html, content_text, enclosures_json, categories_json)
		  SELECT c.id, c.content_html, c.content_text, c.enclosures_json, c.categories_json FROM trimmed_content c
		  WHERE c.id IN (SELECT value FROM json_each(?3)) AND EXISTS (SELECT 1 FROM items i WHERE i.id = c.id)
		ON CONFLICT DO NOTHING`,
		`DELETE FROM trimmed_items WHERE id IN (SELECT value FROM json_each(?3)) AND id IN (SELECT id FROM items)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s, mode, now, rjs); err != nil {
			return nil, fmt.Errorf("store: restore trimmed: %w", err)
		}
	}
	return inserted, nil
}

// MarkScope selects what MarkAllRead touches. The zero value is every item.
type MarkScope struct {
	FeedID   int64
	FolderID int64
	Starred  bool // starred items only; the ledger is skipped (starred items are never in it)
	// HoldCut > 0 leaves out items held back from the Reader API (HeldSQL): a client
	// cannot have seen them, so its mark-all must not read them.
	HoldCut int64
	// HoldPending is DB.HoldPending() taken when HoldCut is set; empty means nothing is pending.
	HoldPending string
}

// MarkAllRead marks unread items with id <= maxID read inside scope, and the
// matching ledger rows (design §6.8). It returns the number of items changed.
func MarkAllRead(ctx context.Context, tx *sql.Tx, scope MarkScope, maxID, now int64) (int64, error) {
	where, feedWhere := "", ""
	args := []any{sql.Named("ts", maxID), sql.Named("now", now)}
	switch {
	case scope.FeedID != 0:
		feedWhere = " AND feed_id = :feed"
		args = append(args, sql.Named("feed", scope.FeedID))
	case scope.FolderID != 0:
		feedWhere = " AND feed_id IN (SELECT id FROM feeds WHERE folder_id = :folder)"
		args = append(args, sql.Named("folder", scope.FolderID))
	case scope.Starred:
		where = " AND starred = 1"
	}
	itemArgs, held := args, ""
	if scope.HoldCut > 0 {
		held = " AND NOT " + HeldSQL(txFulltextAll(ctx, tx))
		itemArgs = append(append([]any{}, args...), sql.Named("hold_cut", scope.HoldCut), sql.Named("pending", cmp.Or(scope.HoldPending, "[]")))
	}
	res, err := tx.ExecContext(ctx, "UPDATE items SET read = 1, read_at = :now WHERE read = 0 AND id <= :ts"+where+feedWhere+held, itemArgs...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if !scope.Starred {
		if _, err := tx.ExecContext(ctx, "UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :ts"+feedWhere, args...); err != nil {
			return n, err
		}
	}
	return n, nil
}

// MaxCommittedID is the highest committed item or ledger id, read on the
// reader pool (never the allocator, which can be ahead of visible rows). It is
// the default mark-all-as-read cutoff.
func (d *DB) MaxCommittedID(ctx context.Context) (int64, error) {
	var id int64
	err := d.reader.QueryRowContext(ctx, `SELECT max(COALESCE((SELECT max(id) FROM items), 0),
		COALESCE((SELECT max(id) FROM trimmed_items), 0))`).Scan(&id)
	return id, err
}

// UnreadLedger flips ledger rows back to unread without restoring any stub: the
// undo of a bulk mark-read, whose ledger rows were only ever flag-flipped.
// Live items are untouched. It returns the ids that changed.
func UnreadLedger(ctx context.Context, tx *sql.Tx, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	js, err := idsJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE trimmed_items SET read = 0
		WHERE id IN (SELECT value FROM json_each(?1)) AND read = 1
		  AND id NOT IN (SELECT id FROM items) RETURNING id, feed_id`, js)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}
