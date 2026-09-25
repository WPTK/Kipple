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
}

// GetFulltextItem loads a live item (a trimmed stub has no row here).
func (d *DB) GetFulltextItem(ctx context.Context, id int64) (it FulltextItem, ok bool, err error) {
	var mode sql.NullInt64
	var ua, html, ferr sql.NullString
	var row, words sql.NullInt64
	var priv, insecure, noH2 int
	err = d.reader.QueryRowContext(ctx, `SELECT i.id, i.feed_id, i.url, i.fulltext_mode, COALESCE(i.fulltext_mode, f.fulltext),
			f.user_agent, f.allow_private_net, f.allow_insecure_tls, f.disable_http2,
			ft.item_id, ft.content_html, ft.error, ft.word_count
		FROM items i JOIN feeds f ON f.id = i.feed_id LEFT JOIN item_fulltext ft ON ft.item_id = i.id WHERE i.id = ?`, id).
		Scan(&it.ID, &it.FeedID, &it.URL, &mode, &it.Effective, &ua, &priv, &insecure, &noH2, &row, &html, &ferr, &words)
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
}

// SaveFulltext upserts the item's item_fulltext row. A failure never replaces a
// stored success (the earlier content stays readable and Error is only
// reported to the caller).
func (d *DB) SaveFulltext(ctx context.Context, id, now int64, s FulltextSave) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if s.Error != "" {
			_, err := tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, extracted_at, error)
				SELECT ?1, ?2, ?3 WHERE EXISTS (SELECT 1 FROM items WHERE id = ?1)
				ON CONFLICT (item_id) DO UPDATE SET error = excluded.error, extracted_at = excluded.extracted_at
				WHERE item_fulltext.content_html IS NULL`, id, now, s.Error)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, content_html, content_text, word_count, image_url, source_url, extracted_at, error)
			SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, NULL WHERE EXISTS (SELECT 1 FROM items WHERE id = ?1)
			ON CONFLICT (item_id) DO UPDATE SET content_html = excluded.content_html, content_text = excluded.content_text,
				word_count = excluded.word_count, image_url = excluded.image_url, source_url = excluded.source_url,
				extracted_at = excluded.extracted_at, error = NULL`,
			id, s.HTML, s.Text, s.WordCount, nullStr(s.ImageURL), nullStr(s.SourceURL), now)
		return err
	})
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
