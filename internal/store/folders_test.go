package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// mkFolder creates a folder inside parent (0 = the top level) and returns its id.
func (e *env) mkFolder(parent int64, name string) int64 {
	e.t.Helper()
	f, err := e.db.CreateFolder(e.ctx, name, parent, -1)
	require.NoError(e.t, err)
	return f.ID
}

// chain creates nested folders, one per name, and returns their ids from the top.
func (e *env) chain(names ...string) []int64 {
	e.t.Helper()
	var ids []int64
	parent := int64(0)
	for _, n := range names {
		parent = e.mkFolder(parent, n)
		ids = append(ids, parent)
	}
	return ids
}

func (e *env) path(id int64) string {
	e.t.Helper()
	return scalar[string](e.t, e.db.Reader(), "SELECT path FROM folder_paths WHERE id = ?", id)
}

func (e *env) resolve(path string) int64 {
	e.t.Helper()
	var id int64
	require.NoError(e.t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		id, err = resolveFolderPath(ctx, tx, path)
		return err
	}))
	return id
}

func (e *env) move(id, parent int64) error {
	_, err := e.db.UpdateFolder(e.ctx, id, FolderPatch{Parent: &parent})
	return err
}

// checkFolderInvariants is what the writer promises after any sequence of changes: every folder
// reachable from the top (no cycle), at most MaxFolderDepth deep, the default folder at the top with
// no subfolders, and every full path unique ignoring ASCII case.
func checkFolderInvariants(t testing.TB, q Querier) {
	t.Helper()
	all := scalar[int](t, q, "SELECT count(*) FROM folders")
	require.Equal(t, all, scalar[int](t, q, "SELECT count(*) FROM folder_paths"), "every folder reachable from the top")
	require.LessOrEqual(t, scalar[int](t, q, "SELECT COALESCE(max(depth), 0) FROM folder_paths"), MaxFolderDepth)
	require.Equal(t, all, scalar[int](t, q, "SELECT count(DISTINCT lower(path)) FROM folder_paths"), "full paths unique")
	require.Zero(t, scalar[int](t, q, "SELECT count(*) FROM folders WHERE parent_id IN (SELECT id FROM folders WHERE is_default = 1)"))
	require.Zero(t, scalar[int](t, q, "SELECT count(*) FROM folders WHERE is_default = 1 AND parent_id IS NOT NULL"))
}

func TestFolderSiblingNamesUniqueIgnoringCase(t *testing.T) {
	e := newEnv(t)
	tech := e.mkFolder(0, "Tech")
	_, err := e.db.CreateFolder(e.ctx, "tech", 0, -1)
	require.ErrorIs(t, err, ErrFolderExists)
	news := e.mkFolder(tech, "news") // the same name at another level is another folder
	_, err = e.db.CreateFolder(e.ctx, "NEWS", tech, -1)
	require.ErrorIs(t, err, ErrFolderExists)
	top := e.mkFolder(0, "News")
	require.NotEqual(t, news, top)
	require.Equal(t, "Tech/news", e.path(news))
	// The database itself refuses a sibling clash a writer bug would let through.
	_, err = e.db.writer.ExecContext(e.ctx, "INSERT INTO folders (parent_id, name) VALUES (?, 'NeWs')", tech)
	require.ErrorContains(t, err, "UNIQUE")
	checkFolderInvariants(t, e.db.Reader())
}

// A literal top-level "AC/DC" and the folder DC inside AC would be one Reader API label.
func TestFolderPathsUniqueAcrossLevels(t *testing.T) {
	e := newEnv(t)
	acdc := e.mkFolder(0, "AC/DC")
	ac := e.mkFolder(0, "AC")
	_, err := e.db.CreateFolder(e.ctx, "dc", ac, -1)
	require.ErrorIs(t, err, ErrFolderExists)
	dc := e.mkFolder(ac, "DC2")
	name := "DC"
	_, err = e.db.UpdateFolder(e.ctx, dc, FolderPatch{Name: &name})
	require.ErrorIs(t, err, ErrFolderExists, "a rename onto a taken path")
	x := e.mkFolder(0, "X")
	require.NoError(t, e.move(e.mkFolder(x, "DC"), x))
	require.NoError(t, e.move(x, ac)) // AC/X/DC is free
	name = "AC/X"
	_, err = e.db.UpdateFolder(e.ctx, acdc, FolderPatch{Name: &name})
	require.ErrorIs(t, err, ErrFolderExists, "a rename onto a subfolder's path")
	// Renaming a folder in place to another spelling of its own name is not a clash.
	name = "ac/dc"
	_, err = e.db.UpdateFolder(e.ctx, acdc, FolderPatch{Name: &name})
	require.NoError(t, err)
	checkFolderInvariants(t, e.db.Reader())
}

// A move checks the whole subtree: a subfolder landing on a taken path refuses the move.
func TestFolderMoveChecksSubtreePaths(t *testing.T) {
	e := newEnv(t)
	a := e.chain("A", "B")
	e.mkFolder(0, "Z/A/B") // a literal top-level name equal to the path B would get
	z := e.mkFolder(0, "Z")
	require.ErrorIs(t, e.move(a[0], z), ErrFolderExists)
	require.Equal(t, "A/B", e.path(a[1]), "nothing moved")
	checkFolderInvariants(t, e.db.Reader())
}

func TestResolveFolderPathLongestPrefix(t *testing.T) {
	e := newEnv(t)
	lit := e.mkFolder(0, "A/B")
	c := e.resolve("A/B/C")
	require.Equal(t, "A/B/C", e.path(c))
	require.Equal(t, lit, scalar[int64](t, e.db.Reader(), "SELECT parent_id FROM folders WHERE id = ?", c))
	require.Zero(t, scalar[int](t, e.db.Reader(), "SELECT count(*) FROM folders WHERE name = 'A'"), "no orphan A beside the literal A/B")

	apple := e.resolve("Tech/Apple")
	require.Equal(t, "Tech/Apple", e.path(apple))
	require.Equal(t, apple, e.resolve("tech/apple"), "found again ignoring case")
	require.Equal(t, apple, e.resolve(" Tech/Apple "))
	// Only the outer ends of the whole path are trimmed: "Tech / Mac" is kept as written, a top-level
	// "Tech " holding " Mac", so the label a client sent is the label it gets back.
	mac := e.resolve("Tech / Mac")
	require.Equal(t, "Tech / Mac", e.path(mac))
	require.Equal(t, " Mac", scalar[string](t, e.db.Reader(), "SELECT name FROM folders WHERE id = ?", mac))
	require.Equal(t, int64(1), e.resolve(""))
	before := e.count("SELECT count(*) FROM folders")
	for bad, want := range map[string]error{"Tech/": ErrEmptyFolderSegment, "Tech//x": ErrEmptyFolderSegment, "/x": ErrEmptyFolderSegment,
		"New/ /x": ErrEmptyFolderSegment, "New/" + strings.Repeat("y", 101) + "/z": ErrBadFolderName, "New/a\x01/b": ErrBadFolderName} {
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := resolveFolderPath(ctx, tx, bad)
			require.ErrorIs(t, err, want, bad)
			require.True(t, FolderRefused(err), bad)
			return nil
		}))
	}
	require.Equal(t, before, e.count("SELECT count(*) FROM folders"), "a refused path creates nothing")
	checkFolderInvariants(t, e.db.Reader())
}

func TestFolderMoveRefusesCycles(t *testing.T) {
	e := newEnv(t)
	ids := e.chain("A", "B", "C")
	require.ErrorIs(t, e.move(ids[0], ids[2]), ErrFolderCycle)
	require.ErrorIs(t, e.move(ids[0], ids[0]), ErrFolderCycle)
	require.NoError(t, e.move(ids[2], 0))
	require.Equal(t, "C", e.path(ids[2]))
	require.NoError(t, e.move(ids[0], ids[2]))
	require.Equal(t, "C/A/B", e.path(ids[1]))
	checkFolderInvariants(t, e.db.Reader())
}

func TestFolderDepthCap(t *testing.T) {
	e := newEnv(t)
	names := make([]string, MaxFolderDepth)
	for i := range names {
		names[i] = fmt.Sprintf("L%d", i+1)
	}
	ids := e.chain(names...)
	_, err := e.db.CreateFolder(e.ctx, "L9", ids[MaxFolderDepth-1], -1)
	require.ErrorIs(t, err, ErrFolderDepth)
	// A two-level subtree fits under level 6 but not under level 7.
	sub := e.chain("S1", "S2")
	require.ErrorIs(t, e.move(sub[0], ids[6]), ErrFolderDepth)
	require.NoError(t, e.move(sub[0], ids[5]))
	require.Equal(t, 8, scalar[int](t, e.db.Reader(), "SELECT depth FROM folder_paths WHERE id = ?", sub[1]))
	// The Reader API path stops at the cap too.
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := resolveFolderPath(ctx, tx, strings.Join(names, "/")+"/L9")
		require.ErrorIs(t, err, ErrFolderDepth)
		return nil
	}))
	checkFolderInvariants(t, e.db.Reader())
}

func TestDefaultFolderStaysAtTheTop(t *testing.T) {
	e := newEnv(t)
	_, err := e.db.CreateFolder(e.ctx, "Child", 1, -1)
	require.ErrorIs(t, err, ErrFolderParent)
	x := e.mkFolder(0, "X")
	require.ErrorIs(t, e.move(1, x), ErrFolderParent)
	require.ErrorIs(t, e.move(x, 1), ErrFolderParent)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := resolveFolderPath(ctx, tx, "Uncategorized/Sub")
		require.ErrorIs(t, err, ErrFolderParent)
		return nil
	}))
	// The schema refuses it as well.
	_, err = e.db.writer.ExecContext(e.ctx, "UPDATE folders SET parent_id = ? WHERE id = 1", x)
	require.ErrorContains(t, err, "CHECK")
	checkFolderInvariants(t, e.db.Reader())
}

// Deleting a folder deletes its subtree, eight levels deep: every feed moves to the default folder,
// every folder filter goes, and the favorites and saved-search scopes of every id are dropped.
func TestDeleteFolderDeletesTheSubtree(t *testing.T) {
	e := newEnv(t)
	names := make([]string, MaxFolderDepth)
	for i := range names {
		names[i] = fmt.Sprintf("L%d", i+1)
	}
	ids := e.chain(names...)
	other := e.mkFolder(0, "Other")
	var feeds []int64
	for i, fid := range ids {
		feed := e.addFeed(fmt.Sprintf("http://f%d.example/feed", i))
		e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", fid, feed)
		feeds = append(feeds, feed)
	}
	keep := e.addFeed("http://keep.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", other, keep)
	for _, fid := range []int64{ids[0], ids[7], other} {
		f := newFilter("mute", "spam")
		f.Scope, f.FolderID = "folder", &fid
		e.mkFilter(f)
	}
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	require.NoError(t, e.db.SetSettings(e.ctx, map[string]any{
		SettingFavorites: []any{map[string]any{"t": "folder", "id": s(ids[3])}, map[string]any{"t": "folder", "id": s(other)}},
		"library.saved_searches": []any{
			map[string]any{"id": "a", "name": "deep", "q": "x", "scope": map[string]any{"folder_id": s(ids[7])}},
			map[string]any{"id": "b", "name": "other", "q": "x", "scope": map[string]any{"folder_id": s(other)}},
		},
	}))

	moved, err := e.db.DeleteFolder(e.ctx, ids[0])
	require.NoError(t, err)
	require.ElementsMatch(t, feeds, moved)
	require.Equal(t, len(feeds), e.count("SELECT count(*) FROM feeds WHERE folder_id = 1"))
	require.Equal(t, 2, e.count("SELECT count(*) FROM folders"), "Uncategorized and Other")
	require.Equal(t, 1, e.count("SELECT count(*) FROM filters"), "only Other's filter is left")
	favs := scalar[string](t, e.db.Reader(), "SELECT value FROM settings WHERE key = ?", SettingFavorites)
	require.JSONEq(t, fmt.Sprintf(`[{"t":"folder","id":"%d"}]`, other), favs)
	ss := scalar[string](t, e.db.Reader(), "SELECT value FROM settings WHERE key = 'library.saved_searches'")
	require.NotContains(t, ss, `"folder_id":"`+s(ids[7])+`"`)
	require.Contains(t, ss, `"folder_id":"`+s(other)+`"`)

	_, err = e.db.DeleteFolder(e.ctx, 1)
	require.ErrorIs(t, err, ErrDefaultFolder)
	_, err = e.db.DeleteFolder(e.ctx, ids[0])
	require.ErrorIs(t, err, ErrFolderNotFound)
	checkFolderInvariants(t, e.db.Reader())
}

// The cascade itself is the database's: a plain DELETE of the top folder removes all eight levels
// and sends their feeds to the default folder (feeds.folder_id ON DELETE SET DEFAULT).
func TestFolderDeleteCascadesInTheSchema(t *testing.T) {
	e := newEnv(t)
	names := make([]string, MaxFolderDepth)
	for i := range names {
		names[i] = fmt.Sprintf("L%d", i+1)
	}
	ids := e.chain(names...)
	feed := e.addFeed("http://deep.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", ids[7], feed)
	e.exec("DELETE FROM folders WHERE id = ?", ids[0])
	require.Equal(t, 1, e.count("SELECT count(*) FROM folders"))
	require.Equal(t, int64(1), scalar[int64](t, e.db.Reader(), "SELECT folder_id FROM feeds WHERE id = ?", feed))
}

func TestFolderRenameKeepsDescendantPaths(t *testing.T) {
	e := newEnv(t)
	ids := e.chain("Tech", "Apple", "Mac")
	name := "Gear"
	_, err := e.db.UpdateFolder(e.ctx, ids[0], FolderPatch{Name: &name})
	require.NoError(t, err)
	require.Equal(t, "Gear/Apple/Mac", e.path(ids[2]))
}

// The Reader API rename-tag: a dest path may rename, move or both; a merge of a folder with
// subfolders is refused and changes nothing.
func TestRenameLabelPaths(t *testing.T) {
	e := newEnv(t)
	tech := e.chain("Tech", "Apple")
	feed := e.addFeed("http://apple.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", tech[1], feed)

	// Tech/Apple -> Apple Stuff: to the top level.
	require.NoError(t, renameLabel(e.ctx, e.db, tech[1], "Apple Stuff"))
	require.Equal(t, "Apple Stuff", e.path(tech[1]))
	// back below Tech, and a case change of its own name
	require.NoError(t, renameLabel(e.ctx, e.db, tech[1], "Tech/apple"))
	require.Equal(t, "Tech/apple", e.path(tech[1]))
	require.NoError(t, renameLabel(e.ctx, e.db, tech[1], "TECH/Apple"))
	require.Equal(t, "Tech/Apple", e.path(tech[1]), "only its own name takes the new spelling")
	// Tech -> Gear: the subfolder follows.
	require.NoError(t, renameLabel(e.ctx, e.db, tech[0], "Gear"))
	require.Equal(t, "Gear/Apple", e.path(tech[1]))
	// A new parent path is created as subscribe would.
	require.NoError(t, renameLabel(e.ctx, e.db, tech[1], "Work/Clients/Apple"))
	require.Equal(t, "Work/Clients/Apple", e.path(tech[1]))
	// Moving a folder inside itself is refused.
	work := scalar[int64](t, e.db.Reader(), "SELECT id FROM folder_paths WHERE path = 'Work'")
	require.ErrorIs(t, renameLabel(e.ctx, e.db, work, "Work/Clients/Apple/Work"), ErrFolderCycle)
	require.Equal(t, "Work", e.path(work))

	// Merge: a leaf merges as before; a folder with subfolders is refused.
	other := e.mkFolder(0, "Other")
	require.ErrorIs(t, renameLabel(e.ctx, e.db, work, "Other"), ErrMergeSubfolders)
	require.Equal(t, "Work/Clients/Apple", e.path(tech[1]))
	require.NoError(t, renameLabel(e.ctx, e.db, tech[1], "Other"))
	require.Equal(t, other, scalar[int64](t, e.db.Reader(), "SELECT folder_id FROM feeds WHERE id = ?", feed))
	require.Zero(t, e.count("SELECT count(*) FROM folders WHERE id = ?", tech[1]))
	checkFolderInvariants(t, e.db.Reader())
}

// The web scope of a folder is its subtree: its list, search, mark-read and unread count agree.
// The Reader API's label scope (StreamFilter, MarkAllRead, UnreadCounts) is the folder's own feeds.
func TestFolderScopes(t *testing.T) {
	e := newEnv(t)
	ids := e.chain("Tech", "Apple")
	top := e.addFeed("http://tech.example/feed")
	deep := e.addFeed("http://apple.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", ids[0], top)
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", ids[1], deep)
	e.fetchBody(top, frss(fspec{guid: "t1", title: "tech one"}, fspec{guid: "t2", title: "tech two"}))
	e.fetchBody(deep, frss(fspec{guid: "a1", title: "apple one"}, fspec{guid: "a2", title: "apple two"}, fspec{guid: "a3", title: "apple three"}))

	cards, _, err := e.db.ListCards(e.ctx, CardQuery{View: "unread", FolderID: ids[0], Limit: 50})
	require.NoError(t, err)
	require.Len(t, cards, 5, "the parent's list holds the subfolder's items")
	cards, _, err = e.db.ListCards(e.ctx, CardQuery{View: "unread", FolderID: ids[1], Limit: 50})
	require.NoError(t, err)
	require.Len(t, cards, 3)
	cards, _, _, err = e.db.ListCardsFB(e.ctx, CardQuery{View: "all", FolderID: ids[0], Query: "apple", Limit: 50})
	require.NoError(t, err)
	require.Len(t, cards, 3, "search in the parent finds the subfolder's items")

	folders, err := e.db.UIFolders(e.ctx)
	require.NoError(t, err)
	unread := map[int64]int64{}
	for _, f := range folders {
		unread[f.ID] = f.Unread
	}
	require.Equal(t, int64(5), unread[ids[0]], "a folder's count counts what its list shows")
	require.Equal(t, int64(3), unread[ids[1]])
	fo, err := e.db.UpdateFolder(e.ctx, ids[0], FolderPatch{})
	require.NoError(t, err)
	require.Equal(t, int64(5), fo.Unread)

	var labelIDs []int64
	_, _, err = e.db.StreamIDs(e.ctx, StreamFilter{FolderID: ids[0]}, IDPage{N: 100}, func(id int64) error {
		labelIDs = append(labelIDs, id)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, labelIDs, 2, "a label stream holds its own feeds only")
	rows, err := e.db.UnreadCounts(e.ctx, 0)
	require.NoError(t, err)
	for _, r := range rows {
		require.Equal(t, map[int64]string{top: "Tech", deep: "Tech/Apple"}[r.FeedID], r.Folder)
	}

	// Reader API mark-all on the parent label leaves the subfolder alone; the web scope takes the subtree.
	now := time.Now().Unix()
	max, err := e.db.MaxCommittedID(e.ctx)
	require.NoError(t, err)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkAllRead(ctx, tx, MarkScope{FolderID: ids[0]}, max, now)
		return err
	}))
	require.Equal(t, 3, e.count("SELECT count(*) FROM items WHERE read = 0"))
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkScopeRead(ctx, tx, MarkScope{FolderTreeID: ids[0]}, MarkFilter{}, max, now)
		return err
	}))
	require.Zero(t, e.count("SELECT count(*) FROM items WHERE read = 0"))
}

// A folder filter covers the feeds of its subfolders, at ingest and retroactively, and stops
// covering a feed that moves out.
func TestFolderFilterCoversSubfolders(t *testing.T) {
	e := newEnv(t)
	ids := e.chain("Tech", "Apple", "Mac")
	feed := e.addFeed("http://mac.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", ids[2], feed)
	f := newFilter("mute", "spam")
	f.Scope, f.FolderID = "folder", &ids[0]
	e.mkFilter(f)

	e.fetchBody(feed, frss(fspec{guid: "s1", title: "spam one"}, fspec{guid: "h1", title: "ham one"}))
	require.Equal(t, []string{"spam one"}, e.titles("muted_by IS NOT NULL"), "muted at ingest two levels down")

	res, err := e.db.PreviewFilter(e.ctx, newFilterScoped(ids[0], "ham"), true, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, 1, res.Matches, "the retroactive scan covers the subtree")

	other := e.mkFolder(0, "Other")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", other, feed)
	e.fetchBody(feed, frss(fspec{guid: "s2", title: "spam two"}))
	require.Equal(t, []string{"spam one"}, e.titles("muted_by IS NOT NULL"), "not after the feed moved out")
	res, err = e.db.PreviewFilter(e.ctx, newFilterScoped(ids[0], "ham"), true, 5*time.Second)
	require.NoError(t, err)
	require.Zero(t, res.Matches)
}

func newFilterScoped(folder int64, terms ...string) Filter {
	f := newFilter("mute", terms...)
	f.Scope, f.FolderID = "folder", &folder
	return f
}

// Moving a folder recompiles nothing but changes which feeds a folder rule covers: the next ingest sees it.
func TestFolderMoveChangesFilterCoverage(t *testing.T) {
	e := newEnv(t)
	a := e.mkFolder(0, "A")
	b := e.mkFolder(0, "B")
	feed := e.addFeed("http://b.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", b, feed)
	e.mkFilter(newFilterScoped(a, "spam"))
	e.fetchBody(feed, frss(fspec{guid: "s1", title: "spam one"}))
	require.Empty(t, e.titles("muted_by IS NOT NULL"))
	require.NoError(t, e.move(b, a))
	e.fetchBody(feed, frss(fspec{guid: "s1", title: "spam one"}, fspec{guid: "s2", title: "spam two"}))
	require.Equal(t, []string{"spam two"}, e.titles("muted_by IS NOT NULL"))
}

// Bootstrap folders carry parent_id and list the tree in pre-order, siblings by position.
func TestUIFoldersTreeOrder(t *testing.T) {
	e := newEnv(t)
	b, err := e.db.CreateFolder(e.ctx, "B", 0, 20)
	require.NoError(t, err)
	a, err := e.db.CreateFolder(e.ctx, "A", 0, 10)
	require.NoError(t, err)
	_, err = e.db.CreateFolder(e.ctx, "B2", b.ID, 1)
	require.NoError(t, err)
	_, err = e.db.CreateFolder(e.ctx, "B1", b.ID, 0)
	require.NoError(t, err)
	_, err = e.db.CreateFolder(e.ctx, "a1", a.ID, 99)
	require.NoError(t, err)
	folders, err := e.db.UIFolders(e.ctx)
	require.NoError(t, err)
	var got []string
	for _, f := range folders {
		p := "-"
		if f.ParentID != nil {
			p = strconv.FormatInt(*f.ParentID, 10)
		}
		got = append(got, f.Name+"<"+p)
	}
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	require.Equal(t, []string{"Uncategorized<-", "A<-", "a1<" + id(a.ID), "B<-", "B1<" + id(b.ID), "B2<" + id(b.ID)}, got)
}

// OPML's explicit chains: a literal top-level "AC/DC" takes the chain AC > DC without an empty "AC"
// being created on the way, and the chain still walks real levels when they exist.
func TestEnsureFolderChainPrefersAWholeLiteralPath(t *testing.T) {
	e := newEnv(t)
	acdc := e.mkFolder(0, "AC/DC")
	chain := func(segs ...string) (int64, int) {
		var id int64
		var n int
		require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			id, n, err = EnsureFolderChain(ctx, tx, segs)
			return err
		}))
		return id, n
	}
	id, n := chain("AC", "DC")
	require.Equal(t, acdc, id)
	require.Zero(t, n)
	require.Zero(t, e.count("SELECT count(*) FROM folders WHERE name = 'AC'"), "no empty AC left behind")
	id, n = chain("AC", "DC", "Live")
	require.Equal(t, "AC/DC/Live", e.path(id))
	require.Equal(t, 1, n)
	require.Zero(t, e.count("SELECT count(*) FROM folders WHERE name = 'AC'"))
	id, n = chain("Music", "Rock")
	require.Equal(t, "Music/Rock", e.path(id))
	require.Equal(t, 2, n)
	checkFolderInvariants(t, e.db.Reader())
}

// The two mark-read functions each refuse the other's folder scope.
func TestMarkScopeFolderFieldsBelongToOneFunctionEach(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.db.WithWrite(e.ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkAllRead(ctx, tx, MarkScope{FolderTreeID: 1}, 1, 1)
		require.Error(t, err)
		_, err = MarkScopeRead(ctx, tx, MarkScope{FolderID: 1}, MarkFilter{}, 1, 1)
		require.Error(t, err)
		return nil
	}))
}

// A rename-tag of a folder deleted meanwhile is ErrFolderNotFound (the Reader API answers OK), and a
// dest with an empty level is refused before anything changes.
func TestRenameLabelMissingAndEmptyLevel(t *testing.T) {
	e := newEnv(t)
	x := e.mkFolder(0, "X")
	_, err := e.db.RenameLabel(e.ctx, 999, "Y")
	require.ErrorIs(t, err, ErrFolderNotFound)
	for _, dest := range []string{"Y/", "Y//Z", "/Y"} {
		_, err = e.db.RenameLabel(e.ctx, x, dest)
		require.ErrorIs(t, err, ErrEmptyFolderSegment, dest)
	}
	require.Equal(t, 2, e.count("SELECT count(*) FROM folders"))
	require.Equal(t, "X", e.path(x))
}

// A folder the view cannot reach (a cycle written behind the writer's back) never hides its feeds
// from the Reader API: they are listed with an empty label instead of vanishing.
func TestFeedsOfAnUnreachableFolderStayListed(t *testing.T) {
	e := newEnv(t)
	a := e.mkFolder(0, "A")
	b := e.mkFolder(a, "B")
	feed := e.addFeed("http://cycle.example/feed")
	e.exec("UPDATE feeds SET folder_id = ? WHERE id = ?", b, feed)
	e.fetchBody(feed, frss(fspec{guid: "c1", title: "one"}))
	e.exec("UPDATE folders SET parent_id = ? WHERE id = ?", b, a)
	subs, err := e.db.Subscriptions(e.ctx)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, "", subs[0].Folder)
	rows, err := e.db.UnreadCounts(e.ctx, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var got []string
	ids, err := queryIDs(e.ctx, e.db.Reader(), "SELECT id FROM items")
	require.NoError(t, err)
	require.NoError(t, e.db.StreamItems(e.ctx, ids, false, 0, func(r *ContentRow) error { got = append(got, r.Folder); return nil }))
	require.Equal(t, []string{""}, got)
}
