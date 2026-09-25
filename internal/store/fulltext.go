package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// FulltextItem is what POST /api/items/{id}/fulltext needs about an item and
// its feed (design §7.5).
type FulltextItem struct {
	ID, FeedID int64
	URL        string
	Mode       *int // items.fulltext_mode
	Effective  int  // COALESCE(mode, feeds.fulltext)

	UserAgent                                  string // feeds.user_agent, "" = default
	AllowPrivateNet, AllowInsecureTLS, NoHTTP2 bool

	// Stored extraction, if any: HTML set means a success, Error set a failure.
	HasRow bool
	HTML   string
	Error  string
	Words  int64
	// ErrorTransient is set when the stored Error is a transient failure
	// (error_class = 'transient'); a NULL class, from before the column
	// existed, counts as permanent. AttemptedAt is the last attempt, unix s.
	ErrorTransient bool
	AttemptedAt    int64
}

// GetFulltextItem loads a live item (a trimmed stub has no row here).
func (d *DB) GetFulltextItem(ctx context.Context, id int64) (it FulltextItem, ok bool, err error) {
	var mode sql.NullInt64
	var ua, html, ferr, eclass sql.NullString
	var row, words, attempted sql.NullInt64
	var priv, insecure, noH2 int
	err = d.reader.QueryRowContext(ctx, `SELECT i.id, i.feed_id, i.url, i.fulltext_mode, COALESCE(i.fulltext_mode, f.fulltext),
			f.user_agent, f.allow_private_net, f.allow_insecure_tls, f.disable_http2,
			ft.item_id, ft.content_html, ft.error, ft.word_count, ft.error_class, ft.extracted_at
		FROM items i JOIN feeds f ON f.id = i.feed_id LEFT JOIN item_fulltext ft ON ft.item_id = i.id WHERE i.id = ?`, id).
		Scan(&it.ID, &it.FeedID, &it.URL, &mode, &it.Effective, &ua, &priv, &insecure, &noH2, &row, &html, &ferr, &words, &eclass, &attempted)
	if errors.Is(err, sql.ErrNoRows) {
		return it, false, nil
	}
	if err != nil {
		return it, false, err
	}
	if mode.Valid {
		m := int(mode.Int64)
		it.Mode = &m
	}
	it.UserAgent = ua.String
	it.AllowPrivateNet, it.AllowInsecureTLS, it.NoHTTP2 = priv == 1, insecure == 1, noH2 == 1
	it.HasRow, it.HTML, it.Error, it.Words = row.Valid, html.String, ferr.String, words.Int64
	it.ErrorTransient, it.AttemptedAt = eclass.String == "transient", attempted.Int64
	return it, true, nil
}

// SetFulltextMode sets items.fulltext_mode (nil = follow the feed). It reports
// whether the item exists.
func (d *DB) SetFulltextMode(ctx context.Context, id int64, mode *int) (bool, error) {
	var found bool
	err := d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var m any
		if mode != nil {
			m = *mode
		}
		res, err := tx.ExecContext(ctx, "UPDATE items SET fulltext_mode = ? WHERE id = ?", m, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		found = n > 0
		return err
	})
	return found, err
}

// FulltextSave is one extraction outcome. Error non-empty stores a failure
// (content NULL).
type FulltextSave struct {
	HTML, Text, ImageURL, SourceURL string
	WordCount                       int
	Error                           string
	ErrorTransient                  bool // Error is worth retrying later
}

// errorClass is the item_fulltext.error_class value for a failure.
func errorClass(transient bool) string {
	if transient {
		return "transient"
	}
	return "permanent"
}

// SaveFulltext upserts the item's item_fulltext row. A failure never replaces a
// stored success (the earlier content stays readable and Error is only
// reported to the caller).
func (d *DB) SaveFulltext(ctx context.Context, id, now int64, s FulltextSave) error {
	_, err := d.saveFulltext(ctx, id, now, s, "")
	return err
}

// SaveFulltextIfURL is SaveFulltext for a background extraction: it writes only
// while the item still exists with the given URL, so a result for a page the
// item no longer points at is dropped. It reports whether a row was written.
func (d *DB) SaveFulltextIfURL(ctx context.Context, id int64, url string, now int64, s FulltextSave) (bool, error) {
	return d.saveFulltext(ctx, id, now, s, url)
}

func (d *DB) saveFulltext(ctx context.Context, id, now int64, s FulltextSave, onlyURL string) (written bool, err error) {
	err = d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var res sql.Result
		var err error
		if s.Error != "" {
			res, err = tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, extracted_at, error, error_class)
				SELECT ?1, ?2, ?3, ?4 WHERE EXISTS (SELECT 1 FROM items WHERE id = ?1 AND (?5 = '' OR url = ?5))
				ON CONFLICT (item_id) DO UPDATE SET error = excluded.error, extracted_at = excluded.extracted_at, error_class = excluded.error_class
				WHERE item_fulltext.content_html IS NULL`, id, now, s.Error, errorClass(s.ErrorTransient), onlyURL)
		} else {
			res, err = tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, content_html, content_text, word_count, image_url, source_url, extracted_at, error, error_class)
				SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, NULL, NULL WHERE EXISTS (SELECT 1 FROM items WHERE id = ?1 AND (?8 = '' OR url = ?8))
				ON CONFLICT (item_id) DO UPDATE SET content_html = excluded.content_html, content_text = excluded.content_text,
					word_count = excluded.word_count, image_url = excluded.image_url, source_url = excluded.source_url,
					extracted_at = excluded.extracted_at, error = NULL, error_class = NULL`,
				id, s.HTML, s.Text, s.WordCount, nullStr(s.ImageURL), nullStr(s.SourceURL), now, onlyURL)
		}
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		written = n > 0
		return err
	})
	return written, err
}

// KnownUIDs returns which of uids the feed already has, live or as a trimmed
// tombstone. The worker uses it to pick the new items worth extracting before
// the commit (design §4.3); the commit itself stays the authority on what is new.
func (d *DB) KnownUIDs(ctx context.Context, feedID int64, uids []string) (map[string]bool, error) {
	known := map[string]bool{}
	if len(uids) == 0 {
		return known, nil
	}
	b, _ := json.Marshal(uids)
	rows, err := d.reader.QueryContext(ctx, `SELECT uid FROM items WHERE feed_id = ?1 AND uid IN (SELECT value FROM json_each(?2))
		UNION SELECT uid FROM trimmed_items WHERE feed_id = ?1 AND uid IN (SELECT value FROM json_each(?2))`, feedID, string(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		known[u] = true
	}
	return known, rows.Err()
}

// ItemIDsByUID maps the uids of a feed's live items to their ids. The
// scheduler uses it after a commit to queue the new items for extraction.
func (d *DB) ItemIDsByUID(ctx context.Context, feedID int64, uids []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(uids) == 0 {
		return out, nil
	}
	b, _ := json.Marshal(uids)
	rows, err := d.reader.QueryContext(ctx, `SELECT uid, id FROM items WHERE feed_id = ? AND uid IN (SELECT value FROM json_each(?))`, feedID, string(b))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var id int64
		if err := rows.Scan(&u, &id); err != nil {
			return nil, err
		}
		out[u] = id
	}
	return out, rows.Err()
}
