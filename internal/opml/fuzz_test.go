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
	for _, s := range []string{
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
	} {
		f.Add(s)
	}
}

// FuzzParse: arbitrary OPML never panics; folder chains are unique (case-folded
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
		lower := func(c []string) string { return strings.ToLower(chainKey(c)) }
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

// FuzzImportExport: an import loses no feed whose URL is valid, the stored tree
// stays within store.MaxFolderDepth, the export parses back with nothing merged
// or refused, and export -> import -> export is a fixed point.
func FuzzImportExport(f *testing.F) {
	fuzzSeeds(f)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(f.TempDir(), "kipple.db")})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = db.Close() })
	reset := func(t *testing.T) {
		if err := db.WithWrite(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM feeds"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM folders WHERE parent_id IS NULL AND is_default = 0")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	exportOut := func(t *testing.T) string {
		var b bytes.Buffer
		if err := Export(ctx, db, &b); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	f.Fuzz(func(t *testing.T, s string) {
		doc, err := Parse(strings.NewReader(s))
		if err != nil {
			return
		}
		reset(t)
		res, err := Import(ctx, db, doc, ImportOptions{})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		want := map[string]bool{} // distinct valid URLs
		for _, fd := range doc.Feeds {
			if _, key, _, err := store.ValidateFeedURL(fd.URL, true); err == nil {
				want[key] = true
			}
		}
		var n, depth int
		if err := db.Reader().QueryRowContext(ctx, "SELECT count(*), (SELECT COALESCE(max(depth), 0) FROM folder_paths) FROM feeds").Scan(&n, &depth); err != nil {
			t.Fatal(err)
		}
		if n != len(want) || res.FeedsAdded != len(want) {
			t.Fatalf("%d distinct valid feeds, %d stored, %d added (skipped %v)", len(want), n, res.FeedsAdded, res.Skipped)
		}
		if depth > store.MaxFolderDepth {
			t.Fatalf("folder depth %d", depth)
		}

		out1 := exportOut(t)
		again, err := Parse(strings.NewReader(out1))
		if err != nil {
			t.Fatalf("export does not parse: %v\n%s", err, out1)
		}
		if len(again.Feeds) != n || len(again.FoldersRefused) != 0 || len(again.FoldersMergedCase) != 0 {
			t.Fatalf("export parses to %d feeds (want %d), refused %v, merged %v", len(again.Feeds), n, again.FoldersRefused, again.FoldersMergedCase)
		}
		reset(t)
		res2, err := Import(ctx, db, again, ImportOptions{})
		if err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if res2.FeedsAdded != n || len(res2.FoldersRefused) != 0 || len(res2.Skipped) != 0 || len(res2.MembershipsDropped) != 0 {
			t.Fatalf("re-import: %+v", res2)
		}
		if out2 := exportOut(t); out2 != out1 {
			t.Fatalf("export -> import -> export differs:\n%s\n---\n%s", out1, out2)
		}
	})
}
