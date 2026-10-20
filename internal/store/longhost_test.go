package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/stretchr/testify/require"
)

// legacyLongHostFeed inserts a feed whose host name is over what DNS allows, as one saved before the host-name rule
// existed would be stored (its url_key is what Key gave then).
func legacyLongHostFeed(t *testing.T, db *DB) (int64, string) {
	t.Helper()
	host := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." +
		strings.Repeat("d", 60) + "." + strings.Repeat("e", 60) + ".example"
	u := "https://" + host + "/feed.xml"
	_, err := feedurl.Key(u)
	require.ErrorIs(t, err, feedurl.ErrHostTooLong, "the fixture host is over the limit")
	key, err := feedurl.StoredKey(u)
	require.NoError(t, err)
	var id int64
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO feeds (url, url_key, host) VALUES (?1, ?2, ?3) RETURNING id`, u, key, host).Scan(&id)
	}))
	return id, u
}

// A feed saved with an over-long host name can still be found, edited and unsubscribed by its address (what a
// sync client sends), though no new one can be added.
func TestFeedWithAnOverlongHostIsStillFoundByItsAddress(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	ctx := context.Background()
	id, u := legacyLongHostFeed(t, db)

	got, found, err := db.FindFeedID(ctx, u)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, id, got)

	ids, err := db.EditSubscription(ctx, []FeedRef{{URL: u}}, EditOpts{Title: "Renamed"})
	require.NoError(t, err)
	require.Equal(t, []int64{id}, ids)
	require.Equal(t, "Renamed", scalar[string](t, db.Reader(), "SELECT custom_title FROM feeds WHERE id = ?", id))

	ids, err = db.Unsubscribe(ctx, []FeedRef{{URL: u}})
	require.NoError(t, err)
	require.Equal(t, []int64{id}, ids)
	require.Zero(t, scalar[int64](t, db.Reader(), "SELECT count(*) FROM feeds WHERE id = ?", id))
}

// Deleting by id never parses the stored address, so such a feed deletes like any other.
func TestFeedWithAnOverlongHostDeletesByID(t *testing.T) {
	t.Parallel()
	db, _ := openTest(t)
	ctx := context.Background()
	id, _ := legacyLongHostFeed(t, db)
	require.NoError(t, db.DeleteFeed(ctx, id, false))
	require.Zero(t, scalar[int64](t, db.Reader(), "SELECT count(*) FROM feeds WHERE id = ?", id))

	id, _ = legacyLongHostFeed(t, db)
	ids, err := db.Unsubscribe(ctx, []FeedRef{{ID: id}})
	require.NoError(t, err)
	require.Equal(t, []int64{id}, ids)
}
