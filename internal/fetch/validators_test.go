package fetch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A Last-Modified value is opaque to Kipple: the server compares the string it
// sent, so any safe-to-echo value is kept exactly as received.
func TestCleanValidatorsKeepsLastModifiedAsSent(t *testing.T) {
	for _, lm := range []string{
		"Wed, 01 Jan 2025 10:00:00 GMT",
		"Wed, 1 Jan 2025 10:00:00 GMT",
		"Wed, 01 Jan 2025 10:00:00 UTC",
		"Wed, 01 Jan 2025 10:00:00 +0000",
		"Wednesday, 01-Jan-25 10:00:00 GMT",
		"Wed Jan  1 10:00:00 2025",
		"version-17",
	} {
		_, got := CleanValidators("", lm)
		require.Equal(t, lm, got)
	}
}

func TestCleanValidatorsDropsUnsafeLastModified(t *testing.T) {
	for name, lm := range map[string]string{
		"line break":   "Wed, 01 Jan 2025 10:00:00 GMT\r\nX: y",
		"control byte": "Wed\x00",
		"non ascii":    "Wed, 01 Jan 2025 é",
		"too long":     strings.Repeat("x", 65),
	} {
		t.Run(name, func(t *testing.T) {
			_, got := CleanValidators("", lm)
			require.Equal(t, "", got)
		})
	}
}
