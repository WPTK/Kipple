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
	ctx := context.Background()
	db, _ := openTest(t)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES ('https://a/f','a/f','a')`); err != nil {
			return err
		}
		for i := 0; i < 300; i++ {
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
			require.Contains(t, p, "idx_items_state_changed", "analyze=%v asc=%v\n%s", analyze, asc, p)
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
