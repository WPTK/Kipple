package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
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

// searchCards runs a Query card list and reports whether it ran in partial-match (fallback) mode.
// Rank order keys on (rank, id) ascending (bm25: lower is better); date order on (sort_at, id)
// descending. The first page runs the exact expression and, when that finds nothing in scope,
// retries once with SearchQuery.FallbackMatch; the cursor remembers the mode so later pages stay
// in it.
func (d *DB) searchCards(ctx context.Context, q CardQuery, limit int) ([]Card, *Cursor, bool, error) {
	sq := ParseSearch(q.Query)
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
	var outer, order string
	var outerArgs []any
	if q.Rank {
		order = "r, id"
		if q.Cursor != nil {
			outer = " WHERE (r > ? OR (r = ? AND id > ?))"
			outerArgs = []any{q.Cursor.Rank, q.Cursor.Rank, q.Cursor.ID}
		}
	} else {
		order = dateOrder("", q.Oldest)
		if q.Cursor != nil {
			where = append(where, "(i.sort_at, i.id) "+keysetOp(q.Oldest)+" (?, ?)")
			args = append(args, q.Cursor.SortAt, q.Cursor.ID)
		}
	}
	sqlText := `SELECT id, feed_id, title, url, author, txt, image_url, published_at, sort_at, read, starred, word_count, origin, ftitle, muted_by, muted_name, snip, r FROM (
		SELECT i.id AS id, i.feed_id AS feed_id, i.title AS title, i.url AS url, i.author AS author,
			substr(COALESCE(c.content_text, ''), 1, 1200) AS txt, i.image_url AS image_url, i.published_at AS published_at,
			i.sort_at AS sort_at, i.read AS read, i.starred AS starred, i.word_count AS word_count, i.origin_title AS origin, (SELECT COALESCE(NULLIF(custom_title, ''), NULLIF(title, ''), url) FROM feeds WHERE id = i.feed_id) AS ftitle,
			i.muted_by AS muted_by, (SELECT name FROM filters WHERE id = i.muted_by) AS muted_name,
			snippet(items_fts, 2, '` + snipOpen + `', '` + snipClose + `', '…', 24) AS snip, items_fts.rank AS r
		FROM items_fts JOIN items i ON i.id = items_fts.rowid LEFT JOIN item_content c ON c.item_id = i.id
		WHERE ` + strings.Join(where, " AND ") + `)` + outer + ` ORDER BY ` + order + ` LIMIT ?`
	args = append(append(args, outerArgs...), limit+1)

	rows, err := d.reader.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, false, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()
	cards := []Card{}
	var lastRank float64
	for rows.Next() {
		var c Card
		var text, snip string
		var img, origin, ftitle, mutedName sql.NullString
		var mutedBy sql.NullInt64
		var read, starred int
		var rank float64
		if err := rows.Scan(&c.ID, &c.FeedID, &c.Title, &c.URL, &c.Author, &text, &img, &c.PublishedAt, &c.SortAt, &read, &starred, &c.WordCount, &origin, &ftitle, &mutedBy, &mutedName, &snip, &rank); err != nil {
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
		if len(cards) < limit {
			lastRank = rank
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	if len(cards) > limit {
		cards = cards[:limit]
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
