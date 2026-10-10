package fetch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Any JSON object parses as a JSON Feed in the library; only the version member makes it one.
func TestParseFeedRefusesJSONThatIsNotAFeed(t *testing.T) {
	_, err := ParseFeed([]byte(`{"a":1}`), ParseOptions{FeedURL: "https://x.example/f.json"})
	require.Error(t, err)
	_, err = ParseFeed([]byte(`{"version":"https://jsonfeed.org/version/1.1","title":"T","items":[]}`), ParseOptions{FeedURL: "https://x.example/f.json"})
	require.NoError(t, err)
}
