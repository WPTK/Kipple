package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/WPTK/kipple/internal/sanitize"
)

// SettingContentPolicy is the sanitize.PolicyVersion the stored article HTML was last cleaned under.
// A restore deletes it (the backup's rows come from elsewhere), and raising the version leaves it
// behind, so EnsureContentPolicy cleans everything once more.
const SettingContentPolicy = "sys.content_policy"

// contentBatch is how many rows one pass step reads, cleans and writes back.
const contentBatch = 200

// contentTables are the tables holding stored article HTML: the feed's own content, extracted full text, and the
// stubs a trimmed item is restored from. words is the statement that stores the recounted words of a cleaned row;
// it takes the count and the row id.
var contentTables = []struct {
	table, idCol, extra, words string
}{
	{"item_content", "item_id", "", "UPDATE items SET word_count = ? WHERE id = ?"},
	{"item_fulltext", "item_id", " AND content_html IS NOT NULL", "UPDATE item_fulltext SET word_count = ? WHERE item_id = ?"},
	{"trimmed_content", "id", "", "UPDATE trimmed_content SET word_count = ? WHERE id = ?"},
}

// EnsureContentPolicy cleans the stored article HTML (feed content, extracted text and restore stubs) again with
// the current policy, unless it was already cleaned under this policy version, then records the version.
// It returns the number of rows it changed. Rows already clean are not written. Cleaning happens outside the
// write transaction, in batches, so the writer is only held to store the rows that changed. The version is
// recorded after the last batch, so a pass that stops early starts again from the first row (rows already clean
// are read but not written).
func (d *DB) EnsureContentPolicy(ctx context.Context) (changed int, err error) {
	want := strconv.Itoa(sanitize.PolicyVersion)
	if raw, ok, err := settingRawErr(ctx, d.reader, SettingContentPolicy); err != nil {
		return 0, err
	} else if ok && string(raw) == want {
		return 0, nil
	}
	for _, t := range contentTables {
		var after int64
		for {
			n, last, done, err := d.cleanBatch(ctx, t.table, t.idCol, t.extra, t.words, after)
			if err != nil {
				return changed, fmt.Errorf("store: clean %s: %w", t.table, err)
			}
			changed += n
			after = last
			if done {
				break
			}
		}
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = unixepoch()`, SettingContentPolicy, want)
		return err
	})
	return changed, err
}

// cleanBatch cleans the next contentBatch rows of table after id `after`, and returns how many it changed, the
// last id it looked at and whether the table is finished. A row is written back only if it still holds the HTML
// that was cleaned: one a fetch replaced meanwhile came in under the current policy already.
func (d *DB) cleanBatch(ctx context.Context, table, idCol, extra, words string, after int64) (changed int, last int64, done bool, err error) {
	type row struct {
		id          int64
		html, clean string
	}
	rows, err := d.reader.QueryContext(ctx, `SELECT `+idCol+`, content_html FROM `+table+` WHERE `+idCol+` > ?`+extra+` ORDER BY `+idCol+` LIMIT ?`, after, contentBatch)
	if err != nil {
		return 0, after, false, err
	}
	var batch, dirty []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.html); err != nil {
			_ = rows.Close()
			return 0, after, false, err
		}
		batch = append(batch, r)
	}
	if err := rows.Close(); err != nil {
		return 0, after, false, err
	}
	if err := rows.Err(); err != nil {
		return 0, after, false, err
	}
	for _, r := range batch {
		last = r.id
		if clean := sanitize.Resanitize(r.html); clean != r.html {
			dirty = append(dirty, row{id: r.id, html: r.html, clean: clean})
		}
	}
	if len(dirty) == 0 {
		return 0, last, len(batch) < contentBatch, nil
	}
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		changed = 0
		for _, r := range dirty {
			text := sanitize.PlainText(r.clean)
			res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET content_html = ?, content_text = ? WHERE `+idCol+` = ? AND content_html = ?`,
				r.clean, text, r.id, r.html)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, words, sanitize.WordCount(text), r.id); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	if err != nil {
		return 0, after, false, err
	}
	return changed, last, len(batch) < contentBatch, nil
}
