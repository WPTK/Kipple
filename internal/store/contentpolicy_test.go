package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/sanitize"
)

const hostileHTML = `<p onclick="x()">hi</p><script>alert(1)</script>`

func seedStoredHTML(t *testing.T, db *DB, html string) (item int64) {
	t.Helper()
	feed := seedFeed(t, db)
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash)
			VALUES (7, ?, 1, 1, 'u7', 'c', 't')`, feed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (7, ?, 'old')`, html); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO item_fulltext (item_id, content_html, content_text, extracted_at) VALUES (7, ?, 'old', 1)`, html)
		return err
	}))
	return 7
}

func TestStoredHTMLIsCleanedAgainUnderANewPolicyVersion(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	seedStoredHTML(t, db, hostileHTML)

	n, err := db.EnsureContentPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the article and its extracted text")
	require.Equal(t, `<p>hi</p>`, scalar[string](t, db.Reader(), "SELECT content_html FROM item_content WHERE item_id = 7"))
	require.Equal(t, `<p>hi</p>`, scalar[string](t, db.Reader(), "SELECT content_html FROM item_fulltext WHERE item_id = 7"))
	require.Equal(t, "hi", scalar[string](t, db.Reader(), "SELECT content_text FROM item_content WHERE item_id = 7"))
	require.Equal(t, strconv.Itoa(sanitize.PolicyVersion), scalar[string](t, db.Reader(), "SELECT value FROM settings WHERE key = ?", SettingContentPolicy))

	// Done for this version: stored HTML is not looked at again until the version changes or a restore clears the mark.
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE item_content SET content_html = ? WHERE item_id = 7`, hostileHTML)
		return err
	}))
	n, err = db.EnsureContentPolicy(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, hostileHTML, scalar[string](t, db.Reader(), "SELECT content_html FROM item_content WHERE item_id = 7"))

	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, SettingContentPolicy)
		return err
	}))
	n, err = db.EnsureContentPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestStoredHTMLCleaningCoversRestoreStubsAndRecountsWords(t *testing.T) {
	ctx := context.Background()
	db, _ := openTest(t)
	feed := seedFeed(t, db)
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO items (id, feed_id, published_at, sort_at, uid, content_hash, text_hash, word_count)
			VALUES (7, ?, 1, 1, 'u7', 'c', 't', 99)`, feed); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_content (item_id, content_html, content_text) VALUES (7, ?, 'old')`, hostileHTML); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at) VALUES (8, ?, 'u8', 0, 1, 1)`, feed); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO trimmed_content (id, published_at, sort_at, word_count, content_hash, text_hash, url, title, author, content_html, content_text)
			VALUES (8, 1, 1, 99, 'c', 't', 'https://a/8', 't', 'a', ?, 'old')`, hostileHTML)
		return err
	}))

	n, err := db.EnsureContentPolicy(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, `<p>hi</p>`, scalar[string](t, db.Reader(), "SELECT content_html FROM trimmed_content WHERE id = 8"))
	require.Equal(t, "hi", scalar[string](t, db.Reader(), "SELECT content_text FROM trimmed_content WHERE id = 8"))
	require.EqualValues(t, 1, scalar[int64](t, db.Reader(), "SELECT word_count FROM trimmed_content WHERE id = 8"))
	require.EqualValues(t, 1, scalar[int64](t, db.Reader(), "SELECT word_count FROM items WHERE id = 7"))
}
