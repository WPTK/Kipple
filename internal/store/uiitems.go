package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Web UI item reads (design §7.1). All of them run on the reader pool.

const (
	// ExcerptChars is the card excerpt length cap.
	ExcerptChars = 280
	// CardMaxLimit is the largest page a card list returns.
	CardMaxLimit = 100
	// CardDefaultLimit is the page size when the client sends none.
	CardDefaultLimit = 50
	wordsPerMinute   = 230
	minReadingWords  = 100
)

// Card is one item in a list (design §7.1). Ids are strings in JSON.
type Card struct {
	ID             int64   `json:"id,string"`
	FeedID         int64   `json:"feed_id,string"`
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	Author         string  `json:"author"`
	Excerpt        string  `json:"excerpt"`
	Image          *string `json:"image"`
	PublishedAt    int64   `json:"published_at"`
	SortAt         int64   `json:"sort_at"`
	Read           bool    `json:"read"`
	Starred        bool    `json:"starred"`
	WordCount      int64   `json:"word_count"`
	ReadingMinutes *int64  `json:"reading_minutes"`
	Snippet        string  `json:"snippet,omitempty"`
}

// readingMinutes is ceil(words/230), nil under 100 words.
func readingMinutes(words int64) *int64 {
	if words < minReadingWords {
		return nil
	}
	m := (words + wordsPerMinute - 1) / wordsPerMinute
	return &m
}

// excerpt collapses whitespace and cuts to ExcerptChars runes.
func excerpt(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if r := []rune(s); len(r) > ExcerptChars {
		s = string(r[:ExcerptChars])
	}
	return s
}

// CardQuery selects a page of cards. View is "unread", "all" or "starred".
// IDs, when non-empty, overrides everything else (SSE catch-up).
type CardQuery struct {
	View     string
	FeedID   int64
	FolderID int64
	Cursor   *Cursor
	Limit    int
	IDs      []int64
}

// Cursor is the keyset position (sort_at, id) of the last card served.
type Cursor struct {
	SortAt int64
	ID     int64
}

// Encode renders the opaque cursor: base64url of "sort_at.id".
func (c Cursor) Encode() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.SortAt, 10) + "." + strconv.FormatInt(c.ID, 10)))
}

// ParseCursor is the inverse of Encode.
func ParseCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errors.New("store: bad cursor")
	}
	a, b, ok := strings.Cut(string(raw), ".")
	if !ok {
		return Cursor{}, errors.New("store: bad cursor")
	}
	sortAt, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil {
		return Cursor{}, errors.New("store: bad cursor")
	}
	return Cursor{sortAt, id}, nil
}

const cardCols = `i.id, i.feed_id, i.title, i.url, i.author, substr(COALESCE(c.content_text, ''), 1, 1200), i.image_url,
	i.published_at, i.sort_at, i.read, i.starred, i.word_count`

func scanCard(rows interface{ Scan(...any) error }) (Card, error) {
	var c Card
	var text string
	var img sql.NullString
	var read, starred int
	if err := rows.Scan(&c.ID, &c.FeedID, &c.Title, &c.URL, &c.Author, &text, &img, &c.PublishedAt, &c.SortAt, &read, &starred, &c.WordCount); err != nil {
		return c, err
	}
	c.Excerpt = excerpt(text)
	if img.Valid && img.String != "" {
		c.Image = &img.String // TODO(phase 2 step 3): rewrite through the signed image proxy (design §7.4)
	}
	c.Read, c.Starred = read == 1, starred == 1
	c.ReadingMinutes = readingMinutes(c.WordCount)
	return c, nil
}

// ListCards returns newest-first cards (sort_at DESC, id DESC) and the cursor of
// the next page, or nil when there is none. An ids query returns those cards
// that still exist in items, with no cursor.
func (d *DB) ListCards(ctx context.Context, q CardQuery) ([]Card, *Cursor, error) {
	var where []string
	var args []any
	limit := q.Limit
	if len(q.IDs) > 0 {
		js, err := json.Marshal(q.IDs)
		if err != nil {
			return nil, nil, err
		}
		where = append(where, "i.id IN (SELECT value FROM json_each(?))")
		args = append(args, string(js))
		limit = len(q.IDs)
	} else {
		if limit <= 0 {
			limit = CardDefaultLimit
		}
		limit = min(limit, CardMaxLimit)
		switch q.View {
		case "unread":
			where = append(where, "i.read = 0") // literal, so the partial indexes are usable
		case "starred":
			where = append(where, "i.starred = 1")
		}
		if q.FeedID != 0 {
			where = append(where, "i.feed_id = ?")
			args = append(args, q.FeedID)
		}
		if q.FolderID != 0 {
			where = append(where, "i.feed_id IN (SELECT id FROM feeds WHERE folder_id = ?)")
			args = append(args, q.FolderID)
		}
		if q.Cursor != nil {
			where = append(where, "(i.sort_at, i.id) < (?, ?)")
			args = append(args, q.Cursor.SortAt, q.Cursor.ID)
		}
	}
	sqlText := "SELECT " + cardCols + " FROM items i LEFT JOIN item_content c ON c.item_id = i.id"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY i.sort_at DESC, i.id DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := d.reader.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cards := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, nil, err
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(cards) > limit {
		cards = cards[:limit]
		if len(q.IDs) == 0 {
			last := cards[len(cards)-1]
			return cards, &Cursor{last.SortAt, last.ID}, nil
		}
	}
	return cards, nil, nil
}

// ItemFeed is the feed summary embedded in an item.
type ItemFeed struct {
	ID      int64  `json:"id,string"`
	Title   string `json:"title"`
	SiteURL string `json:"site_url"`
}

// Fulltext is an item's extraction state (design §7.5).
type Fulltext struct {
	Mode      *int    `json:"mode"`      // items.fulltext_mode: 1, 0 or null (follow the feed)
	Effective int     `json:"effective"` // COALESCE(mode, feeds.fulltext)
	Available bool    `json:"available"` // a successful extraction is stored
	Error     *string `json:"error"`
}

// ItemDetail is GET /api/items/{id}. Trimmed is true for a restore stub.
type ItemDetail struct {
	Card
	ContentHTML string          `json:"content_html"`
	Fulltext    Fulltext        `json:"fulltext"`
	Enclosures  json.RawMessage `json:"enclosures"`
	Feed        ItemFeed        `json:"feed"`
	Trimmed     bool            `json:"trimmed"`
}

// GetItem loads an item, or the restore stub of a ledger id trimmed within
// retention.restore_days (older stubs cannot be restored, so they are absent).
// ok is false when neither exists.
func (d *DB) GetItem(ctx context.Context, id, now int64) (det ItemDetail, ok bool, err error) {
	var text string
	var img, enc, ftHTML, ftErr sql.NullString
	var read, starred int
	var mode sql.NullInt64
	var ftEff int
	var ftRow sql.NullInt64
	err = d.reader.QueryRowContext(ctx, `SELECT `+cardCols+`, COALESCE(c.content_html, ''), c.enclosures_json,
			f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.site_url,
			i.fulltext_mode, COALESCE(i.fulltext_mode, f.fulltext), ft.item_id, ft.content_html, ft.error
		FROM items i LEFT JOIN item_content c ON c.item_id = i.id JOIN feeds f ON f.id = i.feed_id
		LEFT JOIN item_fulltext ft ON ft.item_id = i.id WHERE i.id = ?`, id).
		Scan(&det.ID, &det.FeedID, &det.Title, &det.URL, &det.Author, &text, &img, &det.PublishedAt, &det.SortAt, &read, &starred, &det.WordCount,
			&det.ContentHTML, &enc, &det.Feed.ID, &det.Feed.Title, &det.Feed.SiteURL, &mode, &ftEff, &ftRow, &ftHTML, &ftErr)
	switch {
	case err == nil:
		det.Read, det.Starred = read == 1, starred == 1
		det.Fulltext.Effective = ftEff
		if mode.Valid {
			m := int(mode.Int64)
			det.Fulltext.Mode = &m
		}
		det.Fulltext.Available = ftRow.Valid && ftHTML.Valid
		if ftErr.Valid {
			det.Fulltext.Error = &ftErr.String
		}
		if ftEff == 1 && ftHTML.Valid {
			det.ContentHTML = ftHTML.String
		}
	case errors.Is(err, sql.ErrNoRows):
		return d.getStub(ctx, id, now)
	default:
		return det, false, err
	}
	det.finish(text, img, enc)
	return det, true, nil
}

func (det *ItemDetail) finish(text string, img, enc sql.NullString) {
	det.Excerpt = excerpt(text)
	if img.Valid && img.String != "" {
		det.Image = &img.String // TODO(phase 2 step 3): image proxy rewrite (design §7.4)
	}
	det.ReadingMinutes = readingMinutes(det.WordCount)
	det.Enclosures = json.RawMessage("[]")
	if enc.Valid && json.Valid([]byte(enc.String)) {
		det.Enclosures = json.RawMessage(enc.String)
	}
}

func (d *DB) getStub(ctx context.Context, id, now int64) (det ItemDetail, ok bool, err error) {
	cutoff := now - int64(LoadFetchSettings(ctx, d.reader).RestoreDays)*86400
	var text string
	var img, enc sql.NullString
	var read int
	var mode sql.NullInt64
	err = d.reader.QueryRowContext(ctx, `SELECT t.id, t.feed_id, c.title, c.url, c.author, substr(c.content_text, 1, 1200), c.image_url,
			c.published_at, c.sort_at, t.read, c.word_count, c.content_html, c.enclosures_json,
			f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.site_url, c.fulltext_mode
		FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id JOIN feeds f ON f.id = t.feed_id
		WHERE t.id = ? AND t.trimmed_at >= ?`, id, cutoff).
		Scan(&det.ID, &det.FeedID, &det.Title, &det.URL, &det.Author, &text, &img, &det.PublishedAt, &det.SortAt, &read, &det.WordCount,
			&det.ContentHTML, &enc, &det.Feed.ID, &det.Feed.Title, &det.Feed.SiteURL, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return det, false, nil
	}
	if err != nil {
		return det, false, err
	}
	det.Read, det.Trimmed = read == 1, true
	if mode.Valid {
		m := int(mode.Int64)
		det.Fulltext.Mode = &m
		det.Fulltext.Effective = int(mode.Int64)
	}
	det.finish(text, img, enc)
	return det, true, nil
}

// ItemKnown reports whether id is an item or a restorable stub.
func (d *DB) ItemKnown(ctx context.Context, id, now int64) (bool, error) {
	var n int
	cutoff := now - int64(LoadFetchSettings(ctx, d.reader).RestoreDays)*86400
	err := d.reader.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM items WHERE id = ?1)
		OR EXISTS (SELECT 1 FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id WHERE t.id = ?1 AND t.trimmed_at >= ?2)`, id, cutoff).Scan(&n)
	return n == 1, err
}

// MarkScopeRead marks the unread items inside scope with id <= maxID read and
// returns which ids changed; the scope's ledger rows are marked read too
// (design §7.1). Like the other read-state functions it has no stats side effect.
func MarkScopeRead(ctx context.Context, tx *sql.Tx, scope MarkScope, maxID, now int64) (StateResult, error) {
	where, feedWhere := "", ""
	args := []any{sql.Named("max", maxID)}
	if scope.Starred {
		where = " AND starred = 1"
	}
	switch {
	case scope.FeedID != 0:
		feedWhere = " AND feed_id = :feed"
		args = append(args, sql.Named("feed", scope.FeedID))
	case scope.FolderID != 0:
		feedWhere = " AND feed_id IN (SELECT id FROM feeds WHERE folder_id = :folder)"
		args = append(args, sql.Named("folder", scope.FolderID))
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, feed_id FROM items WHERE read = 0 AND id <= :max"+where+feedWhere, args...)
	if err != nil {
		return StateResult{}, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return StateResult{}, err
	}
	res, err := SetRead(ctx, tx, ids, true, now)
	if err != nil {
		return res, err
	}
	if !scope.Starred {
		if _, err := tx.ExecContext(ctx, "UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :max"+feedWhere, args...); err != nil {
			return res, fmt.Errorf("store: mark scope ledger: %w", err)
		}
	}
	return res, nil
}
