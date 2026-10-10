package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// shapeLibrary seeds feeds in nested folders (F holds a, and subfolder G holds b; c is in the
// default folder; d in F is being deleted) with 400 items: sort_at ties, every read, starred and
// muted mix, a spread of word counts, and some items without an item_content row.
func shapeLibrary(t *testing.T) (e *env, feeds map[string]int64, folders map[string]int64) {
	t.Helper()
	e = newEnv(t)
	folders = map[string]int64{"F": e.mkFolder(0, "F")}
	folders["G"] = e.mkFolder(folders["F"], "G")
	feeds = map[string]int64{}
	for name, folder := range map[string]int64{"a": folders["F"], "b": folders["G"], "c": 1, "d": folders["F"]} {
		feeds[name] = e.addFeed("https://" + name + ".example/feed")
		e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", folder, feeds[name])
	}
	order := []int64{feeds["a"], feeds["b"], feeds["c"], feeds["d"]}
	e.exec(`INSERT INTO filters (id, name, scope, kind, terms, action) VALUES (5, 'noise', 'global', 'text', '["x"]', 'mute')`)
	for i := int64(0); i < 400; i++ {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, word_count, uid, content_hash, text_hash, title, muted_by, muted_was_read)
			VALUES (?1, ?2, ?1 % 3 = 0 OR ?1 % 17 = 0, ?1 % 7 = 0, ?1, ?1 / 3, (?1 * 37) % 2500, 'u' || ?1, 'c', 't', 'title ' || ?1,
			        CASE WHEN ?1 % 17 = 0 THEN 5 END, CASE WHEN ?1 % 17 = 0 THEN 0 END)`, 1_000_000+i*1000+(i%5), order[i%4])
		if i%9 != 0 {
			e.exec(`INSERT INTO item_content (item_id, content_html, content_text) VALUES (?1, '<p>body</p>', 'body ' || ?1)`, 1_000_000+i*1000+(i%5))
		}
	}
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return markFeedsDeleting(ctx, tx, []int64{feeds["d"]}, 1)
	}))
	return e, feeds, folders
}

// joinedCards runs a card page the way listCardsSQL built it before the deferred join: one
// query joining item_content to the view's WHERE, keyset order and LIMIT, with the same arguments.
func joinedCards(t *testing.T, e *env, q CardQuery) ([]Card, *Cursor) {
	t.Helper()
	sqlText, args, limit, err := listCardsSQL(q)
	require.NoError(t, err)
	const in = " WHERE i.id IN ("
	pick := sqlText[strings.Index(sqlText, in)+len(in) : strings.LastIndex(sqlText, ") ORDER BY ")]
	joined := "SELECT " + cardCols + " FROM items i LEFT JOIN item_content c ON c.item_id = i.id" +
		strings.TrimPrefix(pick, "SELECT i.id FROM items i")
	rows, err := e.db.Reader().QueryContext(e.ctx, joined, args...)
	require.NoError(t, err)
	defer rows.Close()
	cards := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		require.NoError(t, err)
		cards = append(cards, c)
	}
	require.NoError(t, rows.Err())
	if len(cards) > limit {
		cards = cards[:limit]
		last := cards[len(cards)-1]
		return cards, &Cursor{SortAt: last.SortAt, ID: last.ID, Asc: q.Oldest}
	}
	return cards, nil
}

// The deferred join returns exactly what the joined query did, page by page through the whole
// list: every view, scope (all, a feed, a folder tree, a subfolder), direction and reading filter.
func TestCardPagesMatchTheJoinedQuery(t *testing.T) {
	t.Parallel()
	e, feeds, folders := shapeLibrary(t)
	for _, analyze := range []bool{false, true} {
		if analyze {
			e.exec("ANALYZE")
		}
		for _, view := range []string{"unread", "all", "starred", "muted"} {
			for _, scope := range []CardQuery{{}, {FeedID: feeds["a"]}, {FeedID: feeds["d"]}, {FolderID: folders["F"]}, {FolderID: folders["G"]}} {
				for _, oldest := range []bool{false, true} {
					for _, minutes := range [][2]int{{0, 0}, {2, 0}, {0, 5}} {
						q := scope
						q.View, q.Oldest, q.MinMinutes, q.MaxMinutes, q.Limit = view, oldest, minutes[0], minutes[1], 7
						label := fmt.Sprintf("%+v analyze=%v", q, analyze)
						seen := 0
						for page := 0; ; page++ {
							require.Less(t, page, 100, label)
							want, wantCur := joinedCards(t, e, q)
							got, gotCur, err := e.db.ListCards(e.ctx, q)
							require.NoError(t, err, label)
							require.Equal(t, want, got, "%s page %d", label, page)
							require.Equal(t, wantCur, gotCur, "%s page %d", label, page)
							seen += len(got)
							if gotCur == nil {
								break
							}
							q.Cursor = gotCur
						}
						if view != "muted" && scope.FeedID != feeds["d"] && minutes == [2]int{} {
							require.NotZero(t, seen, label) // the seed reaches every non-empty list
						}
					}
				}
			}
		}
	}
}

// UIFeeds' starred count (a grouped join) is each feed's count of starred items, 0 when it has
// none, as the per-feed subquery it replaced counted it.
func TestUIFeedsStarredCount(t *testing.T) {
	t.Parallel()
	e, _, _ := shapeLibrary(t)
	e.exec("UPDATE items SET starred = 0 WHERE feed_id = (SELECT id FROM feeds WHERE url = 'https://c.example/feed')")
	list, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
	require.NoError(t, err)
	require.Len(t, list, 3, "the feed being deleted is not listed")
	zero := false
	for _, f := range list {
		want := e.count("SELECT count(*) FROM items WHERE feed_id = ? AND starred = 1", f.ID)
		require.EqualValues(t, want, f.StarredCount, f.Title)
		require.EqualValues(t, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", f.ID), f.Unread, f.Title)
		zero = zero || want == 0
	}
	require.True(t, zero, "a feed with no starred item counts 0")
}
