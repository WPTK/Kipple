package opml

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzParse: arbitrary OPML never panics; folders are distinct case-folded and
// every feed points at a listed folder.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"", `<opml version="2.0"><body><outline text="F"><outline type="rss" xmlUrl="https://a/feed" text="A"/></outline></body></opml>`,
		`<?xml version="1.0" encoding="ISO-8859-1"?><opml><body><outline text="caf` + "\xe9" + `"><outline xmlUrl="x"/></outline></body></opml>`,
		`<!DOCTYPE x [<!ENTITY a "b">]><opml><body><outline xmlUrl="&a;"/></body></opml>`,
		`<opml><body><outline text="A"><outline text="a"><outline xmlUrl="u" kipple:refresh="9999999999999"/></outline></outline></body></opml>`,
		strings.Repeat("<outline>", 500),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		doc, err := Parse(strings.NewReader(s))
		if err != nil {
			return
		}
		folders := map[string]bool{}
		for _, fo := range doc.Folders {
			k := strings.ToLower(fo)
			if folders[k] {
				t.Fatalf("folder %q listed twice", fo)
			}
			folders[k] = true
			if !utf8.ValidString(fo) {
				t.Fatal("invalid UTF-8 folder")
			}
		}
		for _, fd := range doc.Feeds {
			if fd.Folder != "" && !folders[strings.ToLower(fd.Folder)] {
				t.Fatalf("feed folder %q not in Folders", fd.Folder)
			}
		}
	})
}
