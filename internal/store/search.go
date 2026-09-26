package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// Full-text search over items_fts (design §2.4, §7.1). The query builder is in searchquery.go.

// snippet markers: control characters (stripped from user input and absent
// from plain text) stand in for <mark> until the text has been escaped.
const snipOpen, snipClose = "\x02", "\x03"

// snippetHTML escapes the plain-text snippet and turns the marker characters
// into <mark> tags, so the result is safe to render as HTML.
func snippetHTML(s string) string {
	s = html.EscapeString(s)
	s = strings.ReplaceAll(s, snipOpen, "<mark>")
	return strings.ReplaceAll(s, snipClose, "</mark>")
}

// searchScope holds the WHERE parts of a search that do not depend on the cursor or the match
// mode, so the zero-result probe and the page query see exactly the same rows.
func searchScope(q CardQuery) (where []string, args []any) {
	switch q.View {
	case "unread":
		where = append(where, "i.read = 0")
	case "starred":
		where = append(where, "i.starred = 1")
	case "all":
		where = append(where, "i.muted_by IS NULL") // search skips muted items unless view=muted
	case "muted":
		where = append(where, "i.muted_by IS NOT NULL")
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
	return where, args
}

// searchBudget bounds one search (probe, ranking pass and page decoration together). A search
// that needs longer is too broad for a reader UI: it answers ErrSearchTooBroad instead of holding
// a reader connection. A variable so tests can shrink it.
var searchBudget = 500 * time.Millisecond

// ErrSearchTooBroad is returned by a search that ran out of searchBudget.
var ErrSearchTooBroad = errors.New("store: search too broad")

// searchCards runs a Query card list and reports whether it ran in partial-match (fallback) mode.
// Rank order keys on (rank, id) ascending (bm25: lower is better); date order on (sort_at, id)
// descending. The first page runs the exact expression and, when that finds nothing in scope,
// retries once with SearchQuery.FallbackMatch; the cursor remembers the mode so later pages stay
// in it.
//
// The work is in two passes so its cost does not grow with the number of matches: the first
// selects only the page's ids (and, for relevance order, their rank, which needs bm25 for every
// match); the second computes snippet() and reads the text for those ids alone.
func (d *DB) searchCards(ctx context.Context, q CardQuery, limit int) ([]Card, *Cursor, bool, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, searchBudget)
	defer cancel()
	cards, cur, fb, err := d.searchCardsRun(ctx, q, limit)
	if err != nil && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, nil, false, ErrSearchTooBroad
	}
	return cards, cur, fb, err
}

func (d *DB) searchCardsRun(ctx context.Context, q CardQuery, limit int) ([]Card, *Cursor, bool, error) {
	sq := ParseSearch(q.Query, q.Typing)
	match, ok := sq.Match()
	if !ok {
		return []Card{}, nil, false, nil
	}
	scope, scopeArgs := searchScope(q)
	fallback := false
	if fb := sq.FallbackMatch(); q.Cursor != nil && q.Cursor.Fallback && fb != "" {
		match, fallback = fb, true
	} else if q.Cursor == nil && fb != "" && fb != match {
		probe := `SELECT 1 FROM items_fts JOIN items i ON i.id = items_fts.rowid WHERE ` +
			strings.Join(append([]string{"items_fts MATCH ?"}, scope...), " AND ") + ` LIMIT 1`
		var one int
		err := d.reader.QueryRowContext(ctx, probe, append([]any{match}, scopeArgs...)...).Scan(&one)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			match, fallback = fb, true
		case err != nil:
			return nil, nil, false, fmt.Errorf("store: search probe: %w", err)
		}
	}
	where := append([]string{"items_fts MATCH ?"}, scope...)
	args := append([]any{match}, scopeArgs...)
	var outer, order, rankCol string
	var outerArgs []any
	if q.Rank {
		rankCol = "items_fts.rank"
		order = "r, id"
		if q.Cursor != nil {
			outer = " WHERE (r > ? OR (r = ? AND id > ?))"
			outerArgs = []any{q.Cursor.Rank, q.Cursor.Rank, q.Cursor.ID}
		}
	} else {
		rankCol = "0.0" // date order never needs bm25
		order = dateOrder("", q.Oldest)
		if q.Cursor != nil {
			where = append(where, "(i.sort_at, i.id) "+keysetOp(q.Oldest)+" (?, ?)")
			args = append(args, q.Cursor.SortAt, q.Cursor.ID)
		}
	}
	// Pass 1: the ids of the page (one extra row says whether a next page exists).
	idSQL := `SELECT id, r FROM (SELECT i.id AS id, i.sort_at AS sort_at, ` + rankCol + ` AS r
		FROM items_fts JOIN items i ON i.id = items_fts.rowid
		WHERE ` + strings.Join(where, " AND ") + `)` + outer + ` ORDER BY ` + order + ` LIMIT ?`
	args = append(append(args, outerArgs...), limit+1)
	idRows, err := d.reader.QueryContext(ctx, idSQL, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("store: search: %w", err)
	}
	var ids []int64
	var ranks []float64
	for idRows.Next() {
		var id int64
		var r float64
		if err := idRows.Scan(&id, &r); err != nil {
			idRows.Close()
			return nil, nil, false, err
		}
		ids, ranks = append(ids, id), append(ranks, r)
	}
	if err := idRows.Err(); err != nil {
		idRows.Close()
		return nil, nil, false, fmt.Errorf("store: search: %w", err)
	}
	idRows.Close()
	if len(ids) == 0 {
		return []Card{}, nil, false, nil
	}
	more := len(ids) > limit
	if more {
		ids, ranks = ids[:limit], ranks[:limit]
	}

	// Pass 2: the page's rows, with snippets, in pass 1's order.
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args2 := []any{match}
	for _, id := range ids {
		args2 = append(args2, id)
	}
	sqlText := `SELECT i.id, i.feed_id, i.title, i.url, i.author,
			substr(COALESCE(c.content_text, ''), 1, 1200), i.image_url, i.published_at, i.sort_at, i.read, i.starred, i.word_count, i.origin_title,
			(SELECT COALESCE(NULLIF(custom_title, ''), NULLIF(title, ''), url) FROM feeds WHERE id = i.feed_id),
			i.muted_by, (SELECT name FROM filters WHERE id = i.muted_by),
			snippet(items_fts, 2, '` + snipOpen + `', '` + snipClose + `', '…', 24)
		FROM items_fts JOIN items i ON i.id = items_fts.rowid LEFT JOIN item_content c ON c.item_id = i.id
		WHERE items_fts MATCH ? AND i.id IN (` + marks + `)`
	rows, err := d.reader.QueryContext(ctx, sqlText, args2...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()
	byID := make(map[int64]Card, len(ids))
	for rows.Next() {
		var c Card
		var text, snip string
		var img, origin, ftitle, mutedName sql.NullString
		var mutedBy sql.NullInt64
		var read, starred int
		if err := rows.Scan(&c.ID, &c.FeedID, &c.Title, &c.URL, &c.Author, &text, &img, &c.PublishedAt, &c.SortAt, &read, &starred, &c.WordCount, &origin, &ftitle, &mutedBy, &mutedName, &snip); err != nil {
			return nil, nil, false, err
		}
		c.setSource(origin, ftitle)
		c.setMuted(mutedBy, mutedName)
		c.Excerpt = excerpt(text)
		if img.Valid && img.String != "" {
			c.Image = &img.String
		}
		c.Read, c.Starred = read == 1, starred == 1
		c.ReadingMinutes = readingMinutes(c.WordCount)
		c.Snippet = snippetHTML(snip)
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("store: search: %w", err)
	}
	cards := make([]Card, 0, len(ids))
	lastRank := 0.0
	for i, id := range ids {
		if c, ok := byID[id]; ok { // a row trimmed between the passes just drops out
			cards = append(cards, c)
			lastRank = ranks[i]
		}
	}
	if more && len(cards) > 0 {
		last := cards[len(cards)-1]
		return cards, &Cursor{SortAt: last.SortAt, ID: last.ID, Rank: lastRank, ByRank: q.Rank, Asc: q.Oldest, Fallback: fallback}, fallback, nil
	}
	return cards, nil, fallback && len(cards) > 0, nil
}

// RebuildFTS repairs the search index from its content view (design §2.4).
// It runs on the writer, so it is bounded by the 10 s write deadline.
func (d *DB) RebuildFTS(ctx context.Context) error {
	return d.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO items_fts(items_fts) VALUES('rebuild')")
		return err
	})
}

func formatRank(r float64) string { return strconv.FormatFloat(r, 'g', -1, 64) }
