package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The id query's plans (design §2.3, §6.5): PK range for leg 1, the partial
// changed index for leg 2, idx_items_unread for the unread stream, on a fresh
// database and after ANALYZE.
func TestStreamIDsQueryPlans(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES ('https://a/f','a/f','a')`); err != nil {
			return err
		}
		for i := range 300 {
			var changed, state any
			if i%10 == 0 {
				changed = 1000 + i
			}
			if i%3 == 2 {
				state = 2000 + i
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, published_at, sort_at, content_changed_at, state_changed_at, uid, content_hash, text_hash)
				VALUES (?,1,?,1,1,?,?,?,'c','t')`, 1_700_000_000_000_000+int64(i)*1000, i%3/2, changed, state, fmt.Sprintf("u%d", i)); err != nil {
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
		for _, asc := range []bool{false, true} {
			q, args := streamIDsSQL(StreamFilter{}, IDPage{N: 10, Asc: asc, HasOT: true, OT: 1_700_000_000})
			p := plan(q, args)
			require.Contains(t, p, "idx_items_changed", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.Contains(t, p, "PRIMARY KEY", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.NotContains(t, p, "SCAN items\n", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.NotContains(t, q, "state_changed_at", "the default-off query is unchanged")
			require.Contains(t, q, "UNION ALL")

			// greader.ot_includes_user_changes: the state-change branch on its own partial index.
			q, args = streamIDsSQL(StreamFilter{}, IDPage{N: 10, Asc: asc, HasOT: true, OT: 1_700_000_000, UserChanges: true})
			p = plan(q, args)
			require.Contains(t, p, "idx_items_changed", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.Contains(t, p, "USING COVERING INDEX idx_items_state_changed (state_changed_at>?)", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.Contains(t, p, "PRIMARY KEY", "analyze=%v asc=%v\n%s", analyze, asc, p)
			require.NotContains(t, p, "SCAN items\n", "analyze=%v asc=%v\n%s", analyze, asc, p)
		}
		q, args := streamIDsSQL(StreamFilter{Read: []int{0}}, IDPage{N: 10})
		p := plan(q, args)
		require.Contains(t, p, "idx_items_unread", "analyze=%v\n%s", analyze, p)
		q, args = streamIDsSQL(StreamFilter{Starred: []int{1}}, IDPage{N: 10})
		require.Contains(t, plan(q, args), "idx_items_starred", "analyze=%v", analyze)
	}
}

// The id and content streams call their callback with no reader connection checked out, so a client
// that drains slowly cannot hold one of the pool's connections (or a read snapshot) for the whole write.
func TestStreamCallbacksHoldNoReaderConnection(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://conn.example/feed")
	e.fetchBody(feed, frss(fspec{guid: "c1", title: "one"}, fspec{guid: "c2", title: "two"}))
	ids, err := queryIDs(e.ctx, e.db.Reader(), "SELECT id FROM items")
	require.NoError(t, err)
	require.Len(t, ids, 2)
	calls := 0
	require.NoError(t, e.db.StreamItems(e.ctx, ids, true, 0, func(*ContentRow) error {
		calls++
		require.Zero(t, e.db.Reader().Stats().InUse, "StreamItems calls fn with no reader connection checked out")
		return nil
	}))
	_, _, err = e.db.StreamIDs(e.ctx, StreamFilter{}, IDPage{N: 100}, func(int64) error {
		calls++
		require.Zero(t, e.db.Reader().Stats().InUse, "StreamIDs calls fn with no reader connection checked out")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 4, calls)
}

// Large items are read a statement at a time, cut at the byte bound; every requested item still comes
// back once, in order, whichever way the ids were given.
func TestStreamItemsChunksByBytesKeepOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://chunk.example/feed")
	body := strings.Repeat("word ", 90_000) // about 450 KB each: three of them pass the 1 MiB bound
	var specs []fspec
	for i := range 7 {
		specs = append(specs, fspec{guid: fmt.Sprintf("g%d", i), title: fmt.Sprintf("t%d", i), body: body})
	}
	e.fetchBody(feed, frss(specs...))
	ids, err := queryIDs(e.ctx, e.db.Reader(), "SELECT id FROM items ORDER BY id")
	require.NoError(t, err)
	require.Len(t, ids, 7)
	for _, asc := range []bool{true, false} {
		in := append([]int64{ids[3], ids[0], 999_999}, ids...) // unsorted, duplicated, one unknown
		var got []int64
		require.NoError(t, e.db.StreamItems(e.ctx, in, asc, 0, func(r *ContentRow) error {
			require.Zero(t, e.db.Reader().Stats().InUse)
			got = append(got, r.ID)
			return nil
		}))
		want := append([]int64(nil), ids...)
		if !asc {
			for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
				want[i], want[j] = want[j], want[i]
			}
		}
		require.Equal(t, want, got, "asc=%v", asc)
	}
}
