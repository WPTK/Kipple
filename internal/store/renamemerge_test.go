package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// renameLabel is RenameLabel for tests that do not look at its filters-changed report.
func renameLabel(ctx context.Context, db *DB, id int64, name string) error {
	_, err := db.RenameLabel(ctx, id, name)
	return err
}

// A Reader API rename onto an existing folder's name merges the folders. The old
// folder's filters survive at the scope they had: each becomes a feed filter for
// each feed that was in the old folder, so a mute on "Old" never starts muting
// the feeds that were already in "New".
func TestRenameMergeKeepsFolderFilterScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	a, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "Old"})
	require.NoError(t, err)
	a2, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a2.example/feed", Folder: "Old"})
	require.NoError(t, err)
	b, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "New"})
	require.NoError(t, err)
	oldID, _, err := db.FindLabel(ctx, []string{"Old"})
	require.NoError(t, err)
	newID, _, err := db.FindLabel(ctx, []string{"New"})
	require.NoError(t, err)
	res, err := db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action, hits, last_hit_at)
		VALUES ('f', 1, 'folder', ?, 'text', '["sponsored"]', '["title"]', 'mute', 1200, 5)`, oldID)
	require.NoError(t, err)
	orig, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, `INSERT INTO items (id, feed_id, read, published_at, sort_at, uid, content_hash, text_hash, muted_by, muted_was_read)
		VALUES (900, ?, 1, 1, 1, 'u', 'c', 't', ?, 0)`, a2.FeedID, orig)
	require.NoError(t, err)

	require.NoError(t, renameLabel(ctx, db, oldID, "New"))

	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE folder_id = ?", newID).Scan(&n))
	require.Zero(t, n, "no filter widened to the whole merged folder")
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE scope = 'folder'").Scan(&n))
	require.Zero(t, n)

	feeds, err := queryIDs(ctx, db.Reader(), "SELECT feed_id FROM filters WHERE scope = 'feed' AND name = 'f' AND enabled = 1 ORDER BY feed_id")
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{a.FeedID, a2.FeedID}, feeds, "one feed filter per feed of the old folder, none for %d", b.FeedID)
	var keptFeed int64
	require.NoError(t, db.Reader().QueryRow("SELECT feed_id FROM filters WHERE id = ? AND hits = 1200 AND last_hit_at = 5", orig).Scan(&keptFeed))
	require.Equal(t, a.FeedID, keptFeed, "the rule itself (id, match history) stays with the first feed")
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE hits = 0 AND last_hit_at IS NULL").Scan(&n))
	require.Equal(t, 1, n, "a copy starts with no matches, so the total is not multiplied")

	var mutedBy, copyFeed int64
	require.NoError(t, db.Reader().QueryRow("SELECT muted_by FROM items WHERE id = 900").Scan(&mutedBy))
	require.NotEqual(t, orig, mutedBy, "the muted mark follows the copy")
	require.NoError(t, db.Reader().QueryRow("SELECT feed_id FROM filters WHERE id = ?", mutedBy).Scan(&copyFeed))
	require.Equal(t, a2.FeedID, copyFeed)

	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM folders WHERE id = ?", oldID).Scan(&n))
	require.Zero(t, n)
}

// Merging the default folder away (it is never deleted) keeps its folder filter
// for feeds that land there later and adds feed filters for the feeds that moved.
func TestRenameMergeDefaultFolderFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	a, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed"})
	require.NoError(t, err)
	_, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "New"})
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
		VALUES ('d', 1, 'folder', 1, 'text', '["sponsored"]', '["title"]', 'mute')`)
	require.NoError(t, err)
	// The archive feed sits in the default folder too; it is never fetched and gets no filter.
	_, err = db.writer.ExecContext(ctx, `INSERT INTO feeds (folder_id, url, url_key, host, title, enabled, disabled_reason, retention)
		VALUES (1, 'kipple:archive', 'kipple:archive', 'kipple.invalid', 'Unsubscribed (starred)', 0, 'archive', 0)`)
	require.NoError(t, err)

	require.NoError(t, renameLabel(ctx, db, 1, "New"))

	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE scope = 'folder' AND folder_id = 1").Scan(&n))
	require.Equal(t, 1, n, "the default folder keeps its filter")
	feeds, err := queryIDs(ctx, db.Reader(), "SELECT feed_id FROM filters WHERE scope = 'feed' ORDER BY feed_id")
	require.NoError(t, err)
	require.Equal(t, []int64{a.FeedID}, feeds)
}

// A merge whose copies would take the filter set past filter.MaxRules is refused
// before anything changes: the folders, feeds and filters stay as they were.
func TestRenameMergeRefusedPastRuleLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	for _, u := range []string{"https://a.example/feed", "https://b.example/feed", "https://c.example/feed"} {
		_, err := db.Subscribe(ctx, SubscribeOpts{URL: u, Folder: "Old"})
		require.NoError(t, err)
	}
	_, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://d.example/feed", Folder: "New"})
	require.NoError(t, err)
	oldID, _, err := db.FindLabel(ctx, []string{"Old"})
	require.NoError(t, err)
	// 100 folder filters x 3 feeds = 300 copies, past the 200 limit.
	for i := 0; i < 100; i++ {
		_, err = db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
			VALUES ('f', 0, 'folder', ?, 'text', '["x"]', '["title"]', 'mute')`, oldID)
		require.NoError(t, err)
	}
	require.ErrorIs(t, renameLabel(ctx, db, oldID, "New"), ErrMergeTooManyFilters)
	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE scope = 'folder' AND folder_id = ?", oldID).Scan(&n))
	require.Equal(t, 100, n)
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE folder_id = ?", oldID).Scan(&n))
	require.Equal(t, 3, n)
}

// Nor may a merge switch off copies of a rule that runs today: 60 copies of one
// enabled regex rule would pass the 50 regex rules a set may run.
func TestRenameMergeRefusedPastRegexLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	for i := 0; i < 60; i++ {
		_, err := db.Subscribe(ctx, SubscribeOpts{URL: fmt.Sprintf("https://f%d.example/feed", i), Folder: "Old"})
		require.NoError(t, err)
	}
	_, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://new.example/feed", Folder: "New"})
	require.NoError(t, err)
	oldID, _, err := db.FindLabel(ctx, []string{"Old"})
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
		VALUES ('r', 1, 'folder', ?, 'regex', '["ab+c"]', '["title"]', 'mute')`, oldID)
	require.NoError(t, err)
	require.ErrorIs(t, renameLabel(ctx, db, oldID, "New"), ErrMergeTooManyFilters)
	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters").Scan(&n))
	require.Equal(t, 1, n)

	// The same rule switched off by the user copies fine: nothing that runs today stops running.
	_, err = db.writer.ExecContext(ctx, "UPDATE filters SET enabled = 0")
	require.NoError(t, err)
	require.NoError(t, renameLabel(ctx, db, oldID, "New"))
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters WHERE scope = 'feed' AND enabled = 0").Scan(&n))
	require.Equal(t, 60, n)
}

// A folder filter Kipple switched off keeps its reason on every copy.
func TestRenameMergeCarriesDisabledReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	a, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://a.example/feed", Folder: "Old"})
	require.NoError(t, err)
	_, err = db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "New"})
	require.NoError(t, err)
	oldID, _, err := db.FindLabel(ctx, []string{"Old"})
	require.NoError(t, err)
	res, err := db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
		VALUES ('f', 0, 'folder', ?, 'text', '["x"]', '["title"]', 'mute')`, oldID)
	require.NoError(t, err)
	orig, err := res.LastInsertId()
	require.NoError(t, err)
	require.NoError(t, saveFilterReasons(ctx, db.writer, map[int64]string{orig: "too costly (edit the filter to re-enable it)"}, 1))

	require.NoError(t, renameLabel(ctx, db, oldID, "New"))
	fs, err := db.ListFilters(ctx)
	require.NoError(t, err)
	require.Len(t, fs, 1)
	require.Equal(t, a.FeedID, *fs[0].FeedID)
	require.NotNil(t, fs[0].DisabledReason)
	require.Equal(t, "too costly (edit the filter to re-enable it)", *fs[0].DisabledReason)
	require.Equal(t, orig, fs[0].ID, "converted in place")
}

// An empty old folder's filters matched nothing and go with the folder.
func TestRenameMergeEmptyFolderDropsFilters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, _ := openTest(t)
	_, err := db.Subscribe(ctx, SubscribeOpts{URL: "https://b.example/feed", Folder: "New"})
	require.NoError(t, err)
	res, err := db.writer.ExecContext(ctx, "INSERT INTO folders (name) VALUES ('Empty')")
	require.NoError(t, err)
	emptyID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = db.writer.ExecContext(ctx, `INSERT INTO filters (name, enabled, scope, folder_id, kind, terms, fields, action)
		VALUES ('e', 1, 'folder', ?, 'text', '["x"]', '["title"]', 'mute')`, emptyID)
	require.NoError(t, err)

	require.NoError(t, renameLabel(ctx, db, emptyID, "New"))
	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM filters").Scan(&n))
	require.Zero(t, n)
}
