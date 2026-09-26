package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// undo0007 is the first step of every downgrade helper: a schema-6 database has no
// state_changed_at column and no index on it (the index goes first, it names the column).
const undo0007 = `DROP INDEX idx_items_state_changed;
ALTER TABLE items DROP COLUMN state_changed_at`

// sca reads state_changed_at; 0 means NULL.
func (e *env) sca(id int64) int64 {
	e.t.Helper()
	v := scalar[sql.NullInt64](e.t, e.db.Reader(), "SELECT state_changed_at FROM items WHERE id = ?", id)
	return v.Int64
}

func (e *env) idByTitle(title string) int64 {
	e.t.Helper()
	return int64(scalar[int](e.t, e.db.Reader(), "SELECT id FROM items WHERE title = ?", title))
}

func (e *env) write(fn func(ctx context.Context, tx *sql.Tx) error) {
	e.t.Helper()
	require.NoError(e.t, e.db.WithWrite(e.ctx, fn))
}

// trimWithStub moves an item into the ledger with a restore stub, the way retention does.
func (e *env) trimWithStub(id int64) {
	e.t.Helper()
	now := e.clk.Now().Unix()
	e.exec(`INSERT INTO trimmed_items (id, feed_id, uid, read, trimmed_at, last_seen_at)
		SELECT id, feed_id, uid, read, ?2, ?2 FROM items WHERE id = ?1`, id, now)
	e.exec(`INSERT INTO trimmed_content (id, published_at, updated_at, sort_at, word_count, content_hash, text_hash,
		url, title, author, image_url, origin_title, fulltext_mode, content_html, content_text, enclosures_json)
		SELECT i.id, i.published_at, i.updated_at, i.sort_at, i.word_count, i.content_hash, i.text_hash, i.url, i.title, i.author,
		  i.image_url, i.origin_title, i.fulltext_mode, c.content_html, c.content_text, c.enclosures_json
		FROM items i JOIN item_content c ON c.item_id = i.id WHERE i.id = ?`, id)
	e.exec("DELETE FROM items WHERE id = ?", id)
}

func TestMigration0007FreshSchema(t *testing.T) {
	db, _ := openTest(t)
	r := db.Reader()
	require.GreaterOrEqual(t, LatestVersion(), 7)
	require.True(t, columnNames(t, r, "items")["state_changed_at"])
	require.Contains(t, scalar[string](t, r, "SELECT sql FROM sqlite_master WHERE name = 'idx_items_state_changed'"),
		"WHERE state_changed_at IS NOT NULL")
}

// Ingest never sets state_changed_at, even when it sets the initial state (a star or
// mark-read rule); every later change to read or starred does, and a replay does not.
func TestStateChangedAtIngestAndItemState(t *testing.T) {
	e := newEnv(t)
	e.mkFilter(newFilter("star", "starme"))
	e.mkFilter(newFilter("mark_read", "readme"))
	fid := e.addFeed("http://a.example/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", fid)
	e.fetchBody(fid, frss(fnumbered(8, func(i int) string {
		switch i {
		case 0:
			return "starme 0"
		case 1:
			return "readme 1"
		}
		return fmt.Sprintf("plain %d", i)
	})...))
	require.Equal(t, 1, e.count("SELECT starred FROM items WHERE title = 'starme 0'"))
	require.Equal(t, 1, e.count("SELECT read FROM items WHERE title = 'readme 1'"))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE state_changed_at IS NOT NULL"), "ingest sets no state change")

	p := func(i int) int64 { return e.idByTitle(fmt.Sprintf("plain %d", i)) }
	step := func() int64 { e.clk.Advance(time.Minute); return e.clk.Now().Unix() }

	t1 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{p(2)}, true, t1); return err })
	require.Equal(t, t1, e.sca(p(2)), "read")
	t2 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{p(2)}, true, t2); return err })
	require.Equal(t, t1, e.sca(p(2)), "a replayed read is a no-op")
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{p(2)}, false, t2); return err })
	require.Equal(t, t2, e.sca(p(2)), "unread")

	t3 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetStarred(ctx, tx, []int64{p(3)}, true, t3); return err })
	require.Equal(t, t3, e.sca(p(3)), "star")
	t4 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetStarred(ctx, tx, []int64{p(3)}, false, t4); return err })
	require.Equal(t, t4, e.sca(p(3)), "unstar")
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetStarred(ctx, tx, []int64{p(3)}, false, step()); return err })
	require.Equal(t, t4, e.sca(p(3)), "a replayed unstar is a no-op")

	// A replayed offline star keeps its own time in starred_at; the change time is now.
	t5 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		_, err := SetStarredAt(ctx, tx, []int64{p(4)}, true, t5-3600, t5)
		return err
	})
	require.Equal(t, t5, e.sca(p(4)))
	require.Equal(t, t5-3600, int64(e.count("SELECT starred_at FROM items WHERE id = ?", p(4))))

	// Mark-all touches only what it flips: the rule-read item keeps NULL, p(2) (unread) is marked.
	t6 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkAllRead(ctx, tx, MarkScope{FeedID: fid}, math.MaxInt64, t6)
		return err
	})
	require.Equal(t, t6, e.sca(p(2)))
	require.Equal(t, t6, e.sca(p(5)))
	require.Zero(t, e.sca(e.idByTitle("readme 1")), "already read: untouched")

	// The UI's scoped mark goes through SetRead.
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{p(5), p(6)}, false, t6); return err })
	t7 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkScopeRead(ctx, tx, MarkScope{FeedID: fid}, MarkFilter{}, math.MaxInt64, t7)
		return err
	})
	require.Equal(t, t7, e.sca(p(5)))
	require.Equal(t, t7, e.sca(p(6)))

	// Restores from the ledger (mark unread, star) carry the change time.
	id5, id6 := p(5), p(6)
	e.trimWithStub(id5)
	e.trimWithStub(id6)
	var res StateResult
	t8 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) (err error) { res, err = SetRead(ctx, tx, []int64{id5}, false, t8); return err })
	require.Equal(t, []int64{id5}, res.Restored)
	require.Equal(t, t8, e.sca(id5))
	t9 := step()
	e.write(func(ctx context.Context, tx *sql.Tx) (err error) {
		res, err = SetStarredAt(ctx, tx, []int64{id6}, true, t9-60, t9)
		return err
	})
	require.Equal(t, []int64{id6}, res.Restored)
	require.Equal(t, t9, e.sca(id6))
	require.Equal(t, t9-60, int64(e.count("SELECT starred_at FROM items WHERE id = ?", id6)))
}

// A retro mute stamps only the items it turns read; un-mute to unread stamps only the ones it
// turns back to unread; the other un-mute modes change no state and stamp nothing.
func TestStateChangedAtFilterMuteAndUnmute(t *testing.T) {
	e := newEnv(t)
	e.seedRetro(10) // spam 0,2,4,6,8; ham 1,3,5,7,9
	t0 := e.clk.Now().Unix()
	pre := e.idByTitle("spam 0")
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{pre}, true, t0); return err })

	e.clk.Advance(time.Minute)
	t1 := e.clk.Now().Unix()
	spam := e.mkFilter(newFilter("mute", "spam"))
	_, err := e.db.ApplyFilter(e.ctx, spam.ID, true, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 5, e.count("SELECT count(*) FROM items WHERE muted_by = ?", spam.ID))
	require.Equal(t, t0, e.sca(pre), "muting a read item changes no state")
	require.Equal(t, t1, e.sca(e.idByTitle("spam 2")))
	require.Zero(t, e.sca(e.idByTitle("ham 1")))

	e.clk.Advance(time.Minute)
	t2 := e.clk.Now().Unix()
	_, ok, err := e.db.DeleteFilter(e.ctx, spam.ID, UnmuteUnread, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, t0, e.sca(pre), "read before the mute: stays read, unstamped")
	require.Equal(t, 1, e.count("SELECT read FROM items WHERE id = ?", pre))
	require.Equal(t, t2, e.sca(e.idByTitle("spam 2")), "back to unread")
	require.Equal(t, 0, e.count("SELECT read FROM items WHERE title = 'spam 2'"))

	e.clk.Advance(time.Minute)
	t3 := e.clk.Now().Unix()
	ham := e.mkFilter(newFilter("mute", "ham"))
	_, err = e.db.ApplyFilter(e.ctx, ham.ID, false, 0, nil, nil)
	require.NoError(t, err)
	require.Equal(t, t3, e.sca(e.idByTitle("ham 1")))
	e.clk.Advance(time.Minute)
	_, _, err = e.db.DeleteFilter(e.ctx, ham.ID, UnmuteRead, nil)
	require.NoError(t, err)
	require.Equal(t, t3, e.sca(e.idByTitle("ham 1")), "un-mute keeping read: no state change")
	require.Equal(t, 1, e.count("SELECT read FROM items WHERE title = 'ham 1'"))
}

func TestStateChangedAtAutoRead(t *testing.T) {
	e := newAREnv(t)
	e.setDays(e.feed, 1)
	old := e.item(e.feed, 10*24*h)
	fresh := e.item(e.feed, time.Hour)
	res, err := e.db.RunAutoRead(context.Background(), AutoReadOptions{Now: arNow})
	require.NoError(t, err)
	require.Equal(t, 1, res.Items)
	require.Equal(t, arNow.Unix(), scalar[int64](t, e.db.Reader(), "SELECT state_changed_at FROM items WHERE id = ?", old))
	require.Equal(t, 1, scalar[int](t, e.db.Reader(), "SELECT state_changed_at IS NULL FROM items WHERE id = ?", fresh))
}

// A populated schema-6 database migrates to 7: the column is backfilled from the later of
// read_at and starred_at (what the ot query matched before), the index exists and is used,
// and the FTS index and its triggers are untouched.
func TestMigration0007OnPopulatedSchema6(t *testing.T) {
	e := newEnv(t)
	fid := e.addFeed("https://ex.com/feed")
	e.exec("UPDATE feeds SET retention = 0 WHERE id = ?", fid)
	e.fetchBody(fid, rss(numbered(30)...))
	var ids []int64
	rows, err := e.db.Reader().QueryContext(e.ctx, "SELECT id FROM items ORDER BY id")
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Close())
	require.Len(t, ids, 30)
	want := map[int64]int{} // state_changed_at value -> rows expected
	for i, id := range ids {
		switch {
		case i == 15: // read after it was starred: the later time wins
			e.exec(`UPDATE items SET read = 1, read_at = 300, starred = 1, starred_at = 200 WHERE id = ?`, id)
			want[300]++
		case i%5 == 0:
			e.exec(`UPDATE items SET read = 1, read_at = 100, starred = 1, starred_at = 200 WHERE id = ?`, id)
			want[200]++
		case i%3 == 0:
			e.exec(`UPDATE items SET read = 1, read_at = 100 WHERE id = ?`, id)
			want[100]++
		case i%7 == 0: // unstarred-then-starred without a read
			e.exec(`UPDATE items SET starred = 1, starred_at = 50 WHERE id = ?`, id)
			want[50]++
		}
	}
	items := e.count("SELECT count(*) FROM items")
	fts := e.count("SELECT count(*) FROM items_fts")
	triggers := scalar[string](t, e.db.Reader(), "SELECT group_concat(name || ':' || sql, '|') FROM (SELECT name, sql FROM sqlite_master WHERE type = 'trigger' ORDER BY name)")
	e.exec(undo0007)
	e.exec(`PRAGMA user_version = 6`)
	require.False(t, columnNames(t, e.db.Reader(), "items")["state_changed_at"])
	path := scalar[string](t, e.db.Reader(), "SELECT file FROM pragma_database_list WHERE name = 'main'")
	require.NoError(t, e.db.Close())

	db, err := Open(e.ctx, Options{Path: path, Clock: e.clk})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	e.db = db
	v, err := db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, LatestVersion(), v)
	snaps, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backup", "pre-migration-6-*.db"))
	require.Len(t, snaps, 1)
	r := db.Reader()
	requireCleanIntegrity(t, r)
	require.Equal(t, items, e.count("SELECT count(*) FROM items"))
	require.Equal(t, fts, e.count("SELECT count(*) FROM items_fts"))
	require.Equal(t, triggers, scalar[string](t, r, "SELECT group_concat(name || ':' || sql, '|') FROM (SELECT name, sql FROM sqlite_master WHERE type = 'trigger' ORDER BY name)"))
	ftsCheck5(t, e, db)

	require.Zero(t, e.count("SELECT count(*) FROM items WHERE read_at IS NULL AND starred_at IS NULL AND state_changed_at IS NOT NULL"))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE (read_at IS NOT NULL OR starred_at IS NOT NULL) AND state_changed_at IS NULL"))
	for at, n := range want {
		require.Equal(t, n, e.count("SELECT count(*) FROM items WHERE state_changed_at = ?", at), "backfilled to %d", at)
	}

	// The new code paths and the FTS triggers work on the migrated file.
	id := int64(e.count("SELECT min(id) FROM items WHERE read = 0"))
	e.write(func(ctx context.Context, tx *sql.Tx) error { _, err := SetRead(ctx, tx, []int64{id}, true, 999); return err })
	require.EqualValues(t, 999, e.sca(id))
	e.exec("UPDATE items SET title = 'zebra crossing' WHERE id = ?", id)
	require.Equal(t, 1, e.count("SELECT count(*) FROM items_fts WHERE items_fts MATCH 'zebra'"))
	ftsCheck5(t, e, db)
	q, args := streamIDsSQL(StreamFilter{}, IDPage{N: 10, HasOT: true, OT: 1, UserChanges: true})
	rows, err = r.QueryContext(e.ctx, q, args...)
	require.NoError(t, err)
	require.NoError(t, rows.Close())
}

func TestMigration0007RollsBackOnFailure(t *testing.T) {
	e := newEnv(t)
	e.exec(undo0007)
	e.exec(`PRAGMA user_version = 6`)
	ms, err := loadMigrations()
	require.NoError(t, err)
	bad := append([]migration(nil), ms...)
	last := bad[6]
	bad[6] = migration{version: 7, name: last.name, sql: last.sql + "\nCREATE TABLE items (a);"}
	e.db.migrations = bad
	require.Error(t, e.db.migrate(e.ctx))
	v, err := e.db.Version(e.ctx)
	require.NoError(t, err)
	require.Equal(t, 6, v)
	require.False(t, columnNames(t, e.db.Reader(), "items")["state_changed_at"])
	require.Zero(t, e.count("SELECT count(*) FROM sqlite_master WHERE name = 'idx_items_state_changed'"))
	e.db.migrations = nil
	require.NoError(t, e.db.migrate(e.ctx))
	require.True(t, columnNames(t, e.db.Reader(), "items")["state_changed_at"])
}

// An older binary (six migrations) refuses a schema-7 database.
func TestOlderBinaryRefusesSchema7(t *testing.T) {
	db, _ := openTest(t)
	ms, err := loadMigrations()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ms), 7)
	db.migrations = ms[:6]
	require.ErrorContains(t, db.migrate(context.Background()), "newer than this binary")
}
