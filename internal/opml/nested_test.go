package opml

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

// folderOf returns the full path of the folder a feed is in.
func folderOf(t *testing.T, db *store.DB, url string) string {
	t.Helper()
	var p string
	require.NoError(t, db.Reader().QueryRow(`SELECT fp.path FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id WHERE f.url = ?`, url).Scan(&p))
	return p
}

func paths(t *testing.T, db *store.DB) []string {
	t.Helper()
	rows, err := db.Reader().Query("SELECT path FROM folder_paths WHERE id != 1 ORDER BY sort_key")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestParseKeepsTheTree(t *testing.T) {
	d := parseString(t, `<opml><body>
	<outline text="Tech"><outline text="News"><outline xmlUrl="https://t.test/f"/></outline></outline>
	<outline text="Sports"><outline text="News"><outline xmlUrl="https://s.test/f"/></outline></outline>
	<outline text="A"><outline text="a"><outline xmlUrl="https://aa.test/f"/></outline></outline>
	<outline text="tech"><outline text="news"><outline xmlUrl="https://t2.test/f"/></outline><outline text="Gadgets"/></outline>
	</body></opml>`)
	require.Equal(t, [][]string{{"Tech"}, {"Tech", "News"}, {"Sports"}, {"Sports", "News"}, {"A"}, {"A", "a"}, {"Tech", "Gadgets"}}, d.Folders,
		"the same name under different parents stays apart; A > a is two levels; a case variant merges into its sibling")
	require.Equal(t, []string{"Tech", "News"}, d.Feeds[0].Folder)
	require.Equal(t, []string{"Sports", "News"}, d.Feeds[1].Folder)
	require.Equal(t, []string{"A", "a"}, d.Feeds[2].Folder)
	require.Equal(t, []string{"Tech", "News"}, d.Feeds[3].Folder, "first spelling kept")
	require.Equal(t, []MergedCase{{"Tech", "tech"}, {"Tech/News", "Tech/news"}}, d.FoldersMergedCase)
}

func TestParseAndImportTooDeep(t *testing.T) {
	var b strings.Builder
	b.WriteString("<opml><body>")
	for i := 1; i <= 10; i++ {
		b.WriteString(`<outline text="L` + string(rune('0'+i%10)) + `">`)
		if i == 9 {
			b.WriteString(`<outline xmlUrl="https://nine.test/f"/>`)
		}
	}
	b.WriteString(`<outline xmlUrl="https://ten.test/f"/>`)
	b.WriteString(strings.Repeat("</outline>", 10) + "</body></opml>")
	d := parseString(t, b.String())
	require.Len(t, d.Folders, store.MaxFolderDepth)
	require.Len(t, d.Feeds[0].Folder, store.MaxFolderDepth, "a feed below the cap goes into its level-8 ancestor")
	require.Len(t, d.Feeds[1].Folder, store.MaxFolderDepth)
	require.Len(t, d.FoldersRefused, 1, "only the topmost too-deep outline; what is below it is covered by it")
	require.Equal(t, "L1/L2/L3/L4/L5/L6/L7/L8/L9", d.FoldersRefused[0].Path)
	require.Equal(t, "folders nest at most 8 levels deep", d.FoldersRefused[0].Reason)
	require.True(t, d.Feeds[0].cut)
	require.True(t, d.Feeds[1].cut)

	db := openDB(t)
	r, err := Import(context.Background(), db, d, ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, r.FeedsAdded)
	require.Equal(t, store.MaxFolderDepth, r.FoldersCreated)
	require.Equal(t, d.FoldersRefused, r.FoldersRefused)
	require.Equal(t, "L1/L2/L3/L4/L5/L6/L7/L8", folderOf(t, db, "https://nine.test/f"))
	require.Equal(t, "L1/L2/L3/L4/L5/L6/L7/L8", folderOf(t, db, "https://ten.test/f"))
}

// A folder the writer refuses (a bad name, a subfolder of Uncategorized) is reported once, at its
// top; its feeds and its subfolders' feeds go into the deepest ancestor that was kept.
func TestImportRefusedFolderFallsBackToAncestor(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml><body>
	<outline text="Uncategorized"><outline text="X"><outline xmlUrl="https://u.test/f"/></outline></outline>
	<outline text="Good"><outline text="Bad&#127;"><outline text="Kid"><outline xmlUrl="https://b.test/f"/></outline></outline>
	  <outline xmlUrl="https://g.test/f"/></outline>
	</body></opml>`, ImportOptions{})
	require.Equal(t, 3, r.FeedsAdded)
	require.Empty(t, r.Skipped)
	require.Equal(t, 1, r.FoldersCreated, "only Good")
	require.Len(t, r.FoldersRefused, 2, "%v", r.FoldersRefused)
	require.Equal(t, "Uncategorized/X", r.FoldersRefused[0].Path)
	require.Equal(t, "Good/Bad\x7f", r.FoldersRefused[1].Path)
	require.Equal(t, "Uncategorized", folderOf(t, db, "https://u.test/f"))
	require.Equal(t, "Good", folderOf(t, db, "https://b.test/f"))
	require.Equal(t, "Good", folderOf(t, db, "https://g.test/f"))
}

func TestImportNestedSameNamesStayDistinct(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml><body>
	<outline text="Tech"><outline text="News"><outline xmlUrl="https://t.test/f"/></outline></outline>
	<outline text="Sports"><outline text="News"><outline xmlUrl="https://s.test/f"/></outline></outline>
	</body></opml>`, ImportOptions{})
	require.Equal(t, 4, r.FoldersCreated)
	require.Equal(t, "Tech/News", folderOf(t, db, "https://t.test/f"))
	require.Equal(t, "Sports/News", folderOf(t, db, "https://s.test/f"))

	db2 := openDB(t)
	importString(t, db2, export(t, db), ImportOptions{})
	require.Equal(t, paths(t, db), paths(t, db2))
	require.Equal(t, "Sports/News", folderOf(t, db2, "https://s.test/f"))
}

func TestExportNestsTheTree(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body>
	<outline text="Tech"><outline xmlUrl="https://t.test/f" text="T"/><outline text="Apple"><outline xmlUrl="https://a.test/f" text="A"/></outline></outline>
	<outline text="Music"><outline text="AC/DC"><outline xmlUrl="https://acdc.test/f" text="AC"/></outline></outline>
	<outline text="Empty"/>
	</body></opml>`, ImportOptions{})
	out := export(t, db)
	require.Contains(t, out, `    <outline text="Tech" title="Tech">
      <outline type="rss" text="T" title="T" xmlUrl="https://t.test/f"/>
      <outline text="Apple" title="Apple">
        <outline type="rss" text="A" title="A" xmlUrl="https://a.test/f"/>
      </outline>
    </outline>
    <outline text="Music" title="Music">
      <outline text="AC/DC" title="AC/DC">
        <outline type="rss" text="AC" title="AC" xmlUrl="https://acdc.test/f"/>
      </outline>
    </outline>
    <outline text="Empty" title="Empty">
    </outline>
  </body>`)

	// A literal '/' in a name round-trips as one folder, not as AC > DC.
	db2 := openDB(t)
	importString(t, db2, out, ImportOptions{})
	require.Equal(t, []string{"Tech", "Tech/Apple", "Music", "Music/AC/DC", "Empty"}, paths(t, db2))
	var depth int
	require.NoError(t, db2.Reader().QueryRow("SELECT depth FROM folder_paths WHERE path = 'Music/AC/DC'").Scan(&depth))
	require.Equal(t, 2, depth)
	require.Equal(t, out, export(t, db2))
}

// Siblings are exported in folder order, whatever order they were created in.
func TestExportSiblingsInPositionOrder(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="P"><outline text="B"/><outline text="A"/></outline></body></opml>`, ImportOptions{})
	ctx := context.Background()
	var a, b int64
	require.NoError(t, db.Reader().QueryRow("SELECT id FROM folder_paths WHERE path = 'P/A'").Scan(&a))
	require.NoError(t, db.Reader().QueryRow("SELECT id FROM folder_paths WHERE path = 'P/B'").Scan(&b))
	pos := int64(0)
	_, err := db.UpdateFolder(ctx, a, store.FolderPatch{Position: &pos})
	require.NoError(t, err)
	out := export(t, db)
	require.Less(t, strings.Index(out, `text="A"`), strings.Index(out, `text="B"`))
}

func TestRoundTripNested(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "nested.opml"))
	require.NoError(t, err)
	roundTrip(t, src, 38, 19)

	db := openDB(t)
	importString(t, db, string(src), ImportOptions{})
	require.Equal(t, []string{
		"Tech", "Tech/Apple", "Tech/Apple/Mac", "Tech/Apple/Mac/Pro Apps", "Tech/News", "Tech/Linux", "Tech/Linux/Kernel",
		"Sports", "Sports/News", "Sports/Football", "Sports/Football/Clubs & Leagues",
		"Music", "Music/AC/DC", "Music/Jazz", "Empty Folder", "Reading", "Reading/Long reads", "Science", "Science/Space",
	}, paths(t, db))
}

// Without MoveExisting a re-import leaves existing feeds where they are; with it, they move into the
// file's folders, and a folder left empty is reported, not deleted.
func TestImportMoveExisting(t *testing.T) {
	db := openDB(t)
	flat := `<opml><body>
	<outline text="Apple"><outline xmlUrl="https://a.test/f"/><outline xmlUrl="https://b.test/f"/></outline>
	<outline text="Misc"><outline xmlUrl="https://m.test/f"/></outline>
	</body></opml>`
	importString(t, db, flat, ImportOptions{})
	nested := `<opml><body>
	<outline text="Tech"><outline text="Apple"><outline xmlUrl="https://a.test/f"/><outline xmlUrl="http://b.test/f"/></outline></outline>
	<outline text="Misc"><outline xmlUrl="https://m.test/f"/></outline>
	<outline xmlUrl="https://new.test/f"/>
	</body></opml>`

	r := importString(t, db, nested, ImportOptions{})
	require.Len(t, r.FeedsExisting, 3)
	require.Empty(t, r.FeedsMoved)
	require.Empty(t, r.FoldersEmptied)
	require.Equal(t, "Apple", folderOf(t, db, "https://a.test/f"), "off: existing feeds stay put")
	require.Equal(t, 2, r.FoldersCreated, "Tech and Tech/Apple are still created")

	r = importString(t, db, nested, ImportOptions{MoveExisting: true})
	require.Zero(t, r.FeedsAdded)
	require.Zero(t, r.FoldersCreated)
	require.Len(t, r.FeedsMoved, 2, "%v", r.FeedsMoved)
	require.Equal(t, "https://a.test/f", r.FeedsMoved[0].URL)
	require.Equal(t, "Tech/Apple", folderOf(t, db, "https://a.test/f"))
	require.Equal(t, "Tech/Apple", folderOf(t, db, "https://b.test/f"))
	require.Equal(t, "Misc", folderOf(t, db, "https://m.test/f"), "already in place: not moved")
	require.Equal(t, []string{"Apple"}, r.FoldersEmptied)
	require.Contains(t, paths(t, db), "Apple", "an emptied folder is kept")

	// Moving a feed listed at the root of the file takes it to Uncategorized.
	r = importString(t, db, `<opml><body><outline xmlUrl="https://m.test/f"/></body></opml>`, ImportOptions{MoveExisting: true})
	require.Len(t, r.FeedsMoved, 1)
	require.Equal(t, "Uncategorized", folderOf(t, db, "https://m.test/f"))
	require.Equal(t, []string{"Misc"}, r.FoldersEmptied)
}

// Folder lookups use the sibling index, so a large tree imports in one write well inside the writer's
// timeout (a lookup per folder through folder_paths made this quadratic).
func TestImportManyFolders(t *testing.T) {
	var b strings.Builder
	b.WriteString("<opml><body>")
	n := 0
	for i := 0; i < 1250; i++ {
		fmt.Fprintf(&b, `<outline text="Top %d">`, i)
		for j := 0; j < 3; j++ {
			n++
			fmt.Fprintf(&b, `<outline text="Sub %d"><outline xmlUrl="https://f%d.test/rss"/></outline>`, j, n)
		}
		b.WriteString("</outline>")
	}
	b.WriteString("</body></opml>")
	db := openDB(t)
	start := time.Now()
	r := importString(t, db, b.String(), ImportOptions{})
	t.Logf("5000 folders, %d feeds: %v", n, time.Since(start))
	require.Equal(t, 5000, r.FoldersCreated)
	require.Equal(t, n, r.FeedsAdded)
	require.Equal(t, "Top 1249/Sub 2", folderOf(t, db, fmt.Sprintf("https://f%d.test/rss", n)))

	start = time.Now()
	r = importString(t, db, b.String(), ImportOptions{MoveExisting: true})
	t.Logf("re-import: %v", time.Since(start))
	require.Zero(t, r.FoldersCreated)
	require.Empty(t, r.FeedsMoved)
}

// With MoveExisting, a feed whose folder in the file was refused (a bad name, too deep) stays where it
// is instead of falling back to an ancestor or Uncategorized.
func TestImportMoveExistingSkipsRefusedFolders(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="Keep">
	<outline xmlUrl="https://bad.test/f"/><outline xmlUrl="https://deep.test/f"/><outline xmlUrl="https://ok.test/f"/>
	</outline></body></opml>`, ImportOptions{})
	deep := `<outline xmlUrl="https://deep.test/f"/>`
	for i := 9; i >= 1; i-- {
		deep = fmt.Sprintf(`<outline text="D%d">%s</outline>`, i, deep)
	}
	r := importString(t, db, `<opml><body>
	<outline text="Good"><outline text="Bad&#127;"><outline xmlUrl="https://bad.test/f"/></outline><outline xmlUrl="https://ok.test/f"/></outline>
	`+deep+`</body></opml>`, ImportOptions{MoveExisting: true})
	require.Len(t, r.FoldersRefused, 2, "%v", r.FoldersRefused)
	require.Len(t, r.FeedsMoved, 1)
	require.Equal(t, "https://ok.test/f", r.FeedsMoved[0].URL)
	require.Equal(t, "Keep", folderOf(t, db, "https://bad.test/f"))
	require.Equal(t, "Keep", folderOf(t, db, "https://deep.test/f"))
	require.Equal(t, "Good", folderOf(t, db, "https://ok.test/f"))
}

// A chain that lands on an existing folder with the same full path but other levels is reported, and
// the container it came through is not created empty.
func TestImportReportsPathMerge(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="Music"><outline text="AC/DC"><outline xmlUrl="https://acdc.test/f"/></outline></outline></body></opml>`, ImportOptions{})
	r := importString(t, db, `<opml><body><outline text="Music"><outline text="AC"><outline text="DC"><outline xmlUrl="https://dc.test/f"/></outline></outline></outline></body></opml>`, ImportOptions{})
	require.Zero(t, r.FoldersCreated, "no empty AC beside the literal AC/DC")
	require.Equal(t, []MergedPath{{Kept: []string{"Music", "AC/DC"}, Merged: []string{"Music", "AC", "DC"}}}, r.FoldersMergedPath)
	require.Equal(t, []string{"Music", "Music/AC/DC"}, paths(t, db))
	require.Equal(t, "Music/AC/DC", folderOf(t, db, "https://dc.test/f"))

	// A case-only difference from an existing folder is not a path merge.
	r = importString(t, db, `<opml><body><outline text="music"><outline text="ac/dc"/></outline></body></opml>`, ImportOptions{})
	require.Empty(t, r.FoldersMergedPath)
	require.Zero(t, r.FoldersCreated)
}

// Sibling names are compared like SQLite's NOCASE: ASCII letters only, so "É" and "é" are two folders
// in the parser and in the store, and a re-import keeps both.
func TestParseFoldsASCIICaseOnly(t *testing.T) {
	src := `<opml><body><outline text="École"><outline xmlUrl="https://a.test/f"/></outline>` +
		`<outline text="école"><outline xmlUrl="https://b.test/f"/></outline>` +
		`<outline text="ÉCOLE"><outline xmlUrl="https://c.test/f"/></outline></body></opml>`
	d := parseString(t, src)
	require.Equal(t, [][]string{{"École"}, {"école"}}, d.Folders, "only the ASCII letters fold: ÉCOLE is École")
	require.Equal(t, []MergedCase{{"École", "ÉCOLE"}}, d.FoldersMergedCase)

	db := openDB(t)
	r := importString(t, db, src, ImportOptions{})
	require.Equal(t, 2, r.FoldersCreated)
	require.Equal(t, "école", folderOf(t, db, "https://b.test/f"))
	db2 := openDB(t)
	r = importString(t, db2, export(t, db), ImportOptions{})
	require.Equal(t, 2, r.FoldersCreated)
	require.Equal(t, paths(t, db), paths(t, db2))
}

// reorderQ runs ExportFrom's query with another ORDER BY, as a broken view or query would.
type reorderQ struct {
	q     Queryer
	order string
}

func (r reorderQ) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.q.QueryContext(ctx, strings.Replace(query, "ORDER BY fp.sort_key", "ORDER BY "+r.order+", fp.sort_key", 1), args...)
}

// Rows out of tree order are an error, never misnested XML.
func TestExportRefusesRowsOutOfTreeOrder(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="A"><outline text="B"><outline xmlUrl="https://b.test/f"/></outline></outline>
	<outline text="C"><outline xmlUrl="https://c.test/f"/></outline></body></opml>`, ImportOptions{})
	var b bytes.Buffer
	err := ExportFrom(context.Background(), reorderQ{db.Reader(), "fo.id DESC"}, &b)
	require.ErrorContains(t, err, "outside its parent")
	b.Reset()
	require.NoError(t, ExportFrom(context.Background(), db.Reader(), &b))

	// A folder's rows split by another folder's.
	db2 := openDB(t)
	importString(t, db2, `<opml><body><outline text="A"><outline xmlUrl="https://a1.test/f"/></outline>
	<outline text="C"><outline xmlUrl="https://c.test/f"/></outline></body></opml>`, ImportOptions{})
	importString(t, db2, `<opml><body><outline text="A"><outline xmlUrl="https://a2.test/f"/></outline></body></opml>`, ImportOptions{})
	err = ExportFrom(context.Background(), reorderQ{db2.Reader(), "f.id"}, &b)
	require.ErrorContains(t, err, "listed twice")
}
