package fetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Any JSON object parses as a JSON Feed in the library; only the version member makes it one.
func TestParseFeedRefusesJSONThatIsNotAFeed(t *testing.T) {
	strict := ParseOptions{FeedURL: "https://x.example/f.json", Strict: true}
	_, err := ParseFeed([]byte(`{"a":1}`), strict)
	require.Error(t, err)
	for _, ok := range []string{
		`{"version":"https://jsonfeed.org/version/1.1","title":"T","items":[]}`,
		`{"title":"T","items":[]}`,                 // no version member
		`{"version":"1.1","title":"T","items":[]}`, // bare version
		`{"version":"1.1","title":"T","items":[{"id":"1","url":"https://x.example/1","content_text":"hi"}]}`,
	} {
		_, err = ParseFeed([]byte(ok), strict)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{`{"title":"T"}`, `{"version":"2","items":[]}`, `{"data":[1,2]}`} {
		_, err = ParseFeed([]byte(bad), strict)
		require.Error(t, err, bad)
	}
}

// A feed already subscribed keeps parsing whatever version string its publisher writes, as long as the
// document has an items array; only a document with none (no longer a feed at all) fails.
func TestParseFeedLenientForSubscribedFeeds(t *testing.T) {
	lax := ParseOptions{FeedURL: "https://x.example/f.json"}
	for _, ok := range []string{
		`{"version":"1","title":"T","items":[{"id":"1","url":"https://x.example/1","content_text":"hi"}]}`,
		`{"version":"1.0","title":"T","items":[]}`,
		`{"version":"2","items":[]}`,
	} {
		_, err := ParseFeed([]byte(ok), lax)
		require.NoError(t, err, ok)
		_, err = ParseFeed([]byte(ok), ParseOptions{FeedURL: lax.FeedURL, Strict: true})
		require.Error(t, err, "adding refuses it: "+ok)
	}
	for _, bad := range []string{`{"a":1}`, `{"title":"T"}`, `{"data":[1,2]}`} {
		_, err := ParseFeed([]byte(bad), lax)
		require.Error(t, err, bad)
	}
}
