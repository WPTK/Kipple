package store

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// titledDoc is an RSS document whose channel title is title, with one item.
func titledDoc(title string) []byte {
	return []byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>` + title + `</title><link>https://news.example/</link>` +
		`<item><guid>a</guid><title>t</title><link>https://news.example/a</link></item></channel></rss>`)
}

func (e *env) name(id int64) string {
	e.t.Helper()
	n, err := e.db.FeedName(e.ctx, id)
	require.NoError(e.t, err)
	return n
}

func (e *env) subscribe(o SubscribeOpts) int64 {
	e.t.Helper()
	res, err := e.db.Subscribe(e.ctx, o)
	require.NoError(e.t, err)
	require.False(e.t, res.Existed)
	return res.FeedID
}

// A feed added without a name is named after its host until its first fetch, which names it with the
// title the feed gives itself and reports the change (the scheduler announces it as feed.changed).
func TestFirstFetchNamesAnUntitledFeed(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml"})
	require.Equal(t, "news.example", e.name(id))

	info := e.commit(e.okResult(e.snap(id), titledDoc("Daily  News")))
	require.True(t, info.Retitled)
	require.Equal(t, "Daily News", e.name(id))
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ? AND custom_title IS NOT NULL", id), "the feed's own title is not a custom name")

	e.clk.Advance(time.Hour)
	require.False(t, e.commit(e.okResult(e.snap(id), titledDoc("Daily News"))).Retitled, "same name: nothing to announce")

	e.clk.Advance(time.Hour)
	require.True(t, e.commit(e.okResult(e.snap(id), titledDoc("Daily News Weekly"))).Retitled, "the feed's own later rename flows through")
	require.Equal(t, "Daily News Weekly", e.name(id))
}

// A document with no title, or only whitespace, keeps the placeholder name; a failed first fetch (the
// feed unreachable) leaves it too, and the next successful one names the feed.
func TestUntitledDocumentAndFailedFetchKeepTheHost(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml"})
	require.NoError(t, e.db.CommitFetchError(e.ctx, &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError,
		ErrClass: "network", ErrMsg: "connection refused", NextFetchAt: e.clk.Now().Add(time.Hour), CurrentDelayS: 3600}))
	require.Equal(t, "news.example", e.name(id))

	e.clk.Advance(time.Hour)
	require.False(t, e.commit(e.okResult(e.snap(id), titledDoc(" \n\t "))).Retitled)
	require.Equal(t, "news.example", e.name(id))

	e.clk.Advance(time.Hour)
	require.True(t, e.commit(e.okResult(e.snap(id), titledDoc("Named at last"))).Retitled)
	require.Equal(t, "Named at last", e.name(id))
}

// The name the feed gives itself is plain text on one line: entities decoded (twice-escaped ones too),
// whitespace runs collapsed, and cut to fetch.MaxTitleRunes with an ellipsis.
func TestFeedTitleIsNormalized(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml"})
	e.commit(e.okResult(e.snap(id), titledDoc("\n  Tom &amp;amp; Jerry&#8217;s\n\t  Blog  \n")))
	require.Equal(t, "Tom & Jerry’s Blog", e.name(id))

	long := e.subscribe(SubscribeOpts{URL: "https://long.example/feed.xml"})
	e.commit(e.okResult(e.snap(long), titledDoc(strings.Repeat("é", 300))))
	got := []rune(e.name(long))
	require.Len(t, got, fetch.MaxTitleRunes)
	require.Equal(t, '…', got[len(got)-1])
}

// A name given when the feed is added wins over the feed's own title, at the first fetch and after;
// a rename later sticks the same way.
func TestGivenNameAndRenamesStick(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml", Title: "  My picks  "})
	require.Equal(t, "My picks", e.name(id))
	require.False(t, e.commit(e.okResult(e.snap(id), titledDoc("Daily News"))).Retitled)
	require.Equal(t, "My picks", e.name(id))
	require.Equal(t, "Daily News", scalar[string](t, e.db.Reader(), "SELECT title FROM feeds WHERE id = ?", id), "the feed's own title is still kept")

	_, err := e.db.EditSubscription(e.ctx, []FeedRef{{ID: id}}, EditOpts{Title: "Renamed"})
	require.NoError(t, err)
	e.clk.Advance(time.Hour)
	require.False(t, e.commit(e.okResult(e.snap(id), titledDoc("Daily News, new look"))).Retitled)
	require.Equal(t, "Renamed", e.name(id))

	// A given name equal to the feed's own title is not kept as a custom name: the feed's later
	// renames then flow through.
	same := e.subscribe(SubscribeOpts{URL: "https://same.example/feed.xml", Title: "Daily News"})
	e.commit(e.okResult(e.snap(same), titledDoc("Daily News")))
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ? AND custom_title IS NOT NULL", same))
}
