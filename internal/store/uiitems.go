package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	// OriginTitle is the title of the feed an item was subscribed under when that
	// feed was deleted (starred items live on in the archive feed); Source is what
	// to show as the item's source: COALESCE(origin_title, its feed's title).
	OriginTitle *string `json:"origin_title"`
	Source      string  `json:"source"`
}

// setSource fills Source from the raw origin_title and feed title columns.
func (c *Card) setSource(origin sql.NullString, feedTitle sql.NullString) {
	if origin.Valid && origin.String != "" {
		c.OriginTitle = &origin.String
		c.Source = origin.String
		return
	}
	c.Source = feedTitle.String
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
	// Query is raw user search text (turned into a safe FTS5 expression by
	// BuildFTSQuery); Rank orders results by relevance instead of date.
	Query string
	Rank  bool
	// Oldest lists ascending (sort_at ASC, id ASC) instead of newest first.
	Oldest bool
	// MinMinutes and MaxMinutes (0 = unset) keep items whose reading time
	// (ceil(words/230)) is at least/at most that many minutes; items with no
	// extracted text (word_count 0) never match either filter.
	MinMinutes int
	MaxMinutes int
}

// ReadingWhere is the SQL for the reading-time filters on column col
// (word_count), or "" when neither is set. Minutes m means word_count in
// ((m-1)*230, m*230].
func ReadingWhere(col string, minM, maxM int) (string, []any) {
	if minM <= 0 && maxM <= 0 {
		return "", nil
	}
	sqlText, args := col+" > 0", []any(nil)
	if minM > 1 {
		sqlText += " AND " + col + " > ?"
		args = append(args, int64(minM-1)*wordsPerMinute)
	}
	if maxM > 0 {
		sqlText += " AND " + col + " <= ?"
		args = append(args, int64(maxM)*wordsPerMinute)
	}
	return sqlText, args
}

// keysetOp is the row-value comparison that selects the rows after a cursor.
func keysetOp(oldest bool) string {
	if oldest {
		return ">"
	}
	return "<"
}

func dateOrder(prefix string, oldest bool) string {
	if oldest {
		return prefix + "sort_at ASC, " + prefix + "id ASC"
	}
	return prefix + "sort_at DESC, " + prefix + "id DESC"
}

// Cursor is the keyset position (sort_at, id) of the last card served.
// A relevance cursor (ByRank) keys on (Rank, ID) instead.
type Cursor struct {
	SortAt int64
	ID     int64
	Rank   float64
	ByRank bool
	Asc    bool // an oldest-first cursor: (sort_at, id) ascending
}

// Encode renders the opaque cursor: base64url of "sort_at.id", or "r<rank>|id"
// for a relevance cursor.
func (c Cursor) Encode() string {
	if c.ByRank {
		return base64.RawURLEncoding.EncodeToString([]byte("r" + formatRank(c.Rank) + "|" + strconv.FormatInt(c.ID, 10)))
	}
	tag := ""
	if c.Asc {
		tag = "a"
	}
	return base64.RawURLEncoding.EncodeToString([]byte(tag + strconv.FormatInt(c.SortAt, 10) + "." + strconv.FormatInt(c.ID, 10)))
}

// ParseCursor is the inverse of Encode.
func ParseCursor(s string) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, errors.New("store: bad cursor")
	}
	if rest, isRank := strings.CutPrefix(string(raw), "r"); isRank {
		a, b, ok := strings.Cut(rest, "|")
		rank, err1 := strconv.ParseFloat(a, 64)
		id, err2 := strconv.ParseInt(b, 10, 64)
		if !ok || err1 != nil || err2 != nil || math.IsNaN(rank) || math.IsInf(rank, 0) {
			return Cursor{}, errors.New("store: bad cursor")
		}
		return Cursor{ID: id, Rank: rank, ByRank: true}, nil
	}
	body, asc := strings.CutPrefix(string(raw), "a")
	a, b, ok := strings.Cut(body, ".")
	if !ok {
		return Cursor{}, errors.New("store: bad cursor")
	}
	sortAt, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil {
		return Cursor{}, errors.New("store: bad cursor")
	}
	return Cursor{SortAt: sortAt, ID: id, Asc: asc}, nil
}

const cardCols = `i.id, i.feed_id, i.title, i.url, i.author, substr(COALESCE(c.content_text, ''), 1, 1200), i.image_url,
	i.published_at, i.sort_at, i.read, i.starred, i.word_count,
	i.origin_title, (SELECT COALESCE(NULLIF(custom_title, ''), NULLIF(title, ''), url) FROM feeds WHERE id = i.feed_id)`

func scanCard(rows interface{ Scan(...any) error }) (Card, error) {
	var c Card
	var text string
	var img, origin, feedTitle sql.NullString
	var read, starred int
	if err := rows.Scan(&c.ID, &c.FeedID, &c.Title, &c.URL, &c.Author, &text, &img, &c.PublishedAt, &c.SortAt, &read, &starred, &c.WordCount, &origin, &feedTitle); err != nil {
		return c, err
	}
	c.setSource(origin, feedTitle)
	c.Excerpt = excerpt(text)
	if img.Valid && img.String != "" {
		c.Image = &img.String // raw here; internal/api rewrites it through the image proxy at serve time (design §7.4)
	}
	c.Read, c.Starred = read == 1, starred == 1
	c.ReadingMinutes = readingMinutes(c.WordCount)
	return c, nil
}

// ListCards returns newest-first cards (sort_at DESC, id DESC) and the cursor of
// the next page, or nil when there is none. An ids query returns those cards
// that still exist in items, with no cursor.
func (d *DB) ListCards(ctx context.Context, q CardQuery) ([]Card, *Cursor, error) {
	if q.Query != "" && len(q.IDs) == 0 {
		limit := q.Limit
		if limit <= 0 {
			limit = CardDefaultLimit
		}
		return d.searchCards(ctx, q, min(limit, CardMaxLimit))
	}
	sqlText, args, limit, err := listCardsSQL(q)
	if err != nil {
		return nil, nil, err
	}
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
			return cards, &Cursor{SortAt: last.SortAt, ID: last.ID, Asc: q.Oldest}, nil
		}
	}
	return cards, nil, nil
}

// listCardsSQL builds the non-search card query and returns the page limit.
func listCardsSQL(q CardQuery) (string, []any, int, error) {
	var where []string
	var args []any
	limit := q.Limit
	if len(q.IDs) > 0 {
		js, err := json.Marshal(q.IDs)
		if err != nil {
			return "", nil, 0, err
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
		if w, a := ReadingWhere("i.word_count", q.MinMinutes, q.MaxMinutes); w != "" {
			where = append(where, w)
			args = append(args, a...)
		}
		if q.Cursor != nil {
			where = append(where, "(i.sort_at, i.id) "+keysetOp(q.Oldest)+" (?, ?)")
			args = append(args, q.Cursor.SortAt, q.Cursor.ID)
		}
	}
	sqlText := "SELECT " + cardCols + " FROM items i LEFT JOIN item_content c ON c.item_id = i.id"
	if len(where) > 0 {
		sqlText += " WHERE " + strings.Join(where, " AND ")
	}
	sqlText += " ORDER BY " + dateOrder("i.", q.Oldest && len(q.IDs) == 0) + " LIMIT ?"
	args = append(args, limit+1)
	return sqlText, args, limit, nil
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
	Effective int     `json:"effective"` // EffectiveFulltext(mode, feeds.fulltext, fetch.fulltext_all)
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
	var img, enc, ftHTML, ftErr, origin, feedTitle sql.NullString
	var read, starred int
	var mode sql.NullInt64
	var ftEff int
	var ftRow sql.NullInt64
	err = d.reader.QueryRowContext(ctx, `SELECT `+cardCols+`, COALESCE(c.content_html, ''), c.enclosures_json,
			f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.site_url,
			i.fulltext_mode, `+FulltextModeSQL("i.fulltext_mode", "f.fulltext", d.FulltextAll(ctx))+`, ft.item_id, ft.content_html, ft.error
		FROM items i LEFT JOIN item_content c ON c.item_id = i.id JOIN feeds f ON f.id = i.feed_id
		LEFT JOIN item_fulltext ft ON ft.item_id = i.id WHERE i.id = ?`, id).
		Scan(&det.ID, &det.FeedID, &det.Title, &det.URL, &det.Author, &text, &img, &det.PublishedAt, &det.SortAt, &read, &starred, &det.WordCount, &origin, &feedTitle,
			&det.ContentHTML, &enc, &det.Feed.ID, &det.Feed.Title, &det.Feed.SiteURL, &mode, &ftEff, &ftRow, &ftHTML, &ftErr)
	switch {
	case err == nil:
		det.setSource(origin, feedTitle)
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
		det.Image = &img.String // raw here; internal/api rewrites it (design §7.4)
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
	var img, enc, origin sql.NullString
	var read int
	var feedFT int
	var mode sql.NullInt64
	err = d.reader.QueryRowContext(ctx, `SELECT t.id, t.feed_id, c.title, c.url, c.author, substr(c.content_text, 1, 1200), c.image_url,
			c.published_at, c.sort_at, t.read, c.word_count, c.origin_title, c.content_html, c.enclosures_json,
			f.id, COALESCE(NULLIF(f.custom_title, ''), NULLIF(f.title, ''), f.url), f.site_url, c.fulltext_mode, f.fulltext
		FROM trimmed_items t JOIN trimmed_content c ON c.id = t.id JOIN feeds f ON f.id = t.feed_id
		WHERE t.id = ? AND t.trimmed_at >= ?`, id, cutoff).
		Scan(&det.ID, &det.FeedID, &det.Title, &det.URL, &det.Author, &text, &img, &det.PublishedAt, &det.SortAt, &read, &det.WordCount, &origin,
			&det.ContentHTML, &enc, &det.Feed.ID, &det.Feed.Title, &det.Feed.SiteURL, &mode, &feedFT)
	if errors.Is(err, sql.ErrNoRows) {
		return det, false, nil
	}
	if err != nil {
		return det, false, err
	}
	det.setSource(origin, sql.NullString{String: det.Feed.Title, Valid: true})
	det.Read, det.Trimmed = read == 1, true
	if mode.Valid {
		m := int(mode.Int64)
		det.Fulltext.Mode = &m
	}
	var stubMode *int
	if mode.Valid {
		m := int(mode.Int64)
		stubMode = &m
	}
	det.Fulltext.Effective = EffectiveFulltext(stubMode, feedFT == 1, d.FulltextAll(ctx))
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

// Bound restricts a mark to one side of an anchor in the list's own
// (sort_at, id) order (design §7.1). The anchor is excluded unless Inclusive.
type Bound struct {
	Oldest    bool // the list is oldest-first
	Above     bool // rows shown before the anchor; false = after it
	SortAt    int64
	ID        int64
	Inclusive bool
}

// op is the comparison that selects the bounded rows. In a newest-first list
// "above" means a larger key; oldest-first flips it.
func (b Bound) op() string {
	greater := b.Above != b.Oldest
	switch {
	case greater && b.Inclusive:
		return ">="
	case greater:
		return ">"
	case b.Inclusive:
		return "<="
	}
	return "<"
}

// MarkFilter narrows a scope to what the list behind it showed: search text,
// reading-time limits and an anchor bound. The zero value adds nothing.
type MarkFilter struct {
	Query      string
	MinMinutes int
	MaxMinutes int
	Bound      *Bound
}

func (f MarkFilter) any() bool {
	return f.Query != "" || f.MinMinutes > 0 || f.MaxMinutes > 0 || f.Bound != nil
}

// MarkScopeRead marks the unread items inside scope with id <= maxID read and
// returns which ids changed. Without a filter the scope's ledger rows are
// marked read too (design §7.1); a filtered scope never touches the ledger,
// which has no sort_at, text or word count to test. Like the other read-state
// functions it has no stats side effect.
func MarkScopeRead(ctx context.Context, tx *sql.Tx, scope MarkScope, f MarkFilter, maxID, now int64) (StateResult, error) {
	sel, args, feedWhere, base, ok := markSelectSQL(scope, f, maxID)
	if !ok {
		return StateResult{}, nil // a search with no usable terms matches nothing
	}
	rows, err := tx.QueryContext(ctx, sel, args...)
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
	if !scope.Starred && !f.any() {
		lrows, err := tx.QueryContext(ctx, "UPDATE trimmed_items SET read = 1 WHERE read = 0 AND id <= :max"+feedWhere+" RETURNING id, feed_id", base...)
		if err != nil {
			return res, fmt.Errorf("store: mark scope ledger: %w", err)
		}
		if res.LedgerRead, err = scanIDs(lrows); err != nil {
			return res, fmt.Errorf("store: mark scope ledger: %w", err)
		}
	}
	return res, nil
}

// markSelectSQL builds the query that picks the unread ids to mark. base holds
// the arguments the ledger statement shares (max and the feed/folder target).
func markSelectSQL(scope MarkScope, f MarkFilter, maxID int64) (sel string, args []any, feedWhere string, base []any, ok bool) {
	where := ""
	args = []any{sql.Named("max", maxID)}
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
	base = append([]any(nil), args...)
	extra := ""
	if f.Query != "" {
		match, ok := BuildFTSQuery(f.Query)
		if !ok {
			return "", nil, "", nil, false
		}
		extra += " AND id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH :match)"
		args = append(args, sql.Named("match", match))
	}
	if w, a := ReadingWhere("word_count", f.MinMinutes, f.MaxMinutes); w != "" {
		for i, v := range a {
			w = strings.Replace(w, "?", fmt.Sprintf(":wc%d", i), 1)
			args = append(args, sql.Named(fmt.Sprintf("wc%d", i), v))
		}
		extra += " AND " + w
	}
	if b := f.Bound; b != nil {
		extra += " AND (sort_at, id) " + b.op() + " (:bsort, :bid)"
		args = append(args, sql.Named("bsort", b.SortAt), sql.Named("bid", b.ID))
	}
	return "SELECT id, feed_id FROM items WHERE read = 0 AND id <= :max" + where + feedWhere + extra, args, feedWhere, base, true
}
