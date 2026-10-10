package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func customTitle(t *testing.T, db *DB, id int64) sql.NullString {
	t.Helper()
	var s sql.NullString
	require.NoError(t, db.Reader().QueryRow("SELECT custom_title FROM feeds WHERE id = ?", id).Scan(&s))
	return s
}

// A batch edit with one title per feed commits all or nothing: a failure on a
// later feed leaves the earlier ones untouched, so a client retry does not
// re-apply half a batch.
func TestEditSubscriptionPerFeedTitlesAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	a, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed"})
	require.NoError(t, err)
	b, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed"})
	require.NoError(t, err)
	refs := []FeedRef{{ID: a.FeedID}, {ID: b.FeedID}}

	ids, err := db.EditSubscription(ctx, refs, EditOpts{Titles: []string{"One", "Two"}})
	require.NoError(t, err)
	require.Equal(t, []int64{a.FeedID, b.FeedID}, ids)
	require.Equal(t, "One", customTitle(t, db, a.FeedID).String)
	require.Equal(t, "Two", customTitle(t, db, b.FeedID).String)

	// Make the second feed's rename fail inside the transaction.
	_, err = db.writer.ExecContext(ctx, `CREATE TRIGGER boom BEFORE UPDATE OF custom_title ON feeds
		WHEN NEW.custom_title = 'boom' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	require.NoError(t, err)
	_, err = db.EditSubscription(ctx, refs, EditOpts{Titles: []string{"Changed", "boom"}, Folder: "Moved", SetFolder: true})
	require.Error(t, err)
	require.Equal(t, "One", customTitle(t, db, a.FeedID).String, "the first feed's rename rolled back")
	_, found, err := db.FindLabel(ctx, []string{"Moved"})
	require.NoError(t, err)
	require.False(t, found, "the folder the batch created rolled back too")

	_, err = db.EditSubscription(ctx, refs, EditOpts{Titles: []string{"only one"}})
	require.ErrorIs(t, err, ErrEditTitles)
}
