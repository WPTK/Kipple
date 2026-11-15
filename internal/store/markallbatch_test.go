package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedItems adds n unread items to the feed with ids first..first+n-1.
func (e *env) seedItems(feed, first int64, n int, starred int) {
	e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?3 - 1)
		INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
		SELECT i, ?2, 0, ?4, 1, 1, 'u' || i, 'c', 't' FROM s`, first, feed, n, starred)
}

// seedLedger adds n unread ledger rows to the feed with ids first..first+n-1.
func (e *env) seedLedger(feed, first int64, n int) {
	e.exec(`WITH RECURSIVE s(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM s WHERE i < ?1 + ?3 - 1)
		INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
		SELECT i, ?2, 'l' || i, 0, 1, 1 FROM s`, first, feed, n)
}

func (e *env) batch(scope MarkScope, limit int) (int64, bool) {
	var n int64
	var more bool
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		var err error
		n, more, err = markAllReadBatch(ctx, tx, scope, 1<<40, 100, limit)
		return err
	})
	return n, more
}

// One batch of a mark-all-read changes at most its limit of rows in each table and says when more are
// left, so its transaction stays short however large the scope. A batch filled by the ledger alone
// also says so.
func TestMarkAllReadBatchIsBounded(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://batch.example/feed")
	e.seedItems(feed, 1, 10, 0)
	e.seedLedger(feed, 11, 5)
	n, more := e.batch(MarkScope{FeedID: feed}, 3)
	require.EqualValues(t, 3, n)
	require.True(t, more)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE read = 1"))
	require.Equal(t, 3, e.count("SELECT count(*) FROM trimmed_items WHERE read = 1"))
	n, more = e.batch(MarkScope{FeedID: feed}, 100)
	require.EqualValues(t, 7, n)
	require.False(t, more)
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items WHERE read = 0"))

	e2 := newEnv(t)
	f2 := e2.addFeed("http://ledger.example/feed")
	e2.seedItems(f2, 1, 2, 0)
	e2.seedLedger(f2, 3, 5)
	n, more = e2.batch(MarkScope{FeedID: f2}, 3)
	require.EqualValues(t, 2, n)
	require.True(t, more, "the ledger filled the batch")
	n, more = e2.batch(MarkScope{FeedID: f2}, 3)
	require.Zero(t, n)
	require.False(t, more)
	require.Zero(t, e2.count("SELECT count(*) FROM trimmed_items WHERE read = 0"))
}

// MarkAllReadBatched ends with the same state as one transaction over the whole scope: every unread item
// and ledger row at or below the cutoff read, the count returned, other feeds untouched.
func TestMarkAllReadBatchedMarksEverything(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://batched.example/feed")
	other := e.addFeed("http://other.example/feed")
	items, ledger := 2*MarkAllBatch+7, MarkAllBatch+3
	e.seedItems(feed, 1, items, 0)
	e.seedLedger(feed, int64(items)+1, ledger)
	e.seedItems(other, int64(items+ledger)+1, 1, 0)

	n, err := e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: feed}, 1<<40, 500)
	require.NoError(t, err)
	require.EqualValues(t, items, n)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", feed))
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", feed))
	require.Equal(t, items, e.count("SELECT count(*) FROM items WHERE read = 1 AND read_at = 500 AND state_changed_at = 500"))
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", other))

	n, err = e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: feed}, 1<<40, 600)
	require.NoError(t, err)
	require.Zero(t, n, "nothing left")
}

// The cutoff is clamped to what is committed when the run starts: an item committed after that, between two
// batches, is never reached, whatever cutoff the caller sent.
func TestMarkAllReadBatchedNeverReachesWhatCommitsLater(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://later.example/feed")
	items := 2*MarkAllBatch + 5
	e.seedItems(feed, 1, items, 0)
	top, err := e.db.MaxCommittedID(e.ctx)
	require.NoError(t, err)
	late := top + 10
	inserted := false
	n, err := e.db.markAllReadBatched(e.ctx, MarkScope{FeedID: feed}, 1<<40, top, 500, func() {
		if !inserted {
			inserted = true
			e.seedItems(feed, late, 1, 0)
		}
	})
	require.NoError(t, err)
	require.True(t, inserted, "the run took more than one batch")
	require.EqualValues(t, items, n)
	require.Equal(t, 0, e.count("SELECT read FROM items WHERE id = ?", late))
}

// A run that fails after its first batch leaves that batch committed and reports what it changed.
func TestMarkAllReadBatchedKeepsEarlierBatchesOnFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://fail.example/feed")
	e.seedItems(feed, 1, 2*MarkAllBatch+5, 0)
	top, err := e.db.MaxCommittedID(e.ctx)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(e.ctx)
	n, err := e.db.markAllReadBatched(ctx, MarkScope{FeedID: feed}, top, top, 500, cancel)
	require.Error(t, err)
	require.EqualValues(t, MarkAllBatch, n)
	require.Equal(t, MarkAllBatch, e.count("SELECT count(*) FROM items WHERE read = 1"))
}

// The scopes: the held items stay unread through every batch, a starred scope marks starred items only and
// skips the ledger, and a label marks its own feeds only.
func TestMarkAllReadBatchedScopes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	n := 2*MarkAllBatch + 5

	held := e.addFeed("http://held.example/feed")
	e.exec("UPDATE feeds SET fulltext = 1 WHERE id = ?", held)
	e.seedItems(held, 1, n, 0)
	got, err := e.db.MarkAllReadBatched(e.ctx, MarkScope{FeedID: held, HoldCut: 1, HoldPending: "[2,3,4]"}, 1<<40, 500)
	require.NoError(t, err)
	require.EqualValues(t, n-3, got)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", held))
	require.Equal(t, 0, e.count("SELECT read FROM items WHERE id = 2"))

	star := e.addFeed("http://star.example/feed")
	base := int64(n) + 1
	e.seedItems(star, base, n, 1)
	e.seedItems(star, base+int64(n), 3, 0)
	e.seedLedger(star, base+int64(n)+3, 4)
	got, err = e.db.MarkAllReadBatched(e.ctx, MarkScope{Starred: true}, 1<<40, 600)
	require.NoError(t, err)
	require.EqualValues(t, n, got)
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", star), "unstarred items stay")
	require.Equal(t, 4, e.count("SELECT count(*) FROM trimmed_items WHERE read = 0"), "a starred scope leaves the ledger")

	folder := e.mkFolder(0, "F")
	in, out := e.addFeed("http://in.example/feed"), e.addFeed("http://out.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", folder, in)
	base += int64(2*n) + 100
	e.seedItems(in, base, n, 0)
	e.seedLedger(in, base+int64(n), 3)
	e.seedItems(out, base+int64(n)+3, 4, 0)
	e.seedLedger(out, base+int64(n)+7, 2)
	got, err = e.db.MarkAllReadBatched(e.ctx, MarkScope{FolderID: folder}, 1<<40, 700)
	require.NoError(t, err)
	require.EqualValues(t, n, got)
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", in))
	require.Zero(t, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", in))
	require.Equal(t, 4, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND read = 0", out))
	require.Equal(t, 2, e.count("SELECT count(*) FROM trimmed_items WHERE feed_id = ? AND read = 0", out))
}

// The batch statements stay on the partial unread indexes: the scope's own index for a feed, a label and
// starred, the global one otherwise, and no sort of the whole scope.
func TestMarkAllBatchQueryPlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	feed := e.addFeed("http://plan.example/feed")
	e.seedItems(feed, 1, 3000, 0)
	e.seedLedger(feed, 3001, 100)
	plan := func(q string, args []any) string {
		rows, err := e.db.Reader().Query("EXPLAIN QUERY PLAN "+q, args...)
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
	scopes := []struct {
		name  string
		scope MarkScope
		index string
	}{
		{"all", MarkScope{}, "idx_items_unread (id<?)"},
		{"feed", MarkScope{FeedID: feed}, "idx_items_unread_feed (feed_id=? AND id<?)"},
		{"label", MarkScope{FolderID: 1}, "idx_items_unread_feed (feed_id=? AND id<?)"},
		{"starred", MarkScope{Starred: true}, "idx_items_starred (id<?)"},
		{"hold", MarkScope{HoldCut: 5, HoldPending: "[]"}, "idx_items_unread (id<?)"},
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			_, err := e.db.writer.ExecContext(e.ctx, "ANALYZE")
			require.NoError(t, err)
		}
		for _, sc := range scopes {
			q, a, lq, la, err := markAllSQL(sc.scope, false, 1<<40, 1, MarkAllBatch)
			require.NoError(t, err)
			label := fmt.Sprintf("%s analyze=%v", sc.name, analyze)
			p := plan(q, a)
			require.Contains(t, p, sc.index, label+"\n"+p)
			require.NotContains(t, p, "TEMP B-TREE", label+"\n"+p)
			if lq == "" {
				continue
			}
			lp := plan(lq, la)
			require.NotContains(t, lp, "TEMP B-TREE", label+"\n"+lp)
			if sc.name == "all" || sc.name == "hold" {
				require.Contains(t, lp, "idx_trimmed_unread (id<?)", label+"\n"+lp)
			}
		}
	}
}
