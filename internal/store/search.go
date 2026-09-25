package store

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"strconv"
	"strings"
	"unicode"
)

// Full-text search over items_fts (design §2.4, §7.1).

const (
	maxSearchTokens   = 16
	maxSearchTokenLen = 64 // runes
	maxSearchInput    = 512
	// snippet markers: control characters (stripped from user input and absent
	// from plain text) stand in for <mark> until the text has been escaped.
	snipOpen, snipClose = "\x02", "\x03"
)

// BuildFTSQuery turns user text into an FTS5 MATCH expression that can never
// contain FTS syntax: every token is a double-quoted phrase (embedded quotes
// doubled), a trailing '*' stays outside the quotes for prefix search, tokens
// are ANDed by juxtaposition. Column filters ("title:x"), NEAR, NOT, '^' and
// parentheses are all inert inside a quoted phrase. Tokens with no letter or
// digit are dropped (the tokenizer would turn them into an empty phrase).
// ok is false when nothing searchable remains.
func BuildFTSQuery(raw string) (match string, ok bool) {
	if len(raw) > maxSearchInput {
		raw = raw[:maxSearchInput]
	}
	clean := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, raw)
	var out []string
	for _, tok := range strings.Fields(clean) {
		prefix := strings.HasSuffix(tok, "*")
		tok = strings.TrimRight(tok, "*")
		if !strings.ContainsFunc(tok, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		if r := []rune(tok); len(r) > maxSearchTokenLen {
			tok = string(r[:maxSearchTokenLen])
		}
		q := `"` + strings.ReplaceAll(tok, `"`, `""`) + `"`
		if prefix {
			q += "*"
		}
		out = append(out, q)
		if len(out) == maxSearchTokens {
			break
		}
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, " "), true
}

// snippetHTML escapes the plain-text snippet and turns the marker characters
// into <mark> tags, so the result is safe to render as HTML.
func snippetHTML(s string) string {
	s = html.EscapeString(s)
	s = strings.ReplaceAll(s, snipOpen, "<mark>")
	return strings.ReplaceAll(s, snipClose, "</mark>")
}

// searchCards runs a Query card list. Rank order keys on (rank, id) ascending
// (bm25: lower is better); date order on (sort_at, id) descending.
func (d *DB) searchCards(ctx context.Context, q CardQuery, limit int) ([]Card, *Cursor, error) {
	match, ok := BuildFTSQuery(q.Query)
	if !ok {
		return []Card{}, nil, nil
	}
	where := []string{"items_fts MATCH ?"}
	args := []any{match}
	switch q.View {
	case "unread":
		where = append(where, "i.read = 0")
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
	var outer, order string
	var outerArgs []any
	if q.Rank {
		order = "r, id"
		if q.Cursor != nil {
			outer = " WHERE (r > ? OR (r = ? AND id > ?))"
			outerArgs = []any{q.Cursor.Rank, q.Cursor.Rank, q.Cursor.ID}
		}
	} else {
		order = "sort_at DESC, id DESC"
		if q.Cursor != nil {
			where = append(where, "(i.sort_at, i.id) < (?, ?)")
			args = append(args, q.Cursor.SortAt, q.Cursor.ID)
		}
	}
	sqlText := `SELECT id, feed_id, title, url, author, txt, image_url, published_at, sort_at, read, starred, word_count, origin, ftitle, snip, r FROM (
		SELECT i.id AS id, i.feed_id AS feed_id, i.title AS title, i.url AS url, i.author AS author,
			substr(COALESCE(c.content_text, ''), 1, 1200) AS txt, i.image_url AS image_url, i.published_at AS published_at,
			i.sort_at AS sort_at, i.read AS read, i.starred AS starred, i.word_count AS word_count, i.origin_title AS origin, (SELECT COALESCE(NULLIF(custom_title, ''), NULLIF(title, ''), url) FROM feeds WHERE id = i.feed_id) AS ftitle,
			snippet(items_fts, 2, '` + snipOpen + `', '` + snipClose + `', '…', 24) AS snip, items_fts.rank AS r
		FROM items_fts JOIN items i ON i.id = items_fts.rowid LEFT JOIN item_content c ON c.item_id = i.id
		WHERE ` + strings.Join(where, " AND ") + `)` + outer + ` ORDER BY ` + order + ` LIMIT ?`
	args = append(append(args, outerArgs...), limit+1)

	rows, err := d.reader.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("store: search: %w", err)
	}
	defer rows.Close()
	cards := []Card{}
	var lastRank float64
	for rows.Next() {
		var c Card
		var text, snip string
		var img, origin, ftitle sql.NullString
		var read, starred int
		var rank float64
		if err := rows.Scan(&c.ID, &c.FeedID, &c.Title, &c.URL, &c.Author, &text, &img, &c.PublishedAt, &c.SortAt, &read, &starred, &c.WordCount, &origin, &ftitle, &snip, &rank); err != nil {
			return nil, nil, err
		}
		c.setSource(origin, ftitle)
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
		return nil, nil, err
	}
	if len(cards) > limit {
		cards = cards[:limit]
		last := cards[len(cards)-1]
		return cards, &Cursor{SortAt: last.SortAt, ID: last.ID, Rank: lastRank, ByRank: q.Rank}, nil
	}
	return cards, nil, nil
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
