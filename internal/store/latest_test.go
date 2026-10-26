package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLatestHeadlines(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := e.ctx

	got, err := e.db.LatestHeadlines(ctx, 3)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got, "no articles: no feeds listed")

	sub := func(url string) int64 {
		s, err := e.db.Subscribe(ctx, SubscribeOpts{URL: url})
		require.NoError(t, err)
		return s.FeedID
	}
	add := func(id, feed int64, mutedBy any) {
		e.exec(`INSERT INTO items (id, feed_id, read, starred, published_at, sort_at, uid, content_hash, text_hash, muted_by)
			VALUES (?1, ?2, 0, 0, 1, ?1, 'u' || ?1, 'c', 't', ?3)`, id, feed, mutedBy)
	}
	live, arch, gone := sub("https://a.example/feed"), sub("https://b.example/feed"), sub("https://c.example/feed")
	for id := int64(1); id <= 4; id++ {
		add(id, live, nil)
	}
	add(5, live, 77) // muted: the newest, yet left out
	add(6, arch, nil)
	add(7, gone, nil)
	e.exec("UPDATE feeds SET enabled = 0, disabled_reason = 'archive' WHERE id = ?", arch)
	require.NoError(t, e.db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return markFeedsDeleting(ctx, tx, []int64{gone}, 1)
	}))

	got, err = e.db.LatestHeadlines(ctx, 3)
	require.NoError(t, err)
	require.Len(t, got, 1, "the archive feed and a feed being deleted are left out")
	require.Equal(t, live, got[0].FeedID)
	ids := []int64{}
	for _, h := range got[0].Items {
		ids = append(ids, h.ID)
	}
	require.Equal(t, []int64{4, 3, 2}, ids, "newest first, the muted article skipped, capped at n")

	got, err = e.db.LatestHeadlines(ctx, 100)
	require.NoError(t, err)
	require.Len(t, got[0].Items, 4, "n is capped by what exists (and by LatestMaxPerFeed)")
}
