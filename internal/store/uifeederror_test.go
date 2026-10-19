package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/fetch"
)

// The feed list carries the reason a feed is failing (issue #476), and drops it
// once a fetch works again although the column keeps it for Feed health.
func TestUIFeedLastError(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	id := e.addFeed("http://a.example/feed")
	lastErr := func() *string {
		feeds, err := e.db.UIFeeds(e.ctx, StatusEnv{Now: e.clk.Now()})
		require.NoError(t, err)
		require.Len(t, feeds, 1)
		return feeds[0].LastError
	}
	require.Nil(t, lastErr(), "a new feed has no error")

	res := &fetch.Result{Snap: e.snap(id), StartedAt: e.clk.Now(), Outcome: fetch.OutcomeError, ErrClass: "dns",
		ErrMsg: "lookup a.example: no such host", NextFetchAt: e.clk.Now().Add(time.Hour), CurrentDelayS: 3600}
	require.NoError(t, e.db.CommitFetchError(e.ctx, res))
	got := lastErr()
	require.NotNil(t, got)
	require.Equal(t, "lookup a.example: no such host", *got)

	e.fetchBody(id, rss(numbered(1)...))
	require.Nil(t, lastErr(), "a working feed shows no error")
	require.Equal(t, 1, e.count("SELECT count(*) FROM feeds WHERE last_error IS NOT NULL"), "Feed health keeps the earlier error")
}
