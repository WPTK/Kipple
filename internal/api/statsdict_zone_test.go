package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// #132: the dictionary that ships in every stats export says that a set TZ,
// not only the tz setting, decides the local fields and the export's tz.
func TestStatsDictionaryLocalTimeNamesTZ(t *testing.T) {
	var text string
	for _, c := range statsConcepts {
		if c.Name == "local_time" {
			text = c.Text
		}
	}
	require.NotEmpty(t, text)
	require.Contains(t, text, "TZ environment variable")
	require.Contains(t, text, "else the tz setting")
}
