package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The dictionary that ships in every stats export says that the tz setting alone
// decides the local fields and the export's tz.
func TestStatsDictionaryLocalTimeNamesTheSetting(t *testing.T) {
	var text string
	for _, c := range statsConcepts {
		if c.Name == "local_time" {
			text = c.Text
		}
	}
	require.NotEmpty(t, text)
	require.Contains(t, text, "the tz setting")
	require.NotContains(t, text, "TZ environment variable")
}
