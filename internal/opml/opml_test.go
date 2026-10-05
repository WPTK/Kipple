package opml

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
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

func TestParseNestedEntitiesTextOverTitle(t *testing.T) {
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
	require.Equal(t, [][]string{{"Tech & Gadgets"}, {"Tech & Gadgets", "Apple"}}, d.Folders, "a container with no feeds of its own is kept")
	require.Len(t, d.Feeds, 4)
	require.Equal(t, "Feed & Co", d.Feeds[0].Title, "text beats title, entities decoded")
	require.Equal(t, []string{"Tech & Gadgets", "Apple"}, d.Feeds[0].Folder)
	require.Equal(t, "It’s \"t\"", d.Feeds[1].Title)
	require.Equal(t, "Nbsp\u00A0x", d.Feeds[2].Title)
	require.Empty(t, d.Feeds[3].Folder, "root-level feeds go to the default folder")
}

// feedName is the display name the app shows for the feed at host.
func feedName(t *testing.T, db *store.DB, host string) string {
	t.Helper()
	var id int64
	require.NoError(t, db.Reader().QueryRow("SELECT id FROM feeds WHERE host = ?", host).Scan(&id))
	n, err := db.FeedName(context.Background(), id)
	require.NoError(t, err)
	return n
}

// firstFetch commits a successful fetch of the feed at host whose document is titled title.
func firstFetch(t *testing.T, db *store.DB, host, title string) {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, db.Reader().QueryRow("SELECT id FROM feeds WHERE host = ?", host).Scan(&id))
	snap, ok, err := db.FeedSnapshot(ctx, db.FetchSettings(ctx), id)
	require.NoError(t, err)
	require.True(t, ok)
	snap.Trigger = fetch.TriggerScheduled
	feed, err := fetch.ParseFeed([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>`+title+`</title>`+
		`<item><guid>a</guid><title>t</title><link>https://`+host+`/a</link></item></channel></rss>`), fetch.ParseOptions{FeedURL: snap.URL})
	require.NoError(t, err)
	now := time.Now()
	_, err = db.CommitFetch(ctx, &fetch.Result{Snap: snap, StartedAt: now, Outcome: fetch.OutcomeOK, Status: 200, Feed: feed,
		FinalURL: snap.URL, Redirect: fetch.RedirectDecision{Action: fetch.RedirectClear}, NextFetchAt: now.Add(time.Hour), CurrentDelayS: 3600})
	require.NoError(t, err)
}

// An imported feed without a name has no title until its first fetch (the app names it by its URL),
// like a feed added any other way; a name in the file is kept as the feed's custom name, read by the
// same rule as a document title (fetch.FeedTitle, after the parser's own doubled-&amp; rule): one
// line, at most 200 characters, a level of escaping left behind decoded.
func TestImportNamesFeeds(t *testing.T) {
	db := openDB(t)
	long := strings.Repeat("y", 250)
	importString(t, db, `<opml><body>
	<outline xmlUrl="https://untitled.test/rss"/>
	<outline text="  Two
	  lines  " xmlUrl="https://two.test/rss"/>
	<outline text="`+long+`" xmlUrl="https://long.test/rss"/>
	<outline text="Tips &amp;amp; tricks" xmlUrl="https://tips.test/rss"/>
	<outline text="It&amp;#8217;s mine" xmlUrl="https://mine.test/rss"/>
	</body></opml>`, ImportOptions{})
	require.Equal(t, "https://untitled.test/rss", feedName(t, db, "untitled.test"))
	require.Equal(t, "Two lines", feedName(t, db, "two.test"))
	require.Len(t, []rune(feedName(t, db, "long.test")), 200)
	require.Equal(t, "Tips & tricks", feedName(t, db, "tips.test"), "the OPML parser undoes a doubled &amp;")
	require.Equal(t, "It’s mine", feedName(t, db, "mine.test"), "read like a document title")
}

// An OPML name that is the same text as the feed's own title, however each is escaped, is dropped at
// the first successful fetch, so the feed follows its own renames afterwards.
func TestOPMLNameEqualToTheDocumentTitleIsDropped(t *testing.T) {
	for name, outline := range map[string]string{
		"literal":         `It’s mine`,
		"reference":       `It&#8217;s mine`,
		"escaped twice":   `It&amp;#8217;s mine`,
		"spaced out":      "  It&#8217;s \n mine ",
		"invisible chars": "It’s\u200B mine\u200E",
	} {
		db := openDB(t)
		importString(t, db, `<opml><body><outline text="`+outline+`" xmlUrl="https://mine.test/rss"/></body></opml>`, ImportOptions{})
		firstFetch(t, db, "mine.test", "It&amp;#8217;s mine") // the document title "It&#8217;s mine", escaped once more
		var n int
		require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE custom_title IS NOT NULL").Scan(&n))
		require.Zero(t, n, name)
		require.Equal(t, "It’s mine", feedName(t, db, "mine.test"), name)
	}
}

// A feed an earlier version stored with its host as title (a document without a title keeps it for
// good) does not round-trip that host as a name once migration 0014 has run: the export writes no
// name and the re-imported feed is not pinned to the host.
func TestLegacyHostTitleDoesNotRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kipple.db")
	db, err := store.Open(ctx, store.Options{Path: path})
	require.NoError(t, err)
	importString(t, db, `<opml><body><outline xmlUrl="https://untitled.test/rss"/></body></opml>`, ImportOptions{})
	firstFetch(t, db, "untitled.test", "") // fetched; the document names no title
	require.NoError(t, db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE feeds SET title = host"); err != nil { // what an earlier subscribe left
			return err
		}
		_, err := tx.ExecContext(ctx, "PRAGMA user_version = 13")
		return err
	}))
	require.NoError(t, db.Close())
	db, err = store.Open(ctx, store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	out := export(t, db)
	require.NotContains(t, out, `"untitled.test"`)
	dst := openDB(t)
	importString(t, dst, out, ImportOptions{})
	var n int
	require.NoError(t, dst.Reader().QueryRow("SELECT count(*) FROM feeds WHERE custom_title IS NOT NULL").Scan(&n))
	require.Zero(t, n, "the host is not pinned as a name")
	firstFetch(t, dst, "untitled.test", "Named Now")
	require.Equal(t, "Named Now", feedName(t, dst, "untitled.test"))
}

// A feed that was never fetched round-trips through OPML without picking up a name: the export writes
// none, so the re-imported feed still takes the title its first fetch finds.
func TestRoundTripOfAnUnfetchedFeedKeepsItUnnamed(t *testing.T) {
	src := openDB(t)
	importString(t, src, `<opml><body><outline xmlUrl="https://fresh.test/rss"/></body></opml>`, ImportOptions{})
	out := export(t, src)
	require.NotContains(t, out, `text="fresh.test"`)
	require.NotContains(t, out, `text="https://fresh.test/rss"`)

	dst := openDB(t)
	importString(t, dst, out, ImportOptions{})
	firstFetch(t, dst, "fresh.test", "Fresh News")
	require.Equal(t, "Fresh News", feedName(t, dst, "fresh.test"))
	require.Zero(t, func() int {
		var n int
		require.NoError(t, dst.Reader().QueryRow("SELECT count(*) FROM feeds WHERE custom_title IS NOT NULL").Scan(&n))
		return n
	}(), "no custom name was made up")

	// A fetched feed round-trips with its real title, which the first fetch after re-import drops as a
	// custom name (it equals the document's), so the feed keeps following its own renames.
	firstFetch(t, src, "fresh.test", "Fresh News")
	dst2 := openDB(t)
	importString(t, dst2, export(t, src), ImportOptions{})
	require.Equal(t, "Fresh News", feedName(t, dst2, "fresh.test"))
	firstFetch(t, dst2, "fresh.test", "Fresh News")
	firstFetch(t, dst2, "fresh.test", "Fresh News Daily")
	require.Equal(t, "Fresh News Daily", feedName(t, dst2, "fresh.test"))
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
	require.EqualValues(t, []int64{30, 0, 1, 0, 0}, []int64{interval, retention, ft, en, priv}, "allow_private_net is never applied by import")
	require.Equal(t, []string{"http://a.test/rss: kipple:allow_private_net"}, r.IgnoredAttrs)
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

// A feed marked for deletion (an interrupted delete: its URL is the
// kipple:deleting:<id> placeholder) is left out of the export entirely; its
// folder is still listed, and a default folder left empty by it is not.
func TestExportLeavesOutDeletingFeed(t *testing.T) {
	db := openDB(t)
	importString(t, db, `<opml><body><outline text="F"><outline text="A" xmlUrl="http://a.test/rss"/>
	<outline text="B" xmlUrl="http://b.test/rss"/></outline><outline text="Root" xmlUrl="http://r.test/rss"/></body></opml>`, ImportOptions{})
	require.NoError(t, db.WithWrite(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE feeds SET url = 'kipple:deleting:' || id, url_key = 'kipple:deleting:' || id
			WHERE url IN ('http://a.test/rss', 'http://r.test/rss')`)
		return err
	}))
	out := export(t, db)
	require.NotContains(t, out, "kipple:deleting:")
	require.NotContains(t, out, "a.test")
	require.NotContains(t, out, "r.test")
	require.Contains(t, out, `xmlUrl="http://b.test/rss"`)
	require.Contains(t, out, `text="F"`)
	require.NotContains(t, out, `text="Uncategorized"`, "the default folder is listed only while it has feeds")
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
			k := chainKey(f.Folder)
			m[k] = append(m[k], [2]string{u, f.Title})
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

func TestUnnamedWrapperUnderNamedContainerImports(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml><body>
	<outline text="Tech"><outline><outline text="Wrapped" xmlUrl="http://w.test/rss"/></outline></outline>
	</body></opml>`, ImportOptions{})
	require.Equal(t, 1, r.FeedsAdded)
	require.Equal(t, 1, r.FoldersCreated)
	var name string
	require.NoError(t, db.Reader().QueryRowContext(context.Background(),
		`SELECT f.name FROM feeds x JOIN folders f ON f.id = x.folder_id WHERE x.url = 'http://w.test/rss'`).Scan(&name))
	require.Equal(t, "Tech", name)

	// A hand-built Doc that forgot to list the folder still imports.
	db2 := openDB(t)
	r2, err := Import(context.Background(), db2, &Doc{Feeds: []Feed{{URL: "http://x.test/rss", Folder: []string{"Ghost", "Town"}}}}, ImportOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, r2.FeedsAdded)
	require.Equal(t, 2, r2.FoldersCreated)
}

func TestParseNonUTF8Charsets(t *testing.T) {
	latin := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><opml><body><outline text=\"Caf\xe9\" xmlUrl=\"http://l.test/rss\"/></body></opml>"
	d, err := Parse(strings.NewReader(latin))
	require.NoError(t, err)
	require.Equal(t, "Café", d.Feeds[0].Title)

	u16 := "<?xml version=\"1.0\" encoding=\"UTF-16\"?><opml><body><outline text=\"Café\" xmlUrl=\"http://u.test/rss\"/></body></opml>"
	buf := []byte{0xFF, 0xFE}
	for _, c := range u16 {
		buf = append(buf, byte(c), byte(c>>8))
	}
	d, err = Parse(bytes.NewReader(buf))
	require.NoError(t, err)
	require.Equal(t, "Café", d.Feeds[0].Title)
	require.Equal(t, "http://u.test/rss", d.Feeds[0].URL)
}

func TestImportIgnoresDangerousAttrsAndBareNamespace(t *testing.T) {
	db := openDB(t)
	// No xmlns declaration: the bare "kipple" prefix must not count as ours.
	r := importString(t, db, `<opml><body><outline text="F">
	<outline text="A" xmlUrl="http://a.test/rss" kipple:interval="30" kipple:allow_private_net="1"/>
	</outline></body></opml>`, ImportOptions{})
	require.Equal(t, 1, r.FeedsAdded)
	require.Empty(t, r.IgnoredAttrs)
	require.Empty(t, r.InvalidAttrs)

	r = importString(t, db, `<opml xmlns:kipple="`+NS+`"><body><outline text="F">
	<outline text="B" xmlUrl="http://b.test/rss" htmlUrl="javascript:alert(1)" kipple:allow_insecure_tls="1" kipple:allow_private_net="1"/>
	<outline text="C" xmlUrl="http://c.test/rss" htmlUrl="https://c.test/"/>
	</outline></body></opml>`, ImportOptions{})
	require.Equal(t, []string{"http://b.test/rss: kipple:allow_private_net", "http://b.test/rss: kipple:allow_insecure_tls"}, r.IgnoredAttrs)
	var tls, priv int
	var site string
	require.NoError(t, db.Reader().QueryRowContext(context.Background(),
		`SELECT allow_insecure_tls, allow_private_net, site_url FROM feeds WHERE url='http://b.test/rss'`).Scan(&tls, &priv, &site))
	require.Zero(t, tls)
	require.Zero(t, priv)
	require.Equal(t, "", site, "non-http htmlUrl is dropped")
	require.NoError(t, db.Reader().QueryRowContext(context.Background(),
		`SELECT site_url FROM feeds WHERE url='http://c.test/rss'`).Scan(&site))
	require.Equal(t, "https://c.test/", site)
}

// Import applies the checks every other path applies: ValidateFeedURL's syntax
// check (a literal private address is imported with allow_private_net off and
// reported, not skipped), the feed PATCH rule for kipple:user_agent, and the
// folder name limits (the folder is refused and reported, its feeds kept).
func TestImportValidatesURLUserAgentAndFolderNames(t *testing.T) {
	db := openDB(t)
	long := strings.Repeat("f", 101)
	ok100 := strings.Repeat("g", 100)
	r := importString(t, db, `<opml xmlns:kipple="`+NS+`"><body>
	<outline text="Root" xmlUrl="http://127.0.0.1/rss"/>
	<outline text="NAS" xmlUrl="http://192.168.1.5:8080/rss" kipple:allow_private_net="true"/>
	<outline text="Creds" xmlUrl="http://bob:pw@creds.test/rss"/>
	<outline text="Good" xmlUrl="http://good.test/rss" kipple:user_agent="MyAgent/1.0"/>
	<outline text="LongUA" xmlUrl="http://longua.test/rss" kipple:user_agent="`+strings.Repeat("u", 501)+`"/>
	<outline text="CtlUA" xmlUrl="http://ctlua.test/rss" kipple:user_agent="a&#10;b"/>
	<outline text="`+long+`"><outline text="L" xmlUrl="http://long.test/rss"/></outline>
	<outline text="Bad&#127;Name"><outline text="D" xmlUrl="http://del.test/rss"/></outline>
	<outline text="`+ok100+`"><outline text="H" xmlUrl="http://ok100.test/rss"/></outline>
	</body></opml>`, ImportOptions{})

	reasons := map[string]string{}
	for _, s := range r.Skipped {
		reasons[s.URL] = s.Reason
	}
	require.Len(t, r.Skipped, 1, "%v", r.Skipped)
	require.Contains(t, reasons["http://bob:pw@creds.test/rss"], "user name or password", "userinfo is still refused")
	require.NotContains(t, reasons, "http://127.0.0.1/rss", "a private address is imported, not skipped")
	require.Contains(t, r.IgnoredAttrs, "http://127.0.0.1/rss: private address, imported with allow_private_net off; turn it on for this feed to fetch it")
	require.Contains(t, r.IgnoredAttrs, "http://192.168.1.5:8080/rss: kipple:allow_private_net")
	var priv int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE url IN ('http://127.0.0.1/rss', 'http://192.168.1.5:8080/rss') AND allow_private_net = 0").Scan(&priv))
	require.Equal(t, 2, priv, "imported with the exception off")
	require.Equal(t, 8, r.FeedsAdded, "a feed in a refused folder is imported into Uncategorized")
	require.Equal(t, 1, r.FoldersCreated, "only the 100-character folder")
	require.Len(t, r.FoldersRefused, 2)
	require.Equal(t, long, r.FoldersRefused[0].Path)
	require.Contains(t, r.FoldersRefused[0].Reason, "100 characters")
	require.Equal(t, "Bad\x7fName", r.FoldersRefused[1].Path)
	var inDefault int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE url IN ('http://long.test/rss', 'http://del.test/rss') AND folder_id = 1").Scan(&inDefault))
	require.Equal(t, 2, inDefault)

	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM folders WHERE length(name) > 100 OR name LIKE '%' || char(127) || '%'").Scan(&n))
	require.Zero(t, n)

	ua := func(u string) sql.NullString {
		var v sql.NullString
		require.NoError(t, db.Reader().QueryRow("SELECT user_agent FROM feeds WHERE url = ?", u).Scan(&v))
		return v
	}
	require.Equal(t, "MyAgent/1.0", ua("http://good.test/rss").String)
	require.False(t, ua("http://longua.test/rss").Valid, "an over-long UA is not stored")
	require.False(t, ua("http://ctlua.test/rss").Valid, "a UA with a newline is not stored")
	joined := strings.Join(r.InvalidAttrs, "\n")
	require.Contains(t, joined, "http://longua.test/rss: kipple:user_agent=")
	require.Contains(t, joined, "http://ctlua.test/rss: kipple:user_agent=")
}

// An OPML file is a list of URLs from outside: a feed that is not an http(s) URL,
// or has credentials in it, is skipped and reported, never stored, and no file can
// switch on a feed's private-network or insecure-TLS exceptions.
func TestImportSkipsNonHTTPSchemesAndNeverGrantsExceptions(t *testing.T) {
	db := openDB(t)
	r := importString(t, db, `<opml xmlns:kipple="`+NS+`"><body>
	<outline text="a" xmlUrl="ftp://a.test/rss"/>
	<outline text="b" xmlUrl="file:///etc/passwd"/>
	<outline text="c" xmlUrl="gopher://127.0.0.1:70/_x"/>
	<outline text="d" xmlUrl="javascript:alert(1)"/>
	<outline text="e" xmlUrl="//e.test/rss"/>
	<outline text="f" xmlUrl="http://x@127.0.0.1/rss"/>
	<outline text="g" xmlUrl="http://169.254.169.254/latest/meta-data/" kipple:allow_private_net="1" kipple:allow_insecure_tls="1"/>
	<outline text="h" xmlUrl="http://2130706433/rss" kipple:allow_private_net="1"/>
	</body></opml>`, ImportOptions{})
	require.Len(t, r.Skipped, 5, "%v", r.Skipped)
	require.Equal(t, 3, r.FeedsAdded, "the scheme-relative address (as https), the metadata address and the numeric spelling are stored, switched off for the guard")
	var n int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE url = 'https://e.test/rss'").Scan(&n))
	require.Equal(t, 1, n)
	var granted int
	require.NoError(t, db.Reader().QueryRow("SELECT count(*) FROM feeds WHERE allow_private_net != 0 OR allow_insecure_tls != 0").Scan(&granted))
	require.Zero(t, granted)
}
