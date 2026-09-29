package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// `kipple version` prints the version and nothing else: docs/RELEASING.md step 10 compares against it.
func TestVersionIsJustTheVersion(t *testing.T) {
	old := version
	version = "v9.9.9-test.1"
	t.Cleanup(func() { version = old })

	var out bytes.Buffer
	require.NoError(t, runVersion(nil, &out))
	require.Equal(t, "v9.9.9-test.1\n", out.String())
}

func TestVersionVerbose(t *testing.T) {
	oldV, oldC, oldD := version, commit, buildDate
	version, commit, buildDate = "v9.9.9-test.1", "0123456789abcdef0123456789abcdef01234567", "2026-10-01T00:00:00Z"
	t.Cleanup(func() { version, commit, buildDate = oldV, oldC, oldD })

	for _, flag := range []string{"-v", "--verbose"} {
		var out bytes.Buffer
		require.NoError(t, runVersion([]string{flag}, &out))
		lines := strings.Split(out.String(), "\n")
		require.Equal(t, "v9.9.9-test.1", lines[0], "the first line is the same as plain `version`")
		for _, want := range []string{"commit: 0123456789abcdef0123456789abcdef01234567", "built: 2026-10-01T00:00:00Z", "go: go", "platform: ", "schema: ", "web build: "} {
			require.Contains(t, out.String(), want)
		}
	}
}

func TestVersionRejectsOtherArguments(t *testing.T) {
	var out bytes.Buffer
	require.Error(t, runVersion([]string{"-x"}, &out))
	require.Empty(t, out.String())
}
