package buildinfo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalized(t *testing.T) {
	require.Equal(t, Info{Version: "dev", Commit: Unknown, BuildDate: Unknown}, Info{}.Normalized())
	got := Info{Version: "v1.2.3", Commit: " abc ", BuildDate: "2026-01-02T03:04:05Z"}.Normalized()
	require.Equal(t, "abc", got.Commit)
	require.Equal(t, "v1.2.3", got.Version)
}

// The first line of the verbose report is exactly what plain `kipple version` prints: docs/RELEASING.md
// step 10 compares against it.
func TestReportFirstLineIsTheVersion(t *testing.T) {
	r := Info{Version: "v0.5.0-beta.1", Commit: "0123456789abcdef", BuildDate: "2026-10-01T00:00:00Z"}.Report(9, "abc123")
	lines := strings.Split(r, "\n")
	require.Equal(t, "v0.5.0-beta.1", lines[0])
	for _, want := range []string{"commit: 0123456789abcdef", "built: 2026-10-01T00:00:00Z", "go: go", "platform: ", "schema: 9", "web build: abc123"} {
		require.Contains(t, r, want)
	}
}

func TestReportUnknowns(t *testing.T) {
	r := Info{Version: "dev"}.Report(1, "")
	require.Contains(t, r, "commit: unknown")
	require.Contains(t, r, "built: unknown")
	require.Contains(t, r, "web build: unknown")
}
