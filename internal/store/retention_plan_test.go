package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The statements that act on temp.trim_set walk it and find each row in items by id, so a trim
// costs what it trims, not what the library holds. They used to scan the whole items table once
// a statistics run (ANALYZE, or the nightly PRAGMA optimize) had recorded its size, three times
// per trimming feed (issue #237). The plans are checked on a fresh database and after ANALYZE.
func TestTrimStatementsSearchItemsByID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.loadFeed("http://a.example/feed", 300)
	e.loadFeed("http://b.example/feed", 300)
	for _, analyze := range []bool{false, true} {
		if analyze {
			e.exec("ANALYZE")
		}
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			for _, q := range []string{
				`CREATE TEMP TABLE IF NOT EXISTS trim_set (id INTEGER PRIMARY KEY) STRICT`,
				`DELETE FROM temp.trim_set`,
			} {
				if _, err := tx.ExecContext(ctx, q); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO temp.trim_set SELECT id FROM items WHERE feed_id = ? LIMIT 5`, a); err != nil {
				return err
			}
			for i, args := range [][]any{{0}, {0}, {0, 0, 0}} { // the parameters trimFeedLimit binds
				p := queryPlan(t, tx, trimSetSQL[i], args...)
				requireDrivenByTrimSet(t, p, "statement %d, analyze=%v:\n%s", i, analyze, p)
			}
			p := queryPlan(t, tx, trimDeleteSQL)
			require.Contains(t, p, "SEARCH items USING INTEGER PRIMARY KEY", "analyze=%v:\n%s", analyze, p)
			require.NotContains(t, p, "SCAN items\n", "analyze=%v:\n%s", analyze, p)
			return nil
		}))
	}
}

// requireDrivenByTrimSet checks a plan walks trim_set (t) first and finds every items row (i) by
// its id, through the table or the unread index, never by a scan.
func requireDrivenByTrimSet(t *testing.T, plan string, msg ...any) {
	t.Helper()
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(plan), "\n") {
		switch {
		case strings.HasPrefix(line, "SCAN t"), strings.HasPrefix(line, "SEARCH t "):
			order = append(order, "t")
		case strings.HasPrefix(line, "SCAN i"):
			require.Fail(t, "items is scanned", msg...)
		case strings.HasPrefix(line, "SEARCH i "):
			require.True(t, strings.Contains(line, "(rowid=?)") || strings.Contains(line, "(id=?"), msg...)
			order = append(order, "i")
		}
	}
	require.Contains(t, order, "i", msg...)
	require.Equal(t, "t", order[0], msg...)
}

// queryPlan returns the EXPLAIN QUERY PLAN details of q, one per line.
func queryPlan(t *testing.T, tx *sql.Tx, q string, args ...any) string {
	t.Helper()
	rows, err := tx.Query("EXPLAIN QUERY PLAN "+q, args...)
	require.NoError(t, err)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		b.WriteString(strings.TrimSpace(detail) + "\n")
	}
	require.NoError(t, rows.Err())
	return b.String()
}
