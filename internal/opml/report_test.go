package opml

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// An entity neither XML nor HTML defines refuses the file instead of becoming a name (issue #467); every name HTML
// defines decodes, and a bare & in a query still reads.
func TestParseEntities(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{
		`<opml><body><outline text="&x;"><outline xmlUrl="https://a.test/f"/></outline></body></opml>`,
		`<opml><body><outline title="ok" xmlUrl="https://a.test/f?&x;"/></body></opml>`,
		hostileOPMLSeeds()[0],
		hostileOPMLSeeds()[1],
	} {
		_, err := Parse(strings.NewReader(doc))
		var bad *UndefinedEntityError
		require.True(t, errors.As(err, &bad), "%s: %v", doc, err)
	}
	_, err := Parse(strings.NewReader("<opml>\n<body>\n<outline text=\"a &zz; b\"/></body></opml>"))
	require.EqualError(t, err, "opml: &zz; on line 3 is not an entity XML or HTML defines")

	d := parseString(t, `<opml><body><!-- &comment; -->
	<outline text="A&nbsp;B &hellip; &NewLine;C &amp;x;" xmlUrl="https://a.test/f?a=1&b=2"/></body></opml>`)
	require.Len(t, d.Feeds, 1)
	require.Equal(t, "A B … \nC &x;", d.Feeds[0].Title)
	require.Equal(t, "https://a.test/f?a=1&b=2", d.Feeds[0].URL)
}

// A file with no element at all (empty, JSON) is not an OPML file, not a damaged one (issue #487).
func TestParseNoElement(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{"", "   \n", `{"error":"not_found"}`} {
		_, err := Parse(strings.NewReader(doc))
		require.ErrorIs(t, err, ErrNotOPML, "%q", doc)
	}
}

// Every feed outline is counted once (issue #487), the skip reasons are worded for an import, and an overlong
// address is refused (issue #470).
func TestImportCountsAndSkipReasons(t *testing.T) {
	t.Parallel()
	long := "https://long.test/" + strings.Repeat("a", 4096)
	db := openDB(t)
	r := importString(t, db, `<opml><body>
	<outline text="One" xmlUrl="https://one.test/f"/>
	<outline text="One again" xmlUrl="https://one.test/f"/>
	<outline text="F"><outline text="One in F" xmlUrl="http://one.test/f"/></outline>
	<outline text="Bad" xmlUrl="ftp://bad.test/f"/>
	<outline text="Long" xmlUrl="`+long+`" htmlUrl="`+long+`"/>
	<outline text="Site" xmlUrl="https://site.test/f" htmlUrl="`+long+`"/>
	</body></opml>`, ImportOptions{})
	require.Equal(t, 2, r.FeedsAdded)
	require.Equal(t, 2, r.FeedsRepeated, "the same address again, in the same folder or another")
	require.Len(t, r.Skipped, 2)
	require.Equal(t, 6, r.FeedsAdded+len(r.FeedsExisting)+len(r.Skipped)+r.FeedsRepeated, "every outline counted once")
	require.Equal(t, "not a web address (it must start with http:// or https://)", r.Skipped[0].Reason)
	require.Equal(t, "the address is longer than 4,096 characters", r.Skipped[1].Reason)
	require.Less(t, len(r.Skipped[1].URL), 300, "the report cuts an overlong address")
	var site string
	require.NoError(t, db.Reader().QueryRow("SELECT site_url FROM feeds WHERE url = 'https://site.test/f'").Scan(&site))
	require.Empty(t, site, "an overlong site address is dropped")
}
