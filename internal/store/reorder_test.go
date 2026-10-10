package store

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// treeState is every folder's parent and position and every feed's folder and position: what a
// Reorder may change.
func (e *env) treeState() string {
	e.t.Helper()
	rows, err := e.db.Reader().QueryContext(e.ctx, `SELECT 'folder', id, ifnull(parent_id, 0), position FROM folders
		UNION ALL SELECT 'feed', id, folder_id, position FROM feeds ORDER BY 1, 2`)
	require.NoError(e.t, err)
	defer rows.Close()
	var out string
	for rows.Next() {
		var kind string
		var id, parent, pos int64
		require.NoError(e.t, rows.Scan(&kind, &id, &parent, &pos))
		out += fmt.Sprintf("%s %d in %d at %d\n", kind, id, parent, pos)
	}
	require.NoError(e.t, rows.Err())
	return out
}

func under(id, parent int64) FolderOrder { return FolderOrder{ID: id, Parent: &parent} }

func keep(id int64) FolderOrder { return FolderOrder{ID: id} }

// A folder move and the new order are one Reorder: the parent and every position land together, and
// a folder listed without a parent keeps its own.
func TestReorderMovesAFolderWithItsOrder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ab := e.chain("A", "B")
	c := e.mkFolder(0, "C")
	res, err := e.db.Reorder(e.ctx, []FolderOrder{keep(1), keep(ab[0]), under(ab[1], 0), keep(c)}, nil)
	require.NoError(t, err)
	require.Equal(t, "B", e.path(ab[1]))
	require.Contains(t, res.Folders, ab[1])
	for i, id := range []int64{1, ab[0], ab[1], c} {
		require.Equal(t, int64(i), scalar[int64](t, e.db.Reader(), "SELECT position FROM folders WHERE id = ?", id))
	}
	// The same request again changes nothing.
	res, err = e.db.Reorder(e.ctx, []FolderOrder{keep(1), keep(ab[0]), under(ab[1], 0), keep(c)}, nil)
	require.NoError(t, err)
	require.Empty(t, res.Folders)
	checkFolderInvariants(t, e.db.Reader())
}

// Each move is checked against the tree the earlier entries left, not the final tree: swapping B and
// A (B under A while A is still under B) is refused in that order, and goes through when an earlier
// entry has taken A out first.
func TestReorderChecksEachMoveAgainstTheTreeSoFar(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ba := e.chain("B", "A")
	b, a := ba[0], ba[1]
	_, err := e.db.Reorder(e.ctx, []FolderOrder{under(b, a), under(a, 0)}, nil)
	require.ErrorIs(t, err, ErrFolderCycle)
	_, err = e.db.Reorder(e.ctx, []FolderOrder{keep(1), under(a, 0), under(b, a)}, nil)
	require.NoError(t, err)
	require.Equal(t, "A/B", e.path(b))
	checkFolderInvariants(t, e.db.Reader())
}

// A move in a Reorder is refused by the folder writer's own rules, and a refusal anywhere leaves
// nothing changed: not the positions and moves listed before it, nor the feed lists.
func TestReorderRefusedMoveChangesNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	names := make([]string, MaxFolderDepth-1)
	for i := range names {
		names[i] = fmt.Sprintf("L%d", i+1)
	}
	deep := e.chain(names...) // seven levels
	sub := e.chain("S1", "S2")
	x := e.mkFolder(0, "X")
	e.mkFolder(x, "Apple")
	apple := e.mkFolder(0, "Apple")
	good := e.chain("G1", "G2")
	other := e.mkFolder(0, "Other")
	feed := e.addFeed("http://a.example/feed")

	for name, tc := range map[string]struct {
		move FolderOrder
		want error // nil: a bad request (*ErrReorder)
	}{
		"cycle":                {under(deep[0], deep[3]), ErrFolderCycle},
		"inside itself":        {under(x, x), ErrFolderCycle},
		"too deep":             {under(sub[0], deep[6]), ErrFolderDepth},
		"default folder moved": {under(1, x), ErrFolderParent},
		"into the default":     {under(x, 1), ErrFolderParent},
		"path taken":           {under(apple, x), ErrFolderExists},
		"no such parent":       {under(x, 999_999), ErrParentNotFound},
		"no such folder":       {under(999_999, 0), nil},
		"listed twice":         {keep(other), nil},
	} {
		before := e.treeState()
		// A reposition and a good move before the refused one, and a feed list after it.
		folders := []FolderOrder{keep(other), under(good[1], 0), tc.move}
		_, err := e.db.Reorder(e.ctx, folders, []FeedOrder{{FolderID: other, IDs: []int64{feed}}})
		if tc.want == nil {
			var re *ErrReorder
			require.ErrorAs(t, err, &re, name)
		} else {
			require.ErrorIs(t, err, tc.want, name)
		}
		require.Equal(t, before, e.treeState(), name)
	}
	// The same request without the refused move goes through.
	_, err := e.db.Reorder(e.ctx, []FolderOrder{keep(other), under(good[1], 0)}, []FeedOrder{{FolderID: other, IDs: []int64{feed}}})
	require.NoError(t, err)
	require.Equal(t, "G2", e.path(good[1]))
	require.Equal(t, other, scalar[int64](t, e.db.Reader(), "SELECT folder_id FROM feeds WHERE id = ?", feed))
	checkFolderInvariants(t, e.db.Reader())
}

// The archive feed in a feed list refuses the whole Reorder, a good folder move listed before it too.
func TestReorderArchiveFeedChangesNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ab := e.chain("A", "B")
	feed := e.addFeed("http://a.example/feed")
	e.exec(`INSERT INTO feeds (folder_id, url, url_key, host, enabled, disabled_reason, retention) VALUES (1,'kipple:archive','kipple:archive','kipple.invalid',0,'archive',0)`)
	arch := scalar[int64](t, e.db.Reader(), "SELECT id FROM feeds WHERE disabled_reason = 'archive'")
	before := e.treeState()
	_, err := e.db.Reorder(e.ctx, []FolderOrder{keep(1), keep(ab[0]), under(ab[1], 0)}, []FeedOrder{{FolderID: ab[0], IDs: []int64{feed, arch}}})
	require.ErrorIs(t, err, ErrArchiveFeed)
	require.Equal(t, before, e.treeState())
	require.Equal(t, "A/B", e.path(ab[1]))
}

// Moving a folder in a Reorder changes which feeds a folder filter covers, as a PATCH move does.
func TestReorderMoveChangesFilterCoverage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.mkFolder(0, "A")
	b := e.mkFolder(0, "B")
	feed := e.addFeed("http://b.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", b, feed)
	e.mkFilter(newFilterScoped(a, "spam"))
	e.fetchBody(feed, frss(fspec{guid: "s1", title: "spam one"}))
	require.Empty(t, e.titles("muted_by IS NOT NULL"))
	_, err := e.db.Reorder(e.ctx, []FolderOrder{keep(1), keep(a), under(b, a)}, nil)
	require.NoError(t, err)
	e.fetchBody(feed, frss(fspec{guid: "s1", title: "spam one"}, fspec{guid: "s2", title: "spam two"}))
	require.Equal(t, []string{"spam two"}, e.titles("muted_by IS NOT NULL"))
}
