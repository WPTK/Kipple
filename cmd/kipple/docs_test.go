package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	require.NoError(t, err)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// #124: the README and docs/deploy.md tell a newcomer to run `docker logs
// kipple` and `docker exec kipple /kipple ...`, so the pull-and-run
// compose file (and the README's copy of it) must name the container kipple.
func TestPullAndRunComposeNamesTheContainer(t *testing.T) {
	require.Contains(t, readRepoFile(t, "docker-compose.pull.example.yml"), "\n    container_name: kipple\n")
	require.Contains(t, readRepoFile(t, "docker-compose.example.yml"), "\n    container_name: kipple\n")
	readme := readRepoFile(t, "README.md")
	i := strings.Index(readme, "image: ghcr.io/wptk/kipple:")
	require.Positive(t, i)
	block := readme[i:]
	block = block[:strings.Index(block, "```")]
	require.Contains(t, block, "container_name: kipple", "the README's copy of the pull-and-run file")
	require.Contains(t, readme, "docker logs kipple")
}

// #132: the docs describe TZ and the port as they work.
func TestDocsDescribeTheZoneAndPortAsTheyWork(t *testing.T) {
	design := readRepoFile(t, "docs/design.md")
	for _, stale := range []string{"store.LoadLocation", "The container `TZ` only sets `time.Local`", "in the `tz` setting (default `America/New_York`"} {
		require.NotContains(t, design, stale)
	}
	require.Contains(t, design, "`store.Zone`")
	require.Contains(t, readRepoFile(t, "docker-compose.example.yml"), `- "127.0.0.1:1919:1919"`)
	require.Contains(t, readRepoFile(t, ".env.example"), "# KIPPLE_ADDR=:1919\n")
	require.Contains(t, readRepoFile(t, "README.md"), "| `KIPPLE_ADDR` | Listen address, default `:1919`.")
	require.Contains(t, readRepoFile(t, "docs/deploy.md"), "The default listen address is `:1919`.")
}

// User-facing text describes Kipple as it is, to someone who never ran an earlier version: release history lives in
// CHANGELOG.md and the decision records, not in the README, the deploy guide or the example files.
func TestUserDocsCarryNoVersionHistory(t *testing.T) {
	history := regexp.MustCompile(`(?i)\b(since|pre-|before|from|in|until) v?0\.\d`)
	for _, name := range []string{"README.md", "docs/deploy.md", ".env.example", "docker-compose.example.yml", "docker-compose.pull.example.yml"} {
		require.Empty(t, history.FindAllString(readRepoFile(t, name), -1), name)
	}
}
