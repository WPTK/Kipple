package main

import (
	"os"
	"path/filepath"
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
// kipple` and `docker exec kipple /kipple setup-token`, so the pull-and-run
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

// #132: the docs describe TZ and the port as they now work.
func TestDocsDescribeTheZoneAndPortAsTheyWork(t *testing.T) {
	design := readRepoFile(t, "docs/design.md")
	for _, stale := range []string{"store.LoadLocation", "The container `TZ` only sets `time.Local`", "in the `tz` setting (default `America/New_York`"} {
		require.NotContains(t, design, stale)
	}
	require.Contains(t, design, "`store.Zone`")
	compose := readRepoFile(t, "docker-compose.example.yml")
	require.Contains(t, compose, "when KIPPLE_ADDR is unset and the database dates from before 0.5",
		"a pre-0.5 database keeps 7080 without any .env line too")
}
