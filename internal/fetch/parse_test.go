package fetch

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

const cafe = "Café ‘quoted’ – naïve €5"

func TestParseFeedGolden(t *testing.T) {
	cases := []struct {
		name, file, feedURL, httpCharset, mode string
	}{
		{"rss2", "rss2.xml", "https://example.com/feed.xml", "", ""},
		{"rss1", "rss1.xml", "https://rdf.example.org/index.rdf", "", ""},
		{"atom", "atom.xml", "https://atom.example.net/atom.xml", "", ""},
		{"jsonfeed", "jsonfeed.json", "https://json.example.com/feed.json", "", ""},
		{"missing-dates", "missing-dates.xml", "https://nodates.example.com/rss", "", ""},
		{"dup-guids-auto", "dup-guids.xml", "https://dup.example.com/rss", "", DedupAuto},
		{"dup-guids-link", "dup-guids.xml", "https://dup.example.com/rss", "", DedupLink},
		{"dup-guids-link-title", "dup-guids.xml", "https://dup.example.com/rss", "", DedupLinkTitle},
		{"charset-cp1252", "charset-cp1252.xml", "https://cs.example.com/", "", ""},
		{"charset-utf8-labelled-latin1", "charset-utf8-labelled-latin1.xml", "https://cs.example.com/", "", ""},
		{"charset-cp1252-labelled-utf8", "charset-cp1252-labelled-utf8.xml", "https://cs.example.com/", "windows-1252", ""},
		{"charset-undeclared-cp1252", "charset-undeclared-cp1252.xml", "https://cs.example.com/", "", ""},
		{"charset-shiftjis", "charset-shiftjis.xml", "https://cs.example.com/", "", ""},
		{"charset-utf16le-bom", "charset-utf16le-bom.xml", "https://cs.example.com/", "", ""},
		{"charset-utf8-bom", "charset-utf8-bom.xml", "https://cs.example.com/", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", tc.file))
			require.NoError(t, err)
			f, err := ParseFeed(body, ParseOptions{FeedURL: tc.feedURL, HTTPCharset: tc.httpCharset, DedupMode: tc.mode})
			require.NoError(t, err)
			got, err := json.MarshalIndent(f, "", "  ")
			require.NoError(t, err)
			got = append(got, '\n')

			golden := filepath.Join("testdata", "golden", tc.name+".json")
			if *update {
				require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
				require.NoError(t, os.WriteFile(golden, got, 0o644))
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden; run go test ./internal/fetch -update")
			require.Equal(t, string(want), string(got))
		})
	}
}

func TestParseFeedFixtureFacts(t *testing.T) {
	load := func(t *testing.T, file string, o ParseOptions) *Feed {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", file))
		require.NoError(t, err)
		f, err := ParseFeed(b, o)
		require.NoError(t, err)
		return f
	}

	t.Run("rss2", func(t *testing.T) {
		f := load(t, "rss2.xml", ParseOptions{FeedURL: "https://example.com/feed.xml"})
		require.Equal(t, "rss", f.Format)
		require.Equal(t, "Example Blog", f.Title)
		require.Len(t, f.Items, 4)
		// Tracking pixel skipped, hero image resolved against the item URL.
		require.Equal(t, "https://example.com/img/hero.jpg", f.Items[0].ImageURL)
		require.Equal(t, "Alice Author", f.Items[0].Author)
		// feedburner:origLink replaces the URL but not the uid inputs.
		require.Equal(t, "https://example.com/posts/story.html", f.Items[1].URL)
		require.Contains(t, f.Items[1].RawLink, "feedproxy.google.com")
		require.Equal(t, "https://cdn.example.com/thumb.jpg", f.Items[1].ImageURL)
		// Image enclosure, relative to the item's absolute link.
		require.Equal(t, "https://example.com/media/cover.png", f.Items[2].ImageURL)
		require.Equal(t, "https://example.com/posts/enclosure", f.Items[2].URL)
		require.Equal(t, "https://example.com/posts/photo.jpg", f.Items[3].ImageURL)
		// Dates.
		require.NotNil(t, f.Items[0].Published)
		require.Equal(t, "2026-09-21T12:30:00Z", f.Items[0].Published.Format("2006-01-02T15:04:05Z07:00"))
	})

	t.Run("atom date fallback and image", func(t *testing.T) {
		f := load(t, "atom.xml", ParseOptions{FeedURL: "https://atom.example.net/atom.xml"})
		require.Equal(t, "https://atom.example.net/entries/one", f.Items[0].URL)
		require.Equal(t, "https://atom.example.net/thumb-one.jpg", f.Items[0].ImageURL)
		require.Equal(t, "Carol", f.Items[0].Author)
		require.Equal(t, "", f.Items[1].Author) // no feed-level author fallback: it would churn content_hash
		// Item 2 has only <updated>: published falls back to it.
		require.NotNil(t, f.Items[1].Published)
		require.Equal(t, "2026-09-19T10:00:00Z", f.Items[1].Published.Format("2006-01-02T15:04:05Z07:00"))
	})

	t.Run("json content_text is escaped", func(t *testing.T) {
		f := load(t, "jsonfeed.json", ParseOptions{FeedURL: "https://json.example.com/feed.json"})
		require.Equal(t, "json", f.Format)
		require.Equal(t, "<p>Plain &lt;text&gt; &amp; more<br>second line</p>", f.Items[1].ContentHTML)
		require.Equal(t, "https://json.example.com/banner/2.png", f.Items[1].ImageURL)
	})

	t.Run("missing dates are nil, not now", func(t *testing.T) {
		f := load(t, "missing-dates.xml", ParseOptions{FeedURL: "https://nodates.example.com/rss"})
		require.Len(t, f.Items, 4)
		for _, it := range f.Items {
			require.Nil(t, it.Published, it.Title)
		}
		require.Nil(t, f.Updated)
		// Fourth item has no guid and no link: h: uid.
		require.Regexp(t, `^h:[0-9a-f]{32}$`, f.Items[3].UID)
		require.Equal(t, "", f.Items[3].URL)
	})

	t.Run("duplicate guids", func(t *testing.T) {
		f := load(t, "dup-guids.xml", ParseOptions{FeedURL: "https://dup.example.com/rss"})
		require.Len(t, f.Items, 5)
		require.Equal(t, "g:"+H("same|https://dup.example.com/a"), f.Items[0].UID, "every occurrence of a repeated guid is link-keyed")
		require.Equal(t, "g:"+H("same|https://dup.example.com/b"), f.Items[1].UID)
		require.Equal(t, "g:"+H("same|2"), f.Items[2].UID) // no link: occurrence index
		require.Equal(t, "l:"+H("https://dup.example.com/d"), f.Items[3].UID)
		require.Equal(t, "l:"+H("https://dup.example.com/a"), f.Items[4].UID)
		require.Equal(t, []string{"guid_duplicates: 2/3"}, f.Notes)
		seen := map[string]bool{}
		for _, it := range f.Items {
			require.False(t, seen[it.UID])
			seen[it.UID] = true
		}
	})

	t.Run("link mode drops in-document duplicates", func(t *testing.T) {
		f := load(t, "dup-guids.xml", ParseOptions{FeedURL: "https://dup.example.com/rss", DedupMode: DedupLink})
		// Third has no link (h: fallback, distinct); Fifth repeats First's link.
		require.Len(t, f.Items, 4)
	})
}

func TestCharsetFixturesDecodeToSameText(t *testing.T) {
	files := []struct{ file, httpCharset, wantCharset string }{
		{"charset-cp1252.xml", "", "windows-1252"},
		{"charset-utf8-labelled-latin1.xml", "", "utf-8"},
		{"charset-cp1252-labelled-utf8.xml", "windows-1252", "windows-1252"},
		{"charset-undeclared-cp1252.xml", "", "windows-1252"},
		{"charset-utf16le-bom.xml", "", "utf-16le"},
		{"charset-utf8-bom.xml", "", "utf-8"},
	}
	for _, tc := range files {
		t.Run(tc.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", tc.file))
			require.NoError(t, err)
			f, err := ParseFeed(b, ParseOptions{FeedURL: "https://cs.example.com/", HTTPCharset: tc.httpCharset})
			require.NoError(t, err)
			require.Equal(t, tc.wantCharset, f.Charset)
			require.Equal(t, "Café feed", f.Title)
			require.Equal(t, cafe, f.Items[0].Title)
			require.Equal(t, cafe, f.Items[0].ContentText)
		})
	}

	t.Run("shiftjis", func(t *testing.T) {
		b, err := os.ReadFile(filepath.Join("testdata", "charset-shiftjis.xml"))
		require.NoError(t, err)
		f, err := ParseFeed(b, ParseOptions{FeedURL: "https://cs.example.com/"})
		require.NoError(t, err)
		require.Equal(t, "日本語のフィード", f.Title)
		require.Equal(t, "こんにちは世界", f.Items[0].Title)
	})
}

func TestDecodeBodyRepairsAndHashes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "charset-utf8-labelled-latin1.xml"))
	require.NoError(t, err)
	d := DecodeBody(b, "")
	require.True(t, d.Repaired)
	require.Contains(t, string(d.Body), `encoding="utf-8"`)
	require.NotContains(t, string(d.Body), "iso-8859-1")
	require.Len(t, d.BodyHash, 64)
	require.Equal(t, d.BodyHash, DecodeBody(b, "").BodyHash)

	// An honest windows-1252 body is not "repaired": the label was true.
	b, err = os.ReadFile(filepath.Join("testdata", "charset-cp1252.xml"))
	require.NoError(t, err)
	require.False(t, DecodeBody(b, "").Repaired)
}

func TestUIDRules(t *testing.T) {
	items := []Item{
		{GUID: "  g1  ", RawLink: "https://x/a", Title: "T", ContentText: "body"},
		{RawLink: "https://x/b", Title: "T", ContentText: "body"},
		{Title: "T", ContentText: "body"},
	}
	kept, notes := AssignUIDs(append([]Item(nil), items...), DedupAuto)
	require.Equal(t, "g:"+H("g1"), kept[0].UID) // guid is trimmed
	require.Equal(t, "l:"+H("https://x/b"), kept[1].UID)
	require.Equal(t, "h:"+H("T\x1fbody"), kept[2].UID)
	require.Empty(t, notes, "guid-less items must not trip the duplicate note")

	_, notes = AssignUIDs([]Item{
		{GUID: "a", RawLink: "https://x/1"}, {GUID: "a", RawLink: "https://x/2"}, {RawLink: "https://x/3"}, {RawLink: "https://x/4"},
	}, DedupAuto)
	require.Equal(t, []string{"guid_duplicates: 1/2"}, notes, "only non-empty guids count")

	kept, _ = AssignUIDs(append([]Item(nil), items...), DedupLinkTitle)
	require.Equal(t, "l:"+H("https://x/a\x1fT"), kept[0].UID)
	require.Equal(t, "h:"+H("T\x1fbody"), kept[2].UID)

	// Below the 5% threshold no note is emitted.
	many := make([]Item, 40)
	for i := range many {
		many[i] = Item{GUID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Title: "t"}
	}
	many[39].GUID = "" // 1/40 = 2.5%
	_, notes = AssignUIDs(many, DedupAuto)
	require.Empty(t, notes)
}

func TestHashesIgnoreDates(t *testing.T) {
	require.Equal(t, ContentHash("t", "u", "a", "<p>x</p>"), ContentHash("t", "u", "a", "<p>x</p>"))
	require.NotEqual(t, ContentHash("t", "u", "a", "<p>x</p>"), ContentHash("t", "u", "a", "<p>y</p>"))
	require.NotEqual(t, TextHash("ab", "c"), TextHash("a", "bc")) // separator prevents field bleed
}

func TestAssignUIDsSharedGUIDStableAcrossFetches(t *testing.T) {
	mk := func(link string) Item { return Item{GUID: "shared", RawLink: link, Title: link} }
	first, _ := AssignUIDs([]Item{mk("https://x.test/a"), mk("https://x.test/b")}, DedupAuto)
	second, _ := AssignUIDs([]Item{mk("https://x.test/c"), mk("https://x.test/a"), mk("https://x.test/b")}, DedupAuto)
	require.Len(t, first, 2)
	require.Len(t, second, 3)
	require.Equal(t, first[0].UID, second[1].UID, "a keeps its uid when c is prepended")
	require.Equal(t, first[1].UID, second[2].UID, "b keeps its uid when c is prepended")
	require.NotEqual(t, second[0].UID, second[1].UID)
}

func TestDecodeBodyCharsetEdgeCases(t *testing.T) {
	t.Run("utf-16 label without BOM is ignored", func(t *testing.T) {
		b := []byte(`<?xml version="1.0" encoding="UTF-16"?><rss><t>café</t></rss>`)
		d := DecodeBody(b, "")
		require.Equal(t, "utf-8", d.Source)
		require.Contains(t, string(d.Body), "café")
		d = DecodeBody([]byte(`<rss><t>café</t></rss>`), "utf-16")
		require.Equal(t, "utf-8", d.Source)
		require.Contains(t, string(d.Body), "café")
	})

	t.Run("invalid UTF-8 after a UTF-8 BOM falls back to windows-1252", func(t *testing.T) {
		b := append([]byte{0xEF, 0xBB, 0xBF}, []byte("<rss><t>caf\xe9</t></rss>")...)
		d := DecodeBody(b, "")
		require.True(t, d.Repaired)
		require.Equal(t, "windows-1252", d.Source)
		require.Contains(t, string(d.Body), "café")
		require.NotEqual(t, byte(0xEF), d.Body[0], "BOM stripped")
	})

	t.Run("any single-byte label carrying valid multibyte UTF-8 is treated as UTF-8", func(t *testing.T) {
		for _, label := range []string{"iso-8859-2", "windows-1251", "koi8-r", "iso-8859-15"} {
			b := []byte(`<?xml version="1.0" encoding="` + label + `"?><rss><t>Zażółć</t></rss>`)
			d := DecodeBody(b, "")
			require.Equal(t, "utf-8", d.Source, label)
			require.True(t, d.Repaired, label)
			require.Contains(t, string(d.Body), "Zażółć", label)
		}
		// A genuine single-byte body is still decoded with its label.
		d := DecodeBody([]byte("<?xml version=\"1.0\" encoding=\"iso-8859-2\"?><rss><t>\xb1</t></rss>"), "")
		require.Equal(t, "iso-8859-2", d.Source)
		require.Contains(t, string(d.Body), "ą")
	})
}
