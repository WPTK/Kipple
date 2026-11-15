package sched

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/WPTK/kipple/internal/feedurl"
	"github.com/WPTK/kipple/internal/fetch"
)

// The feed's host and the host of the last hop are compared as feeds.host is derived, so a spelling that
// feeds.host folds (case, a trailing dot, a non-ASCII name) is still the feed's own host.
func TestAnsweredByFeedHostComparesHostsAsStored(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, feed, last string
		want             bool
	}{
		{"no redirect", "https://example.com/f", "", true},
		{"same host", "https://example.com/f", "https://example.com/moved", true},
		{"upper case", "https://example.com/f", "https://EXAMPLE.com/moved", true},
		{"another host", "https://example.com/f", "https://example.org/rss", false},
		{"a subdomain is another host", "https://example.com/f", "https://www.example.com/f", false},
		{"trailing dot kept by both", "https://example.com./f", "https://example.com./moved", true},
		{"trailing dot on one side only", "https://example.com/f", "https://example.com./moved", false},
		{"non-ASCII host", "https://bücher.example/f", "https://b%C3%BCcher.example/moved", true},
		{"non-ASCII capital", "https://İstanbul.example/f", "https://İstanbul.example/moved", true},
		{"IPv6 literal", "https://[::1]:8080/f", "https://[::1]:9090/moved", true},
		{"unparseable target", "https://example.com/f", "https://exa mple.com/x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, err := feedurl.Host(tc.feed)
			require.NoError(t, err)
			var hops []fetch.Hop
			if tc.last != "" {
				hops = []fetch.Hop{{Status: 302, From: tc.feed, To: "https://elsewhere.example/a"}, {Status: 302, From: "https://elsewhere.example/a", To: tc.last}}
			}
			require.Equal(t, tc.want, answeredByFeedHost(hops, host))
		})
	}
}
