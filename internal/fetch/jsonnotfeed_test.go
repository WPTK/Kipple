package fetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Any JSON object parses as a JSON Feed in the library; only the version member makes it one.
func TestParseFeedRefusesJSONThatIsNotAFeed(t *testing.T) {
	_, err := ParseFeed([]byte(`{"a":1}`), ParseOptions{FeedURL: "https://x.example/f.json"})
	require.Error(t, err)
	for _, ok := range []string{
		`{"version":"https://jsonfeed.org/version/1.1","title":"T","items":[]}`,
		`{"title":"T","items":[]}`,                 // no version member
		`{"version":"1.1","title":"T","items":[]}`, // bare version
		`{"version":"1.1","title":"T","items":[{"id":"1","url":"https://x.example/1","content_text":"hi"}]}`,
	} {
		_, err = ParseFeed([]byte(ok), ParseOptions{FeedURL: "https://x.example/f.json"})
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{`{"title":"T"}`, `{"version":"2","items":[]}`, `{"data":[1,2]}`} {
		_, err = ParseFeed([]byte(bad), ParseOptions{FeedURL: "https://x.example/f.json"})
		require.Error(t, err, bad)
	}
}
