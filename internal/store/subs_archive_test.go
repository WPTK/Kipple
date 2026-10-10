package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func seedStarred(t *testing.T, e *env, feed, id int64) {
	t.Helper()
	e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
		VALUES (?1, ?2, 0, 1, 1, 1, 'u' || ?1, 'c', 't')`, id, feed)
}

func archiveID(e *env) int64 {
	var id int64
	require.NoError(e.t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT id FROM feeds WHERE disabled_reason = 'archive'").Scan(&id)
	}))
	return id
}

func TestUnsubscribeArchiveInBatchKeepsStarred(t *testing.T) {
	t.Parallel()
	for _, archiveFirst := range []bool{false, true} {
		e := newEnv(t)
		a, b := e.addFeed("https://ex.com/a"), e.addFeed("https://ex.com/b")
		seedStarred(t, e, a, 1)
		_, err := e.db.Unsubscribe(e.ctx, []FeedRef{{ID: a}}) // creates the archive
		require.NoError(t, err)
		arch := archiveID(e)
		seedStarred(t, e, b, 2)
		refs := []FeedRef{{ID: b}, {ID: arch}}
		if archiveFirst {
			refs = []FeedRef{{ID: arch}, {ID: b}}
		}
		ids, skipped, err := e.db.UnsubscribeSkipped(e.ctx, refs)
		require.NoError(t, err)
		require.Equal(t, []int64{b}, ids)
		require.Equal(t, []int64{arch}, skipped)
		require.Equal(t, 2, e.count("SELECT count(*) FROM items WHERE feed_id = ? AND starred = 1", arch))
	}
}

func TestUnsubscribeArchiveAloneAndDeleteStarred(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.addFeed("https://ex.com/a")
	seedStarred(t, e, a, 1)
	_, err := e.db.Unsubscribe(e.ctx, []FeedRef{{ID: a}})
	require.NoError(t, err)
	arch := archiveID(e)

	ids, skipped, err := e.db.UnsubscribeSkipped(e.ctx, []FeedRef{{ID: arch}})
	require.NoError(t, err)
	require.Empty(t, ids)
	require.Equal(t, []int64{arch}, skipped)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items WHERE feed_id = ?", arch))

	// Web path without delete_starred refuses; with it, really deletes.
	require.ErrorIs(t, e.db.DeleteFeed(e.ctx, arch, false), ErrArchiveHasStarred)
	require.NoError(t, e.db.DeleteFeed(e.ctx, arch, true))
	require.Equal(t, 0, e.count("SELECT count(*) FROM feeds WHERE id = ?", arch))
	require.Equal(t, 0, e.count("SELECT count(*) FROM items"))
}

func TestUnsubscribeEmptyArchiveIsDeleted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.addFeed("https://ex.com/a")
	seedStarred(t, e, a, 1)
	_, err := e.db.Unsubscribe(e.ctx, []FeedRef{{ID: a}})
	require.NoError(t, err)
	arch := archiveID(e)
	e.exec("UPDATE items SET starred = 0")
	_, err = e.db.Unsubscribe(e.ctx, []FeedRef{{ID: arch}})
	require.NoError(t, err)
	require.Equal(t, 0, e.count("SELECT count(*) FROM feeds WHERE id = ?", arch))
}
