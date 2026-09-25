package opml

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "kipple.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func parseString(t *testing.T, s string) *Doc {
	t.Helper()
	d, err := Parse(strings.NewReader(s))
	require.NoError(t, err)
	return d
}

func importString(t *testing.T, db *store.DB, s string, o ImportOptions) Result {
	t.Helper()
	r, err := Import(context.Background(), db, parseString(t, s), o)
	require.NoError(t, err)
	return r
}

func export(t *testing.T, db *store.DB) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, Export(context.Background(), db, &b))
	return b.String()
}

func TestParseFlattenEntitiesTextOverTitle(t *testing.T) {
	d := parseString(t, `<opml version="1.1"><body>
	<outline text="Tech &amp;amp; Gadgets" title="ignored">
	  <outline text="Apple" title="Apple">
	    <outline text="Feed &amp;amp; Co" title="Other" xmlUrl="http://a.test/rss"/>
	    <outline title="It&#8217;s &quot;t&quot;" xmlUrl="http://b.test/rss"/>
	    <outline text="Nbsp&nbsp;x" xmlUrl="http://c.test/rss"/>
	  </outline>
	</outline>
	<outline text="Root" xmlUrl="http://d.test/rss"/>
	</body></opml>`)
	require.Equal(t, []string{"Apple"}, d.Folders, "a container with no direct feeds is flattened away")
	require.Len(t, d.Feeds, 4)
	require.Equal(t, "Feed & Co", d.Feeds[0].Title, "text beats title, entities decoded")
	require.Equal(t, "Apple", d.Feeds[0].Folder)
	require.Equal(t, "It’s \"t\"", d.Feeds[1].Title)
	require.Equal(t, "Nbsp x", d.Feeds[2].Title)
	require.Equal(t, "", d.Feeds[3].Folder, "root-level feeds go to the default folder")
}

func TestImportReportsAndDedup(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml><body>
	<outline text="News"><outline text="A" xmlUrl="http://a.test/rss"/><outline text="B" xmlUrl="ftp://bad"/></outline>
	<outline text="news"><outline text="A2" xmlUrl="https://a.test/rss"/><outline text="C" xmlUrl="http://c.test/rss"/></outline>
	<outline text="Other"><outline text="A3" xmlUrl="http://A.test/rss"/></outline>
	</body></opml>`, ImportOptions{})
	require.Equal(t, 2, r.FoldersCreated)
	require.Equal(t, 2, r.FeedsAdded, "http/https and case variants are one feed")
	require.Equal(t, []MergedCase{{"News", "news"}}, r.FoldersMergedCase)
	require.Len(t, r.MembershipsDropped, 1)
	require.Equal(t, "News", r.MembershipsDropped[0].Kept)
	require.Equal(t, []string{"Other"}, r.MembershipsDropped[0].Dropped)
	require.Len(t, r.Skipped, 1)

	// Re-import after an http -> https migration: nothing new, existing left untouched.
	r2 := importString(t, db, `<opml><body><outline text="X"><outline text="A" xmlUrl="https://a.test/rss"/><outline text="C" xmlUrl="https://c.test/rss"/></outline></body></opml>`, ImportOptions{})
	require.Zero(t, r2.FeedsAdded)
	require.Len(t, r2.FeedsExisting, 2)
	require.Equal(t, 1, r2.FoldersCreated, "the empty-of-new-feeds folder is still created; feeds stay put")
}

func TestKippleAttrsAndMarkRead(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml xmlns:kipple="`+NS+`"><body><outline text="F">
	<outline text="A" xmlUrl="http://a.test/rss" kipple:interval="30" kipple:retention="0" kipple:fulltext="1"
	  kipple:dedup="link" kipple:enabled="0" kipple:user_agent="x/1" kipple:allow_private_net="true"/>
	<outline text="B" xmlUrl="http://b.test/rss" kipple:interval="2" kipple:retention="7" kipple:dedup="zzz"/>
	</outline></body></opml>`, ImportOptions{MarkReadOlderThanDays: 10})
	require.Equal(t, 2, r.FeedsAdded)
	require.Len(t, r.InvalidAttrs, 3)
	ctx := context.Background()
	var interval, retention, ft, en, priv, irb, next int64
	var dedup, reason, ua string
	require.NoError(t, db.Reader().QueryRowContext(ctx, `SELECT interval_minutes, retention, fulltext, enabled, allow_private_net,
		initial_read_before, next_fetch_at, dedup_mode, disabled_reason, user_agent FROM feeds WHERE url = 'http://a.test/rss'`).
		Scan(&interval, &retention, &ft, &en, &priv, &irb, &next, &dedup, &reason, &ua))
	require.EqualValues(t, []int64{30, 0, 1, 0, 1}, []int64{interval, retention, ft, en, priv})
	require.Equal(t, []string{"link", "user", "x/1"}, []string{dedup, reason, ua})
	require.Equal(t, next-10*86400, irb)
	var n int
	require.NoError(t, db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM feeds WHERE url='http://b.test/rss' AND interval_minutes IS NULL AND retention IS NULL AND dedup_mode='auto'`).Scan(&n))
	require.Equal(t, 1, n, "invalid overrides are ignored")

	out := export(t, db)
	require.Contains(t, out, `kipple:interval="30"`)
	require.NotContains(t, out, "http_auth")
}

func TestHTTPAuthNeverExported(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="F"><outline text="A" xmlUrl="http://a.test/rss"/></outline></body></opml>`, ImportOptions{})
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET http_auth='u:secret'`)
		return err
	}))
	out := export(t, db)
	require.NotContains(t, out, "secret")
	require.NotContains(t, out, "http_auth")
}

func TestRoundTripFixedPoint(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "synthetic.opml"))
	require.NoError(t, err)
	roundTrip(t, src, 138, 15)
}

// TestRoundTripRealFile runs the same gate against the live NewsBlur export when
// KIPPLE_REAL_OPML points at it. The file is personal and is never committed.
func TestRoundTripRealFile(t *testing.T) {
	p := os.Getenv("KIPPLE_REAL_OPML")
	if p == "" {
		t.Skip("KIPPLE_REAL_OPML not set")
	}
	src, err := os.ReadFile(p)
	require.NoError(t, err)
	roundTrip(t, src, 138, 15)
}

func roundTrip(t *testing.T, src []byte, wantFeeds, wantFolders int) {
	t.Helper()
	orig, err := Parse(bytes.NewReader(src))
	require.NoError(t, err)
	require.Len(t, orig.Feeds, wantFeeds)
	require.Len(t, orig.Folders, wantFolders)

	db := openDB(t)
	r, err := Import(context.Background(), db, orig, ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, wantFeeds, r.FeedsAdded)
	require.Equal(t, wantFolders, r.FoldersCreated)
	require.Empty(t, r.MembershipsDropped)
	require.Empty(t, r.Skipped)

	out1 := export(t, db)
	again, err := Parse(strings.NewReader(out1))
	require.NoError(t, err)
	require.Equal(t, orig.Folders, again.Folders, "folder count and order")
	require.Equal(t, len(orig.Feeds), len(again.Feeds))
	// Feeds are grouped by folder on export; compare per-folder order and titles.
	group := func(d *Doc) map[string][][2]string {
		m := map[string][][2]string{}
		for _, f := range d.Feeds {
			u, _ := urlKey(f.URL)
			m[f.Folder] = append(m[f.Folder], [2]string{u, f.Title})
		}
		return m
	}
	require.Equal(t, group(orig), group(again))
	// Flat sequence is identical too when folders are contiguous in the source.
	for i := range orig.Feeds {
		if orig.Feeds[i].URL != again.Feeds[i].URL {
			t.Logf("feed order differs at %d only because the source interleaves folders", i)
			break
		}
	}

	db2 := openDB(t)
	_, err = Import(context.Background(), db2, again, ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, out1, export(t, db2), "import -> export -> import is a fixed point")

	// Re-importing the original adds nothing.
	r3, err := Import(context.Background(), db, orig, ImportOptions{})
	require.NoError(t, err)
	require.Zero(t, r3.FeedsAdded)
	require.Len(t, r3.FeedsExisting, wantFeeds)
	require.Zero(t, r3.FoldersCreated)
}

func TestParseURLsNotDoubleUnescaped(t *testing.T) {
	// Correctly escaped query strings must round-trip unchanged: "&sect" and
	// "&region" look like legacy no-semicolon entities to html.UnescapeString.
	d := parseString(t, `<opml><body>
	<outline text="A" xmlUrl="http://a.test/rss?a=1&amp;section=x&amp;region=us" htmlUrl="http://a.test/?x=1&amp;copy=2&amp;reg=3"/>
	<outline text="B &amp;amp; C" xmlUrl="http://b.test/rss"/>
	<outline text="Fish &amp;chips" xmlUrl="http://c.test/rss"/>
	</body></opml>`)
	require.Equal(t, "http://a.test/rss?a=1&section=x&region=us", d.Feeds[0].URL)
	require.Equal(t, "http://a.test/?x=1&copy=2&reg=3", d.Feeds[0].SiteURL)
	require.Equal(t, "B & C", d.Feeds[1].Title, "NewsBlur double-escaped title still decodes")
	require.Equal(t, "Fish &chips", d.Feeds[2].Title, "legacy no-semicolon entities are not decoded")
}
