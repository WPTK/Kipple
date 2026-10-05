package fetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeedTitle(t *testing.T) {
	require.Equal(t, "", FeedTitle(" \n\t "))
	require.Equal(t, "Daily News", FeedTitle("\n  Daily\n\t  News  "))
	require.Equal(t, "a < b & c; Q&A &nbsp", FeedTitle("a < b & c; Q&A &nbsp"), "plain text stays as written")
	require.Equal(t, "Tom & Jerry’s …", FeedTitle("Tom &amp; Jerry&#8217;s &hellip;"), "a level of escaping left behind is undone")
	require.Equal(t, "&amp; once", FeedTitle("&amp;amp; once"), "one level only")
	require.Equal(t, "&notit; and &bogus; stay; a;b", FeedTitle("&notit; and &bogus; stay&semi; a;b"),
		"only whole known references: no legacy prefix decoding")
	require.Equal(t, "Bell and null gone", FeedTitle("Bell\x07 and\x00 null\x1f gone"), "control characters dropped")
	require.Equal(t, "a b", FeedTitle("a&#9;b"), "a decoded control counts too")
	require.Equal(t, "Tips &amp;amp; tricks", CleanName("Tips &amp;amp; tricks"), "a written name keeps its entities")
	require.Equal(t, "x y", CleanName("x\u0085\x01y"), "C1 whitespace is a space, other controls go")
	require.Equal(t, strings.Repeat("x", MaxTitleRunes), FeedTitle(strings.Repeat("x", MaxTitleRunes)), "at the limit: untouched")
	cut := []rune(FeedTitle(strings.Repeat("é", MaxTitleRunes+1)))
	require.Len(t, cut, MaxTitleRunes)
	require.Equal(t, '…', cut[len(cut)-1])
	require.Equal(t, strings.Repeat("a", MaxTitleRunes-2)+"…", FeedTitle(strings.Repeat("a", MaxTitleRunes-2)+" b"+strings.Repeat("c", 10)),
		"no space before the ellipsis")
}

// The title a document gives itself reaches Feed.Title decoded and on one line, in every format.
func TestParsedFeedTitle(t *testing.T) {
	for name, doc := range map[string]string{
		"rss":  `<?xml version="1.0"?><rss version="2.0"><channel><title>` + "\n  Tom &amp;amp; Jerry&#8217;s\n\t Blog " + `</title></channel></rss>`,
		"atom": `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title type="html">` + "Tom &amp;amp; Jerry&amp;#8217;s\n Blog" + `</title></feed>`,
		"json": `{"version":"https://jsonfeed.org/version/1.1","title":"Tom & Jerry’s\n Blog","items":[]}`,
	} {
		f, err := ParseFeed([]byte(doc), ParseOptions{FeedURL: "https://ex.com/feed"})
		require.NoError(t, err, name)
		require.Equal(t, "Tom & Jerry’s Blog", f.Title, name)
	}
}
