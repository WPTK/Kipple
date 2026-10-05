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

// A feed added without a name stores no title and is named by its URL until its first fetch, which names it with the
// title the feed gives itself and reports the change (the scheduler announces it as feed.changed).
func TestFirstFetchNamesAnUntitledFeed(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml"})
	require.Equal(t, "https://news.example/feed.xml", e.name(id))
	require.Equal(t, "", scalar[string](t, e.db.Reader(), "SELECT title FROM feeds WHERE id = ?", id), "no placeholder is stored")

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

// A document with no title, or only whitespace, leaves the feed named by its URL; a failed first fetch (the
// feed unreachable) leaves it too, and the next successful one names the feed.
func TestUntitledDocumentAndFailedFetchKeepTheURL(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://news.example/feed.xml"})
	require.NoError(t, e.db.CommitFetchError(e.ctx, &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError,
		ErrClass: "network", ErrMsg: "connection refused", NextFetchAt: e.clk.Now().Add(time.Hour), CurrentDelayS: 3600}))
	require.Equal(t, "https://news.example/feed.xml", e.name(id))

	e.clk.Advance(time.Hour)
	require.False(t, e.commit(e.okResult(e.snap(id), titledDoc(" \n\t "))).Retitled)
	require.Equal(t, "https://news.example/feed.xml", e.name(id))

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

	// The one exception (design §4.5): a given name that is exactly the feed's own title at its first
	// successful fetch is dropped, so the feed then follows its own renames. That is what keeps an OPML
	// round trip from pinning every exported title as a custom name.
	same := e.subscribe(SubscribeOpts{URL: "https://same.example/feed.xml", Title: "Daily News"})
	e.commit(e.okResult(e.snap(same), titledDoc("Daily News")))
	require.Zero(t, e.count("SELECT count(*) FROM feeds WHERE id = ? AND custom_title IS NOT NULL", same))
	e.clk.Advance(time.Hour)
	require.True(t, e.commit(e.okResult(e.snap(same), titledDoc("Daily News Weekly"))).Retitled)
	require.Equal(t, "Daily News Weekly", e.name(same))
}

// The display name has one definition (feedTitleSQL): blank or whitespace-only titles count as
// absent, in every list and in the fetch commit's rename check, so a row with empty strings is named
// by its URL and its first titled fetch is announced.
func TestBlankTitlesCountAsAbsent(t *testing.T) {
	e := newEnv(t)
	id := e.subscribe(SubscribeOpts{URL: "https://blank.example/feed.xml"})
	e.exec("UPDATE feeds SET custom_title = '  ', title = '' WHERE id = ?", id)
	require.Equal(t, "https://blank.example/feed.xml", e.name(id))
	subs, err := e.db.Subscriptions(e.ctx)
	require.NoError(t, err)
	require.Equal(t, "https://blank.example/feed.xml", subs[0].Title, "the Reader API names it the same way")

	require.True(t, e.commit(e.okResult(e.snap(id), titledDoc("Blank no more"))).Retitled)
	require.Equal(t, "Blank no more", e.name(id))
	subs, err = e.db.Subscriptions(e.ctx)
	require.NoError(t, err)
	require.Equal(t, "Blank no more", subs[0].Title)
}
