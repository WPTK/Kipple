package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAscCursorRoundTrip(t *testing.T) {
	c := Cursor{SortAt: 77, ID: 99, Asc: true}
	back, err := ParseCursor(c.Encode())
	require.NoError(t, err)
	require.Equal(t, c, back)
	desc := Cursor{SortAt: 77, ID: 99}
	require.NotEqual(t, c.Encode(), desc.Encode())
	back, err = ParseCursor(desc.Encode())
	require.NoError(t, err)
	require.False(t, back.Asc)
	require.False(t, back.ByRank)
}

func TestBoundOperatorTable(t *testing.T) {
	// order x side x inclusive: "above" is a larger key in a newest-first list
	// and a smaller one in an oldest-first list.
	cases := []struct {
		oldest, above, incl bool
		want                string
	}{
		{false, true, false, ">"}, {false, true, true, ">="},
		{false, false, false, "<"}, {false, false, true, "<="},
		{true, true, false, "<"}, {true, true, true, "<="},
		{true, false, false, ">"}, {true, false, true, ">="},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Bound{Oldest: c.oldest, Above: c.above, Inclusive: c.incl}.op(), "%+v", c)
	}
}

func TestReadingWhere(t *testing.T) {
	w, a := ReadingWhere("w", 0, 0)
	require.Empty(t, w)
	require.Empty(t, a)
	w, a = ReadingWhere("w", 1, 0)
	require.Equal(t, "w > 0", w)
	require.Empty(t, a)
	w, a = ReadingWhere("w", 3, 5)
	require.Equal(t, "w > 0 AND w > ? AND w <= ?", w)
	require.Equal(t, []any{int64(460), int64(1150)}, a)
}

// The ascending keyset scans the same indexes as the descending one, for every
// list shape and for the bounded mark query, fresh and after ANALYZE.
func TestOrderQueryPlans(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO folders (id, name, position) VALUES (7, 'F', 1) ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
		for f := 1; f <= 3; f++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host, folder_id) VALUES (?,?,?,7)`,
				fmt.Sprintf("https://a/%d", f), fmt.Sprintf("a/%d", f), "a"); err != nil {
				return err
			}
		}
		for i := 0; i < 900; i++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, muted_by)
				VALUES (?,?,?,?,?,?,?,?,'c','t',CASE WHEN ?9 THEN 5 END)`, 1_700_000_000_000_000+int64(i)*1000, 1+i%3, i%4/3, b2i(i%50 == 0), i, i/2, i*10, fmt.Sprintf("u%d", i), i%37 == 0 && i%50 != 0); err != nil {
				return err
			}
		}
		return nil
	}))
	plan := func(q string, args []any) string {
		rows, err := db.Reader().Query("EXPLAIN QUERY PLAN "+q, args...)
		require.NoError(t, err)
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			b.WriteString(detail + "\n")
		}
		return b.String()
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			_, err := db.writer.ExecContext(ctx, "ANALYZE")
			require.NoError(t, err)
		}
		for _, oldest := range []bool{false, true} {
			cur := &Cursor{SortAt: 100, ID: 1_700_000_000_100_000, Asc: oldest}
			for _, tc := range []struct {
				name string
				q    CardQuery
				idx  string
			}{
				{"unread", CardQuery{View: "unread"}, "idx_items_unread_sort"},
				{"all", CardQuery{View: "all"}, "idx_items_sort"},
				{"feed", CardQuery{View: "all", FeedID: 2}, "idx_items_feed_sort"},
				{"starred", CardQuery{View: "starred"}, "idx_items_"},
				{"muted", CardQuery{View: "muted"}, "idx_items_muted"},
				{"muted in a feed", CardQuery{View: "muted", FeedID: 2}, "idx_items_"},
				{"reading time", CardQuery{View: "unread", MinMinutes: 2, MaxMinutes: 9}, "idx_items_unread_sort"},
			} {
				q := tc.q
				q.Oldest, q.Cursor, q.Limit = oldest, cur, 30
				sqlText, args, _, err := listCardsSQL(q)
				require.NoError(t, err)
				// The page is a deferred join: the id pick walks the view's index in keyset order
				// (no sort, no full scan), and the outer query only looks its rows up by id and
				// sorts those (at most limit+1).
				const in = " WHERE i.id IN ("
				pick := sqlText[strings.Index(sqlText, in)+len(in) : strings.LastIndex(sqlText, ") ORDER BY ")]
				require.True(t, strings.HasPrefix(pick, "SELECT i.id FROM items i WHERE "), pick)
				p := plan(pick, args)
				label := fmt.Sprintf("%s oldest=%v analyze=%v\n%s", tc.name, oldest, analyze, p)
				require.Contains(t, p, tc.idx, label)
				require.NotContains(t, p, "USE TEMP B-TREE FOR ORDER BY", label)
				require.NotContains(t, p, "SCAN i\n", label)
				full := plan(sqlText, args)
				label = fmt.Sprintf("%s oldest=%v analyze=%v\n%s", tc.name, oldest, analyze, full)
				require.True(t, strings.HasPrefix(full, "SEARCH i USING INTEGER PRIMARY KEY (rowid=?)\n"), label)
				require.Equal(t, 1, strings.Count(full, "USE TEMP B-TREE FOR ORDER BY"), label)
				require.NotContains(t, full, "SCAN i\n", label)
				require.NotContains(t, full, "SCAN c", label)
			}
		}
		// The bounded mark scans by an index too (never a full-table SCAN of items).
		for _, oldest := range []bool{false, true} {
			for _, above := range []bool{false, true} {
				b := &Bound{Oldest: oldest, Above: above, SortAt: 100, ID: 1_700_000_000_100_000}
				for _, sc := range []MarkScope{{}, {FeedID: 2}, {FolderTreeID: 7}, {Starred: true}, {Muted: true}} {
					sel, args, _, _ := markSelectSQL(sc, MarkFilter{Bound: b}, 1_700_000_000_900_000, "", false)
					p := plan(sel, args)
					require.NotContains(t, p, "SCAN items\n", "%+v oldest=%v above=%v analyze=%v\n%s", sc, oldest, above, analyze, p)
					require.Contains(t, p, "idx_items_", "%+v\n%s", sc, p)
				}
			}
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
