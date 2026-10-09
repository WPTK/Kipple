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

// contentBatch is how many rows one write transaction cleans, so the pass never holds the writer for long.
const contentBatch = 200

// EnsureContentPolicy cleans the stored article HTML (feed content and extracted text) again with the
// current policy, unless it was already cleaned under this policy version, then records the version.
// It returns the number of rows it changed. Rows already clean are not written. The pass is resumable:
// the version is recorded only after the last batch, and a rerun redoes at most unchanged rows.
func (d *DB) EnsureContentPolicy(ctx context.Context) (changed int, err error) {
	want := strconv.Itoa(sanitize.PolicyVersion)
	if raw, ok, err := settingRawErr(ctx, d.reader, SettingContentPolicy); err != nil {
		return 0, err
	} else if ok && string(raw) == want {
		return 0, nil
	}
	for _, t := range []struct{ table, nullable string }{
		{"item_content", ""},
		{"item_fulltext", " AND content_html IS NOT NULL"},
	} {
		var after int64
		for {
			var n, last int
			var done bool
			err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
				var e error
				n, last, done, e = cleanBatch(ctx, tx, t.table, t.nullable, after)
				return e
			})
			if err != nil {
				return changed, fmt.Errorf("store: clean %s: %w", t.table, err)
			}
			changed += n
			after = int64(last)
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

// cleanBatch cleans the next contentBatch rows of table after item id `after`, and returns how many
// it changed, the last id it looked at and whether the table is finished.
func cleanBatch(ctx context.Context, tx *sql.Tx, table, extra string, after int64) (changed, last int, done bool, err error) {
	type row struct {
		id   int64
		html string
	}
	rows, err := tx.QueryContext(ctx, `SELECT item_id, content_html FROM `+table+` WHERE item_id > ?`+extra+` ORDER BY item_id LIMIT ?`, after, contentBatch)
	if err != nil {
		return 0, 0, false, err
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.html); err != nil {
			_ = rows.Close()
			return 0, 0, false, err
		}
		batch = append(batch, r)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, false, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, false, err
	}
	for _, r := range batch {
		clean := sanitize.Resanitize(r.html)
		last = int(r.id)
		if clean == r.html {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET content_html = ?, content_text = ? WHERE item_id = ?`,
			clean, sanitize.PlainText(clean), r.id); err != nil {
			return 0, 0, false, err
		}
		changed++
	}
	return changed, last, len(batch) < contentBatch, nil
}
