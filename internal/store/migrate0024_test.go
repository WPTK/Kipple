package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	narrowUnreadSort = "CREATE INDEX idx_items_unread_sort ON items(sort_at, id) WHERE read = 0"
	wideUnreadSort   = "CREATE INDEX idx_items_unread_sort ON items(sort_at, id, feed_id, fulltext_mode, word_count) WHERE read = 0"
)

// seedUnreadItems adds n items over feeds: the ones whose id ends in 3 are read (a tenth), word counts spread
// over 1 to 3,000 and every 500th item is empty, so the empty ones are unread.
func seedUnreadItems(e *env, n int) {
	e.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
		INSERT INTO items (id, feed_id, read, published_at, sort_at, uid, content_hash, text_hash, word_count, title, url)
		SELECT i, (SELECT min(id) FROM feeds) + i % (SELECT count(*) FROM feeds), i % 10 = 3, i, i, 'u' || i, 'c', 't',
			CASE WHEN i % 500 = 0 THEN 0 ELSE (i * 7919) % 3000 + 1 END, 'A title of some length ' || i, 'https://a.example/' || i FROM n`, n)
}

// itemRows is every item's stored values in id order, as one string to compare across a migration.
const itemRows = `SELECT group_concat(id || '|' || feed_id || '|' || read || '|' || sort_at || '|' || word_count || '|' || ifnull(fulltext_mode, 'null') || '|' || uid || '|' || title || '|' || url, char(10))
	FROM (SELECT * FROM items ORDER BY id)`

// pickIDs runs one id pick and returns its ids; an empty page is an empty slice, not nil.
func pickIDs(t *testing.T, db *sql.DB, ctx context.Context, q pickQuery) []int64 {
	t.Helper()
	rows, err := db.QueryContext(ctx, q.sql, q.args...)
	require.NoError(t, err)
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// 0024 widens idx_items_unread_sort and changes no row: a schema-23 database (the narrow index back and
// user_version stepped back, with planner statistics gathered for it) upgrades to the same objects as a fresh
// one, with every item's values unchanged, and its length-filtered page is read from the index alone.
func TestMigration0024WidensTheUnreadSortIndex(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for f := range 3 {
		e.addFeed(fmt.Sprintf("https://a.example/%d", f))
	}
	seedUnreadItems(e, 3000)
	fresh := schemaObjects(t, e.db.Reader())
	// The seeded sort_at values are past their (tiny) crawl seconds; stepping back to 23 also runs the later
	// sort_at migration, so state the rows the way it leaves them.
	e.exec("UPDATE items SET sort_at = id / 1000000 WHERE sort_at > id / 1000000")
	rowsBefore := scalar[string](t, e.db.Reader(), itemRows)
	require.Equal(t, wideUnreadSort, scalar[string](t, e.db.Reader(), "SELECT sql FROM sqlite_schema WHERE name = 'idx_items_unread_sort'"))
	e.exec("DROP INDEX idx_items_unread_sort")
	e.exec(narrowUnreadSort)
	e.exec("ANALYZE")
	e.exec("PRAGMA user_version = 23")
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db := reopen(t, path)
	r := db.Reader()
	require.Equal(t, wideUnreadSort, scalar[string](t, r, "SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = 'idx_items_unread_sort'"))
	require.Equal(t, fresh, schemaObjects(t, r))
	require.Equal(t, rowsBefore, scalar[string](t, r, itemRows))
	require.Equal(t, 3000, scalar[int](t, r, "SELECT count(*) FROM items"))
	require.Equal(t, 300, scalar[int](t, r, "SELECT count(*) FROM items WHERE read = 1"))
	requireCleanIntegrity(t, r)
	for name, q := range lengthQueries(t, false) {
		p := planOf(t, db.writer, e.ctx, q.sql, q.args)
		require.Contains(t, p, "COVERING INDEX idx_items_unread_sort", name+"\n"+p)
	}
}

type pickQuery struct {
	sql  string
	args []any
}

// lengthQueries are the id picks of the Unread list pages the Digest asks for: each reading-length bucket
// and the plain list, newest first and oldest first, with and without a cursor.
func lengthQueries(t *testing.T, all bool) map[string]pickQuery {
	t.Helper()
	out := map[string]pickQuery{}
	for _, oldest := range []bool{false, true} {
		for _, cur := range []*Cursor{nil, {SortAt: 1000, ID: 1000, Asc: oldest}} {
			for name, q := range map[string]CardQuery{
				"unread":  {View: "unread"},
				"short":   {View: "unread", MaxMinutes: 3},
				"middle":  {View: "unread", MinMinutes: 3, MaxMinutes: 7},
				"long":    {View: "unread", MinMinutes: 8},
				"unknown": {View: "unread", UnknownLength: true},
			} {
				q.Oldest, q.Cursor, q.Limit = oldest, cur, 30
				sqlText, args, _, err := listCardsSQL(q, all)
				require.NoError(t, err)
				const in = " WHERE i.id IN ("
				pick := sqlText[strings.Index(sqlText, in)+len(in) : strings.LastIndex(sqlText, ") ORDER BY ")]
				out[fmt.Sprintf("%s oldest=%v cursor=%v all=%v", name, oldest, cur != nil, all)] = pickQuery{pick, args}
			}
		}
	}
	return out
}

func planOf(t *testing.T, db *sql.DB, ctx context.Context, q string, args []any) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
	require.NoError(t, err)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		b.WriteString(detail + "\n")
	}
	require.NoError(t, rows.Err())
	return b.String()
}

// A page of the Unread list, cut by reading length or not, is picked from idx_items_unread_sort alone, on a
// fresh database and after ANALYZE, whether or not full text is switched on for every feed: no candidate row
// is fetched from the table to read its feed, mode or length.
func TestUnreadLengthPagesAreCovered(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for f := range 3 {
		e.addFeed(fmt.Sprintf("https://a.example/%d", f))
	}
	seedUnreadItems(e, 6000)
	for _, analyze := range []bool{false, true} {
		if analyze {
			e.exec("ANALYZE")
		}
		for _, all := range []bool{false, true} {
			for name, q := range lengthQueries(t, all) {
				p := planOf(t, e.db.writer, e.ctx, q.sql, q.args)
				label := fmt.Sprintf("%s analyze=%v\n%s", name, analyze, p)
				require.Contains(t, p, "COVERING INDEX idx_items_unread_sort", label)
				require.NotContains(t, p, "USE TEMP B-TREE FOR ORDER BY", label)
				require.NotContains(t, p, "SCAN i\n", label)
			}
		}
	}
}

// The index change does not change which articles a length-cut page holds.
func TestUnreadLengthPagesHoldTheSameItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for f := range 3 {
		e.addFeed(fmt.Sprintf("https://a.example/%d", f))
	}
	seedUnreadItems(e, 6000)
	ids := func() map[string][]int64 {
		out := map[string][]int64{}
		for name, q := range lengthQueries(t, false) {
			out[name] = pickIDs(t, e.db.writer, e.ctx, q)
		}
		return out
	}
	wide := ids()
	e.exec("DROP INDEX idx_items_unread_sort")
	e.exec(narrowUnreadSort)
	require.Equal(t, wide, ids())
	for name, got := range wide {
		require.NotEmpty(t, got, name)
	}
	// The sparse bucket holds exactly the unread empty items, newest first: every 500th id.
	var want []int64
	for id := int64(6000); id >= 500; id -= 500 {
		want = append(want, id)
	}
	require.Equal(t, want, wide["unknown oldest=false cursor=false all=false"])
}

// TestUnreadLengthPerf times the length-cut pages on a large library, with the index as it was and as it is.
// Run it with KIPPLE_PERF=1 go test ./internal/store -run UnreadLengthPerf -v; docs/design.md §2.2a has the
// figures.
func TestUnreadLengthPerf(t *testing.T) {
	t.Parallel()
	if testing.Short() || os.Getenv("KIPPLE_PERF") == "" {
		t.Skip("seeds 600,000 rows; set KIPPLE_PERF=1")
	}
	e := newEnv(t)
	for f := range 50 {
		e.addFeed(fmt.Sprintf("https://a.example/%d", f))
	}
	seedUnreadItems(e, 600_000)
	e.exec("ANALYZE")
	best := func(q pickQuery, want int) time.Duration {
		var b time.Duration
		for i := range 5 {
			start := time.Now()
			ids := pickIDs(t, e.db.writer, e.ctx, q)
			if d := time.Since(start); i == 0 || d < b {
				b = d
			}
			require.Len(t, ids, want)
		}
		return b
	}
	query := func(name string) pickQuery {
		key := name + " oldest=false cursor=false all=false"
		q, ok := lengthQueries(t, false)[key]
		require.True(t, ok, key)
		return q
	}
	// Every bucket, the sparse one included, has enough unread matches to fill the page (the limit and one more).
	report := func(label string) {
		for _, name := range []string{"unread", "short", "long", "unknown"} {
			t.Logf("%s %s: %v", label, name, best(query(name), 31))
		}
	}
	report("wide")
	e.exec("DROP INDEX idx_items_unread_sort")
	e.exec(narrowUnreadSort)
	e.exec("ANALYZE")
	report("narrow")
	// A bucket nothing falls into walks every unread item to find that out.
	e.exec("UPDATE items SET word_count = 1 WHERE word_count = 0")
	e.exec("ANALYZE")
	t.Logf("narrow empty bucket: %v", best(query("unknown"), 0))
	e.exec("DROP INDEX idx_items_unread_sort")
	e.exec(wideUnreadSort)
	e.exec("ANALYZE")
	t.Logf("wide empty bucket: %v", best(query("unknown"), 0))
}
