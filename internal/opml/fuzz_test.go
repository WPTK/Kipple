package opml

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WPTK/kipple/internal/store"
)

func fuzzSeeds(f *testing.F) {
	deep := ""
	for i := 0; i < 12; i++ {
		deep += `<outline text="L` + string(rune('a'+i)) + `">`
	}
	deep += `<outline xmlUrl="https://deep.test/feed"/>` + strings.Repeat("</outline>", 12)
	for _, s := range append(hostileOPMLSeeds(), []string{
		"", `<opml version="2.0"><body><outline text="F"><outline type="rss" xmlUrl="https://a/feed" text="A"/></outline></body></opml>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><opml><body><outline text="caf` + "\xe9" + `"><outline xmlUrl="x"/></outline></body></opml>`,
		`<!DOCTYPE x [<!ENTITY a "b">]><opml><body><outline xmlUrl="&a;"/></body></opml>`,
		`<opml><body><outline text="A"><outline text="a"><outline xmlUrl="u" kipple:refresh="9999999999999"/></outline></outline></body></opml>`,
		strings.Repeat("<outline>", 500),
		`<opml><body>` + deep + `</body></opml>`,
		`<opml><body><outline text="Tech"><outline text="News"><outline xmlUrl="https://t.test/f"/></outline></outline>` +
			`<outline text="Sports"><outline text="News"><outline xmlUrl="https://s.test/f"/></outline></outline></body></opml>`,
		`<opml><body><outline text="Music"><outline text="AC/DC"><outline xmlUrl="https://acdc.test/f"/></outline></outline>` +
			`<outline text="AC"><outline text="DC"><outline xmlUrl="https://dc.test/f"/></outline></outline></body></opml>`,
		`<opml><body><outline text="A"><outline><outline text="B"><outline><outline xmlUrl="https://w.test/f"/></outline></outline></outline></outline></body></opml>`,
		`<opml><body><outline text="Uncategorized"><outline text="X"><outline xmlUrl="https://u.test/f"/></outline></outline>` +
			`<outline text="Bad&#127;"><outline text="Kid"><outline xmlUrl="https://b.test/f"/></outline></outline></body></opml>`,
	}...) {
		f.Add(s)
	}
}

// hostileOPMLSeeds are documents written to stress the importer: entity and DTD tricks, nesting far past the
// folder depth limit, very wide folders, and markup or control bytes in names.
func hostileOPMLSeeds() []string {
	return []string{
		`<?xml version="1.0"?><!DOCTYPE opml [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">` +
			`<!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">]><opml><body><outline text="&c;"><outline xmlUrl="https://e.test/&c;"/></outline></body></opml>`,
		`<?xml version="1.0"?><!DOCTYPE opml [<!ENTITY x SYSTEM "file:///etc/passwd">]><opml><body><outline text="&x;" xmlUrl="https://x.test/f"/></body></opml>`,
		`<?xml version="1.0"?><!DOCTYPE opml [<!ENTITY % p SYSTEM "http://127.0.0.1:1/p.dtd">%p;]><opml><body><outline xmlUrl="https://p.test/f"/></body></opml>`,
		`<opml><body>` + strings.Repeat(`<outline text="d">`, 9000) + `<outline xmlUrl="https://deep.test/f"/>` + strings.Repeat("</outline>", 9000) + `</body></opml>`,
		`<opml><body>` + strings.Repeat(`<outline text="d">`, 11000),
		`<opml><body><outline text="wide">` + strings.Repeat(`<outline type="rss" xmlUrl="https://w.test/f"/>`, 3000) + `</outline></body></opml>`,
		"\x00<opml><body><outline text=\"a\x01b\" xmlUrl=\"https://c.test/f\"/></body></opml>",
		`<opml><body><outline text="&lt;script&gt;alert(1)&lt;/script&gt;"><outline text="<b>x</b>" xmlUrl="javascript:alert(1)"/>` +
			`<outline xmlUrl="data:text/html,x"/><outline xmlUrl="file:///etc/passwd"/></outline></body></opml>`,
	}
}

// FuzzParse: arbitrary OPML never panics; folder chains are unique (ASCII case folded
// per level), 1 to store.MaxFolderDepth names long and listed after their
// parent, and every feed's folder is a listed chain.
func FuzzParse(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, s string) {
		doc, err := Parse(strings.NewReader(s))
		if err != nil {
			return
		}
		folders := map[string]bool{}
		lower := func(c []string) string { return foldCase(chainKey(c)) }
		for _, fo := range doc.Folders {
			if len(fo) == 0 || len(fo) > store.MaxFolderDepth {
				t.Fatalf("folder %q has %d levels", fo, len(fo))
			}
			k := lower(fo)
			if folders[k] {
				t.Fatalf("folder %q listed twice", fo)
			}
			if len(fo) > 1 && !folders[lower(fo[:len(fo)-1])] {
				t.Fatalf("folder %q listed before its parent", fo)
			}
			folders[k] = true
			for _, n := range fo {
				if n == "" || !utf8.ValidString(n) {
					t.Fatalf("bad folder name %q", n)
				}
			}
		}
		for _, fd := range doc.Feeds {
			if len(fd.Folder) > 0 && !folders[lower(fd.Folder)] {
				t.Fatalf("feed folder %q not in Folders", fd.Folder)
			}
		}
	})
}

// FuzzImportExport imports arbitrary OPML into a fresh database, or one already holding folders, a
// feed and a folder filter (odd-length inputs), and checks:
//   - no feed whose URL is valid is lost, and each lands in the file's folder cut at the first
//     level that was not kept (Uncategorized when nothing is left);
//   - the stored tree stays within store.MaxFolderDepth, folders never disappear, filters are kept;
//   - with every feed moved to Uncategorized, a MoveExisting re-import puts each feed back in the
//     file's folder, except a feed whose folder was not kept, which stays put;
//   - the export parses back with nothing merged or refused, and export -> import -> export is a
//     fixed point.
func FuzzImportExport(f *testing.F) {
	fuzzSeeds(f)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(f.TempDir(), "kipple.db")})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = db.Close() })
	exec := func(t *testing.T, stmts ...string) {
		if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			for _, s := range stmts {
				if _, err := tx.ExecContext(ctx, s); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(t *testing.T, q string) int {
		var n int
		if err := db.Reader().QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	reset := func(t *testing.T) {
		exec(t, "DELETE FROM feeds", "DELETE FROM filters", "DELETE FROM folders WHERE parent_id IS NULL AND is_default = 0")
	}
	const seedURL = "https://seed.test/feed"
	seed := func(t *testing.T) {
		d, err := Parse(strings.NewReader(`<opml><body><outline text="Music"><outline text="AC/DC"><outline xmlUrl="` + seedURL + `"/></outline></outline>` +
			`<outline text="Tech"><outline text="News"/></outline></body></opml>`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Import(ctx, db, d, ImportOptions{}); err != nil {
			t.Fatal(err)
		}
		exec(t, `INSERT INTO filters (scope, folder_id, kind, terms, action)
			SELECT 'folder', id, 'text', '["x"]', 'mute' FROM folders WHERE parent_id IS NULL AND name = 'Music'`)
	}
	exportOut := func(t *testing.T) string {
		var b bytes.Buffer
		if err := Export(ctx, db, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	// stored is the full path (ASCII case folded) of each feed's folder, by url_key.
	stored := func(t *testing.T) map[string]string {
		rows, err := db.Reader().QueryContext(ctx, "SELECT f.url_key, fp.path FROM feeds f JOIN folder_paths fp ON fp.id = f.folder_id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]string{}
		for rows.Next() {
			var k, p string
			if err := rows.Scan(&k, &p); err != nil {
				t.Fatal(err)
			}
			m[k] = foldCase(p)
		}
		return m
	}
	f.Fuzz(func(t *testing.T, s string) {
		doc, err := Parse(strings.NewReader(s))
		if err != nil {
			return
		}
		reset(t)
		seeded := len(s)%2 == 1
		if seeded {
			seed(t)
		}
		folders0, filters0 := count(t, "SELECT count(*) FROM folders"), count(t, "SELECT count(*) FROM filters")
		res, err := Import(ctx, db, doc, ImportOptions{})
		if err != nil {
			t.Fatalf("import: %v", err)
		}

		refused := map[string]bool{}
		for _, r := range res.FoldersRefused {
			refused[chainKey(r.chain)] = true
		}
		// kept is the file's folder cut at the first level that was not kept, and whether it was cut.
		kept := func(fd Feed) ([]string, bool) {
			for k := 1; k <= len(fd.Folder); k++ {
				if refused[chainKey(fd.Folder[:k])] {
					return fd.Folder[:k-1], true
				}
			}
			return fd.Folder, fd.cut
		}
		pathOf := func(c []string) string {
			if len(c) == 0 {
				return "uncategorized"
			}
			return foldCase(Path(c))
		}
		type listing struct {
			key  string
			feed Feed
		}
		var first []listing // the first listing of each valid URL, which decides its folder
		seen := map[string]bool{}
		for _, fd := range doc.Feeds {
			if _, key, _, err := store.ValidateFeedURL(fd.URL, true); err == nil && !seen[key] {
				seen[key] = true
				first = append(first, listing{key, fd})
			}
		}
		_, seedKey, _, _ := store.ValidateFeedURL(seedURL, true)
		want := len(first)
		if seeded && !seen[seedKey] {
			want++
		}
		got := stored(t)
		if len(got) != want {
			t.Fatalf("%d distinct valid feeds, %d stored (skipped %v)", want, len(got), res.Skipped)
		}
		for _, l := range first {
			if seeded && l.key == seedKey {
				continue // it existed before the import and stays where it was
			}
			c, _ := kept(l.feed)
			if got[l.key] != pathOf(c) {
				t.Fatalf("%s is in %q, want %q (file folder %q, refused %v)", l.key, got[l.key], pathOf(c), l.feed.Folder, res.FoldersRefused)
			}
		}
		folders1 := count(t, "SELECT count(*) FROM folders")
		if count(t, "SELECT COALESCE(max(depth), 0) FROM folder_paths") > store.MaxFolderDepth || folders1 < folders0 ||
			count(t, "SELECT count(*) FROM filters") != filters0 {
			t.Fatal("depth, folder count or filter count broken after import")
		}

		// MoveExisting puts every feed back where the file says, except where its folder was not kept.
		exec(t, "UPDATE feeds SET folder_id = 1")
		res2, err := Import(ctx, db, doc, ImportOptions{MoveExisting: true})
		if err != nil {
			t.Fatalf("move import: %v", err)
		}
		if res2.FeedsAdded != 0 {
			t.Fatalf("move import added %d feeds", res2.FeedsAdded)
		}
		got = stored(t)
		for _, l := range first {
			c, cut := kept(l.feed)
			if cut {
				c = nil
			}
			if got[l.key] != pathOf(c) {
				t.Fatalf("after the move %s is in %q, want %q (file folder %q)", l.key, got[l.key], pathOf(c), l.feed.Folder)
			}
		}
		if count(t, "SELECT count(*) FROM folders") < folders1 || count(t, "SELECT count(*) FROM filters") != filters0 {
			t.Fatal("folder or filter count broken after the move import")
		}

		n := len(got)
		out1 := exportOut(t)
		again, err := Parse(strings.NewReader(out1))
		if err != nil {
			t.Fatalf("export does not parse: %v\n%s", err, out1)
		}
		if len(again.Feeds) != n || len(again.FoldersRefused) != 0 || len(again.FoldersMergedCase) != 0 {
			t.Fatalf("export parses to %d feeds (want %d), refused %v, merged %v", len(again.Feeds), n, again.FoldersRefused, again.FoldersMergedCase)
		}
		reset(t)
		res3, err := Import(ctx, db, again, ImportOptions{})
		if err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if res3.FeedsAdded != n || len(res3.FoldersRefused) != 0 || len(res3.Skipped) != 0 || len(res3.MembershipsDropped) != 0 || len(res3.FoldersMergedPath) != 0 {
			t.Fatalf("re-import: %+v", res3)
		}
		if out2 := exportOut(t); out2 != out1 {
			t.Fatalf("export -> import -> export differs:\n%s\n---\n%s", out1, out2)
		}
	})
}
