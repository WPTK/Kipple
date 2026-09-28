package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// A feed marked for deletion (step 1 of a delete, left marked by an interrupted
// one) is gone as far as every count is concerned, not only the feed lists: the
// folder unread counts (UIFolders, UpdateFolder), the unread total (Counts and
// the status poll's UnreadTotal), the muted count and the per-feed counts of the
// counts event all agree with UIFeeds.
func TestDeletingFeedLeftOutOfCounts(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	a, err := e.db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "X"})
	require.NoError(t, err)
	b, err := e.db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "X"})
	require.NoError(t, err)
	for i, feed := range []int64{a.FeedID, a.FeedID, a.FeedID, b.FeedID} {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (?1, ?2, 0, 0, 1, 1, 'u' || ?1, 'c', 't')`, int64(i+1), feed)
	}
	for i, feed := range []int64{a.FeedID, b.FeedID} {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash, muted_by, muted_was_read)
			VALUES (?1, ?2, 1, 0, 1, 1, 'm' || ?1, 'c', 't', 77, 0)`, int64(i+10), feed)
	}
	folder, _, err := e.db.FindLabel(ctx, []string{"X"})
	require.NoError(t, err)

	require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return markFeedsDeleting(ctx, tx, []int64{a.FeedID}, 1)
	}))

	unread, _, err := e.db.Counts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, unread)
	total, err := e.db.UnreadTotal(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "UnreadTotal (the status poll) agrees with Counts")
	muted, err := e.db.MutedCount(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, muted)

	per, err := e.db.FeedUnreadCounts(ctx)
	require.NoError(t, err)
	require.NotContains(t, per, a.FeedID)
	require.EqualValues(t, 1, per[b.FeedID])

	folders, err := e.db.UIFolders(ctx)
	require.NoError(t, err)
	var got int64 = -1
	for _, f := range folders {
		if f.ID == folder {
			got = f.Unread
		}
	}
	require.EqualValues(t, 1, got, "UIFolders")

	name := "Y"
	upd, err := e.db.UpdateFolder(ctx, folder, &name, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, upd.Unread, "UpdateFolder")

	feeds, err := e.db.UIFeeds(ctx, StatusEnv{})
	require.NoError(t, err)
	var sum int64
	for _, f := range feeds {
		sum += f.Unread
	}
	require.Equal(t, unread, sum, "the unread total matches the feed list")

	// The article lists agree: the Unread list shows only b's article.
	cards, _, err := e.db.ListCards(ctx, CardQuery{View: "unread"})
	require.NoError(t, err)
	require.Len(t, cards, 1)
	require.Equal(t, b.FeedID, cards[0].FeedID)
	var ids []int64
	_, _, err = e.db.StreamIDs(ctx, StreamFilter{Read: []int{0}}, IDPage{N: 100}, func(id int64) error {
		ids = append(ids, id)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int64{4}, ids, "the Reader API unread stream agrees with unread-count")
	// The health page keeps it: that is where an interrupted delete can be finished by hand.
	health, err := e.db.FeedHealth(ctx)
	require.NoError(t, err)
	listed := false
	for _, h := range health {
		listed = listed || h.ID == a.FeedID
	}
	require.True(t, listed)

	// Mark all read marks what the list showed, not the hidden feed's articles.
	require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkScopeRead(ctx, tx, MarkScope{}, MarkFilter{}, 1000, 1)
		return err
	}))
	var left int
	require.NoError(t, e.db.Reader().QueryRow("SELECT count(*) FROM items WHERE read = 0 AND feed_id = ?", a.FeedID).Scan(&left))
	require.Equal(t, 3, left)
	require.NoError(t, e.db.Reader().QueryRow("SELECT count(*) FROM items WHERE read = 0 AND feed_id = ?", b.FeedID).Scan(&left))
	require.Zero(t, left)
	// So does the Reader API mark-all-as-read.
	require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkAllRead(ctx, tx, MarkScope{}, 1000, 1)
		return err
	}))
	require.NoError(t, e.db.Reader().QueryRow("SELECT count(*) FROM items WHERE read = 0 AND feed_id = ?", a.FeedID).Scan(&left))
	require.Equal(t, 3, left)
}
