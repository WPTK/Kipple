package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pagedCount is the number CountSearch must equal: the cards of the search's list, page by page, counted
// (only the unread ones in the starred view), where a list that is the partial-match fallback counts 0.
func pagedCount(t *testing.T, e *env, q CardQuery, starred bool) int {
	t.Helper()
	q.Limit = 3
	n := 0
	for {
		cards, next, fallback, err := e.db.ListCardsFB(e.ctx, q)
		require.NoError(t, err)
		if fallback {
			return 0
		}
		for _, c := range cards {
			if !starred || !c.Read {
				n++
			}
		}
		if next == nil {
			return n
		}
		q.Cursor = next
	}
}

// CountSearch is the length of the list the search opens, for every scope a saved search can have.
func TestCountSearchMatchesThePagedList(t *testing.T) {
	e := newEnv(t)
	var items []sitem
	for i := range 14 {
		items = append(items, sitem{fmt.Sprintf("Apple %d", i), "Ann", "apple pie"})
	}
	items = append(items, sitem{"Pear", "Bob", "pear tart"}, sitem{"Plum", "Bob", "plum jam"})
	ids := seedSearch(t, e, items...)
	other := e.addFeed("https://ex.com/other")
	folder := e.mkFolder(0, "Scoped")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", folder, other)
	for _, id := range ids[:6] {
		e.exec("UPDATE items SET feed_id = ? WHERE id = ?", other, id)
	}
	for _, id := range ids[2:9] {
		e.exec("UPDATE items SET read = 1 WHERE id = ?", id)
	}
	for _, id := range ids[4:12] {
		e.exec("UPDATE items SET starred = 1 WHERE id = ?", id)
	}

	for _, tc := range []struct {
		name    string
		q       CardQuery
		starred bool
		want    int
	}{
		{"unread", CardQuery{View: "unread", Query: "apple "}, false, 7},
		{"unread in a feed", CardQuery{View: "unread", Query: "apple ", FeedID: other}, false, 2},
		{"unread in a folder", CardQuery{View: "unread", Query: "apple ", FolderID: folder}, false, 2},
		{"starred, unread ones", CardQuery{View: "starred", Query: "apple "}, true, 3},
		{"nothing matches", CardQuery{View: "unread", Query: "mango "}, false, 0},
		{"no searchable word", CardQuery{View: "unread", Query: "- + "}, false, 0},
		{"a phrase", CardQuery{View: "unread", Query: `"apple pie" `}, false, 7},
		{"an exclusion", CardQuery{View: "unread", Query: "apple -pie "}, false, 0},
		// No item has both words: the list is the OR fallback and counts 0.
		{"fallback", CardQuery{View: "unread", Query: "apple pear "}, false, 0},
	} {
		got, err := e.db.CountSearch(e.ctx, tc.q, tc.starred, 999)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
		require.Equal(t, pagedCount(t, e, tc.q, tc.starred), got, "%s: the number of the list it opens", tc.name)
	}

	// Past the limit the walk stops one row over, whatever the match set holds.
	n, err := e.db.CountSearch(e.ctx, CardQuery{View: "unread", Query: "apple "}, false, 4)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	n, err = e.db.CountSearch(e.ctx, CardQuery{View: "unread", Query: "apple "}, false, 7)
	require.NoError(t, err)
	require.Equal(t, 7, n)
}

// The card search refuses a search with more matches than searchScanLimit; the count does not apply that
// refusal: it answers the number (or "over the limit") for the same search the list refuses.
func TestCountSearchIsNotRefusedAsTooBroad(t *testing.T) {
	e := newEnv(t)
	var items []sitem
	for i := range 12 {
		items = append(items, sitem{fmt.Sprintf("Apple %d", i), "Ann", "apple pie"})
	}
	seedSearch(t, e, items...)
	old := searchScanLimit
	t.Cleanup(func() { searchScanLimit = old })
	searchScanLimit = 5

	q := CardQuery{View: "unread", Query: "apple ", Limit: 3}
	_, _, _, err := e.db.ListCardsFB(e.ctx, q)
	require.ErrorIs(t, err, ErrSearchTooBroad, "opening the search is refused")

	n, err := e.db.CountSearch(e.ctx, q, false, 999)
	require.NoError(t, err)
	require.Equal(t, 12, n, "the badge still counts it")
	n, err = e.db.CountSearch(e.ctx, q, false, 9)
	require.NoError(t, err)
	require.Equal(t, 10, n, "and reports 'over the cap' past the cap")
}

// The count is one bounded statement over the FTS match joined to items by rowid: it reads no card text,
// builds no snippet, does not sort, and a count past the limit stops one row over it.
func TestCountSearchPlan(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e, sitem{"Apple", "Ann", "apple pie"})
	for _, starred := range []bool{false, true} {
		view := map[bool]string{false: "unread", true: "starred"}[starred]
		sqlText, args, ok := countSearchSQL(CardQuery{View: view, Query: "apple "}, starred, 999, false)
		require.True(t, ok)
		require.Equal(t, 1000, args[len(args)-1], "the walk stops one row past the limit")
		require.Equal(t, 1, strings.Count(sqlText, "SELECT count(*)"))
		require.NotContains(t, sqlText, "snippet(")
		require.NotContains(t, sqlText, "ORDER BY")
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+sqlText, args...)
		require.NoError(t, err)
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			plan.WriteString(detail + "\n")
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		require.Contains(t, plan.String(), "items_fts")
		require.NotContains(t, plan.String(), "USE TEMP B-TREE")
		require.NotContains(t, plan.String(), "item_content")
	}
	_, _, ok := countSearchSQL(CardQuery{View: "unread", Query: "- + "}, false, 999, false)
	require.False(t, ok, "text with no searchable word needs no statement")
}
