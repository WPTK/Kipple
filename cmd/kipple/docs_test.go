package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	for name, re := range map[string]string{
		"docker-compose.example.yml":      `"127\.0\.0\.1:1919:1919"`,
		"docker-compose.pull.example.yml": `"127\.0\.0\.1:1919:1919"`,
		".env.example":                    `(?m)^# KIPPLE_ADDR=:1919$`,
		"README.md":                       "(?m)^\\| `KIPPLE_ADDR` \\|[^\\n]*`:1919`",
		"docs/deploy.md":                  "default listen address is `:1919`",
	} {
		require.Regexp(t, re, readRepoFile(t, name), name)
	}
}

// userDocs are what a self-hoster or a new contributor reads to run Kipple.
var userDocs = []string{"README.md", "docs/deploy.md", ".env.example", "docker-compose.example.yml",
	"docker-compose.pull.example.yml", "web/README.md"}

// Terms with no current use in the user docs: each only ever told the story of an earlier version.
var historyTerms = []string{"7080", "no longer", "any more", "anymore", "used to ", "setup code", "setup-token",
	"legacy_port", "open_lan"}

// versionRef finds a version named as a point in time ("since 0.6.0", "pre-0.5", "release 0.5.0-beta.1", "as of 0.7").
var versionRef = regexp.MustCompile(`(?i)(?:\b(?:since|before|from|in|until|as of|after|to|release|version)\s+|\bpre-)v?(\d+)\.\d+(?:\.\d+)?(?:-[a-z]+\.\d+)?`)

// notAVersion is what may follow such a number when it is a duration, a size or an address, not a version.
var notAVersion = regexp.MustCompile(`^(?:\.\d|\s*(?:s|ms|µs|min|h|MB|MiB|GB|GiB|KB|KiB|%|x)\b)`)

// releasedMajor is the major version of the newest release in CHANGELOG.md. A version with a higher major is still
// ahead ("testing before 1.0"), so naming it is not history.
func releasedMajor(t *testing.T) int {
	m := regexp.MustCompile(`(?m)^## \[(\d+)\.\d+\.\d+`).FindStringSubmatch(readRepoFile(t, "CHANGELOG.md"))
	require.NotNil(t, m, "CHANGELOG.md has no released version")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

// User-facing text describes Kipple as it is, to someone who never ran an earlier version: release history lives in
// CHANGELOG.md and the decision records, not in the README, the deploy guide, the example files or web/README.md.
func TestUserDocsCarryNoVersionHistory(t *testing.T) {
	major := releasedMajor(t)
	for _, name := range userDocs {
		text := readRepoFile(t, name)
		lower := strings.ToLower(text)
		for _, term := range historyTerms {
			require.NotContains(t, lower, term, name)
		}
		for _, loc := range versionRef.FindAllStringSubmatchIndex(text, -1) {
			if notAVersion.MatchString(text[loc[1]:]) {
				continue
			}
			if m, _ := strconv.Atoi(text[loc[2]:loc[3]]); m > major {
				continue
			}
			t.Errorf("%s: %q names an earlier version; describe Kipple as it is (history goes in CHANGELOG.md)", name, text[loc[0]:loc[1]])
		}
	}
}

func TestVersionRefCatchesHistory(t *testing.T) {
	for _, in := range []string{"since 0.6.0", "pre-0.5", "release 0.5.0-beta.1", "as of 0.7", "after 0.7", "to 0.7", "Since v1.2"} {
		loc := versionRef.FindStringIndex(in)
		require.NotNil(t, loc, in)
		require.False(t, notAVersion.MatchString(in[loc[1]:]), in)
	}
	for _, in := range []string{"in 0.5 s", "after 1.5 s", "in 0.5ms", "to 1.5 MB", "to 127.0.0.1"} {
		loc := versionRef.FindStringIndex(in)
		require.True(t, loc == nil || notAVersion.MatchString(in[loc[1]:]), in)
	}
}
