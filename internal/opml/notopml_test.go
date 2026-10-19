package opml

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRefusesWellFormedNonOPML(t *testing.T) {
	for _, s := range []string{`<rss version="2.0"><channel/></rss>`, `<html><body/></html>`, `<feed xmlns="http://www.w3.org/2005/Atom"/>`} {
		_, err := parse(strings.NewReader(s))
		require.ErrorIs(t, err, ErrNotOPML, s)
	}
	_, err := parse(strings.NewReader(`<OPML version="2.0"><body/></OPML>`))
	require.NoError(t, err)
}

// Parse refuses a document with no feeds (the lenient parse does not, for the export round trip), and AsRefused names
// every content refusal the callers map (issue #518).
func TestParseRefusesFoldersOnly(t *testing.T) {
	t.Parallel()
	const folders = `<opml><body><outline text="Empty"/></body></opml>`
	_, err := parse(strings.NewReader(folders))
	require.NoError(t, err)
	_, err = Parse(strings.NewReader(folders))
	ref, ok := AsRefused(err)
	require.True(t, ok)
	require.Equal(t, Refused{Code: "empty_opml", Message: "that OPML file has no feeds in it"}, ref)
	_, ok = AsRefused(errors.New("disk"))
	require.False(t, ok)
	_, ok = AsRefused(nil)
	require.False(t, ok)
	_, err = Parse(strings.NewReader(`<rss/>`))
	ref, ok = AsRefused(err)
	require.True(t, ok)
	require.True(t, ref.NotOPML)
}

// What real exporters write around the <opml> root must still parse.
func TestParseAcceptsOPMLWrappers(t *testing.T) {
	body := `<body><outline type="rss" text="A" xmlUrl="https://a.example/f"/></body>`
	for name, s := range map[string]string{
		"bom":          string(rune(0xFEFF)) + "<?xml version=\"1.0\"?><opml version=\"2.0\">" + body + "</opml>",
		"prefixed":     `<x:opml xmlns:x="http://example.com/ns" version="2.0">` + body + `</x:opml>`,
		"doctype":      `<?xml version="1.0"?><!DOCTYPE opml SYSTEM "http://example.com/opml.dtd"><opml version="2.0">` + body + `</opml>`,
		"doctype html": `<!DOCTYPE opml><opml version="2.0">` + body + `</opml>`,
	} {
		doc, err := parse(strings.NewReader(s))
		require.NoError(t, err, name)
		require.Len(t, doc.Feeds, 1, name)
	}
}
